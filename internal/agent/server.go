package agent

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// Agent is the in-sandbox control process. It is safe for concurrent sessions.
type Agent struct {
	token      []byte
	mu         sync.Mutex
	procs      map[string]*proc
	watchEvery time.Duration
	maxProcs   int
}

// New returns an agent that requires token on every connection.
func New(token string) *Agent {
	return &Agent{
		token:      []byte(token),
		procs:      map[string]*proc{},
		watchEvery: 200 * time.Millisecond,
		maxProcs:   128,
	}
}

// ServeConn speaks one authenticated session. It returns when the connection ends.
func (a *Agent) ServeConn(conn io.ReadWriteCloser) {
	defer conn.Close()
	if d, ok := conn.(interface{ SetReadDeadline(time.Time) error }); ok {
		_ = d.SetReadDeadline(time.Now().Add(10 * time.Second))
	}
	frame, err := ReadFrame(conn)
	if err != nil {
		return
	}
	if frame.Type != MsgHello || !tokenOK(frame.Data, a.token) {
		_ = WriteFrame(conn, 0, MsgError, []byte("unauthorized"))
		return
	}
	body, _ := json.Marshal(HelloOK{Version: ProtocolVersion, Caps: []string{"exec", "pty", "files", "watch", "preview"}})
	if err := WriteFrame(conn, 0, MsgHelloOk, body); err != nil {
		return
	}
	if d, ok := conn.(interface{ SetReadDeadline(time.Time) error }); ok {
		_ = d.SetReadDeadline(time.Time{})
	}
	mux := NewMux(conn, false)
	mux.onStream = func(s *Stream) {
		defer mux.remove(s.id)
		defer func() {
			if rec := recover(); rec != nil {
				_ = s.Send(MsgError, []byte("internal error"))
			}
		}()
		a.handle(s)
	}
	mux.ReadLoop(conn)
}

func tokenOK(got, want []byte) bool {
	if len(got) != len(want) || len(want) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (a *Agent) handle(s *Stream) {
	frame, err := s.Recv(s.ctx)
	if err != nil {
		return
	}
	switch frame.Type {
	case MsgExec:
		a.handleExec(s, frame)
	case MsgProcList:
		a.reply(s, a.listPayload())
	case MsgProcSignal:
		a.handleSignal(s, frame)
	case MsgProcAttach:
		a.handleAttach(s, frame)
	case MsgProcStdin:
		a.handleProcStdin(s, frame)
	case MsgResize:
		a.handleResize(s, frame)
	case MsgFileList:
		a.handleFileList(s, frame)
	case MsgFileStat:
		a.handleFileStat(s, frame)
	case MsgFileRead:
		a.handleFileRead(s, frame)
	case MsgFileWrite:
		a.handleFileWrite(s, frame)
	case MsgFileRemove:
		a.handleFileRemove(s, frame)
	case MsgFileMkdir:
		a.handleFileMkdir(s, frame)
	case MsgFileWatch:
		a.handleWatch(s, frame)
	case MsgDial:
		a.handleDial(s, frame)
	default:
		_ = s.Send(MsgError, []byte("unknown message"))
	}
}

func (a *Agent) reply(s *Stream, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		_ = s.Send(MsgError, []byte(err.Error()))
		return
	}
	_ = s.Send(MsgOK, b)
}

func (a *Agent) fail(s *Stream, err error) {
	if err == nil {
		err = errors.New("error")
	}
	_ = s.Send(MsgError, []byte(err.Error()))
}

func (a *Agent) handleExec(s *Stream, frame Frame) {
	var spec ExecSpec
	if err := json.Unmarshal(frame.Data, &spec); err != nil {
		a.fail(s, err)
		return
	}
	p, err := a.spawn(spec)
	if err != nil {
		a.fail(s, err)
		return
	}
	if spec.Background {
		a.reply(s, p.info())
		return
	}
	sub, replay, exited, _, _ := p.subscribe()
	defer p.unsubscribe(sub)
	go a.pumpStdin(s, p)
	if err := writeChunks(s, replay); err != nil {
		return
	}
	if exited {
		_ = s.Send(MsgExit, mustJSON(p.result()))
		return
	}
	a.streamUntilExit(s, p, sub)
}

func (a *Agent) pumpStdin(s *Stream, p *proc) {
	for {
		frame, err := s.Recv(s.ctx)
		if err != nil {
			return
		}
		switch frame.Type {
		case MsgStdin, MsgData:
			_ = p.writeStdin(frame.Data)
		case MsgStdinEOF:
			_ = p.closeStdin()
			return
		case MsgKill:
			_ = p.signal("KILL")
			return
		case MsgResize:
			var body struct {
				Cols int `json:"cols"`
				Rows int `json:"rows"`
			}
			if json.Unmarshal(frame.Data, &body) == nil {
				_ = p.resize(body.Cols, body.Rows)
			}
		}
	}
}

func (a *Agent) streamUntilExit(s *Stream, p *proc, sub *subscriber) {
	if sub == nil {
		_ = s.Send(MsgExit, mustJSON(p.result()))
		return
	}
	for {
		select {
		case <-s.ctx.Done():
			return
		case chunk := <-sub.ch:
			if err := s.Send(chunk.kind, chunk.data); err != nil {
				return
			}
		case <-sub.done:
			for {
				select {
				case chunk := <-sub.ch:
					if err := s.Send(chunk.kind, chunk.data); err != nil {
						return
					}
				default:
					_ = s.Send(MsgExit, mustJSON(p.result()))
					return
				}
			}
		}
	}
}

func writeChunks(s *Stream, chunks []outChunk) error {
	for _, chunk := range chunks {
		if err := s.Send(chunk.kind, chunk.data); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) listPayload() map[string]any {
	return map[string]any{"processes": a.list()}
}

func (a *Agent) handleSignal(s *Stream, frame Frame) {
	var body struct {
		ID     string `json:"id"`
		Signal string `json:"signal"`
	}
	if err := json.Unmarshal(frame.Data, &body); err != nil {
		a.fail(s, err)
		return
	}
	p, ok := a.get(body.ID)
	if !ok {
		a.fail(s, errors.New("process not found"))
		return
	}
	if err := p.signal(body.Signal); err != nil {
		a.fail(s, err)
		return
	}
	_ = s.Send(MsgOK, []byte(`{}`))
}

func (a *Agent) handleResize(s *Stream, frame Frame) {
	var body struct {
		ID   string `json:"id"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	if err := json.Unmarshal(frame.Data, &body); err != nil {
		a.fail(s, err)
		return
	}
	p, ok := a.get(body.ID)
	if !ok {
		a.fail(s, errors.New("process not found"))
		return
	}
	if err := p.resize(body.Cols, body.Rows); err != nil {
		a.fail(s, err)
		return
	}
	_ = s.Send(MsgOK, []byte(`{}`))
}

func (a *Agent) handleAttach(s *Stream, frame Frame) {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(frame.Data, &body); err != nil {
		a.fail(s, err)
		return
	}
	p, ok := a.get(body.ID)
	if !ok {
		a.fail(s, errors.New("process not found"))
		return
	}
	sub, replay, exited, _, _ := p.subscribe()
	defer p.unsubscribe(sub)
	if err := writeChunks(s, replay); err != nil {
		return
	}
	if exited {
		_ = s.Send(MsgExit, mustJSON(p.result()))
		return
	}
	a.streamUntilExit(s, p, sub)
}

func (a *Agent) handleProcStdin(s *Stream, frame Frame) {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(frame.Data, &body); err != nil {
		a.fail(s, err)
		return
	}
	p, ok := a.get(body.ID)
	if !ok {
		a.fail(s, errors.New("process not found"))
		return
	}
	for {
		f, err := s.Recv(s.ctx)
		if err != nil {
			return
		}
		switch f.Type {
		case MsgData, MsgStdin:
			if err := p.writeStdin(f.Data); err != nil {
				a.fail(s, err)
				return
			}
		case MsgStdinEOF:
			if err := p.closeStdin(); err != nil {
				a.fail(s, err)
				return
			}
			_ = s.Send(MsgOK, []byte(`{}`))
			return
		case MsgEnd:
			_ = s.Send(MsgOK, []byte(`{}`))
			return
		case MsgKill:
			return
		default:
			a.fail(s, errors.New("expected stdin bytes"))
			return
		}
	}
}

func (a *Agent) handleFileList(s *Stream, frame Frame) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(frame.Data, &body); err != nil {
		a.fail(s, err)
		return
	}
	dir, err := cleanAbs(body.Path)
	if err != nil {
		a.fail(s, err)
		return
	}
	entries, err := listDir(dir)
	if err != nil {
		a.fail(s, err)
		return
	}
	a.reply(s, map[string]any{"entries": entries})
}

func (a *Agent) handleFileStat(s *Stream, frame Frame) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(frame.Data, &body); err != nil {
		a.fail(s, err)
		return
	}
	p, err := cleanAbs(body.Path)
	if err != nil {
		a.fail(s, err)
		return
	}
	info, err := statInfo(p)
	if err != nil {
		a.fail(s, err)
		return
	}
	a.reply(s, info)
}

func (a *Agent) handleFileRead(s *Stream, frame Frame) {
	var body struct {
		Path string `json:"path"`
		Max  int    `json:"max"`
	}
	if err := json.Unmarshal(frame.Data, &body); err != nil {
		a.fail(s, err)
		return
	}
	p, err := cleanAbs(body.Path)
	if err != nil {
		a.fail(s, err)
		return
	}
	data, err := readLimited(p, body.Max)
	if err != nil {
		a.fail(s, err)
		return
	}
	for len(data) > 0 {
		n := len(data)
		if n > ChunkSize {
			n = ChunkSize
		}
		if err := s.Send(MsgData, data[:n]); err != nil {
			return
		}
		data = data[n:]
	}
	_ = s.Send(MsgOK, []byte(`{}`))
}

func (a *Agent) handleFileWrite(s *Stream, frame Frame) {
	var body struct {
		Path string `json:"path"`
		Mode uint32 `json:"mode"`
	}
	if err := json.Unmarshal(frame.Data, &body); err != nil {
		a.fail(s, err)
		return
	}
	p, err := cleanAbs(body.Path)
	if err != nil {
		a.fail(s, err)
		return
	}
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		// Closing the reader unblocks pw.Write when the destination cannot
		// be created and writeAtomic returns before it reads.
		defer pr.Close()
		errc <- writeAtomic(p, pr, body.Mode)
	}()
	for {
		f, err := s.Recv(s.ctx)
		if err != nil {
			pw.CloseWithError(err)
			<-errc
			return
		}
		switch f.Type {
		case MsgData:
			if _, werr := pw.Write(f.Data); werr != nil {
				_ = pw.Close()
				cause := <-errc
				if cause == nil {
					cause = werr
				}
				a.fail(s, cause)
				return
			}
		case MsgStdinEOF:
			_ = pw.Close()
			if err := <-errc; err != nil {
				a.fail(s, err)
				return
			}
			_ = s.Send(MsgOK, []byte(`{}`))
			return
		default:
			pw.CloseWithError(errors.New("aborted"))
			<-errc
			a.fail(s, errors.New("aborted"))
			return
		}
	}
}

func (a *Agent) handleFileRemove(s *Stream, frame Frame) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(frame.Data, &body); err != nil {
		a.fail(s, err)
		return
	}
	p, err := cleanAbs(body.Path)
	if err != nil {
		a.fail(s, err)
		return
	}
	if err := refuseRemove(p); err != nil {
		a.fail(s, err)
		return
	}
	if err := os.RemoveAll(p); err != nil {
		a.fail(s, err)
		return
	}
	_ = s.Send(MsgOK, []byte(`{}`))
}

func (a *Agent) handleFileMkdir(s *Stream, frame Frame) {
	var body struct {
		Path string `json:"path"`
		Mode uint32 `json:"mode"`
	}
	if err := json.Unmarshal(frame.Data, &body); err != nil {
		a.fail(s, err)
		return
	}
	p, err := cleanAbs(body.Path)
	if err != nil {
		a.fail(s, err)
		return
	}
	mode := os.FileMode(body.Mode)
	if mode == 0 {
		mode = 0o755
	}
	if err := os.MkdirAll(p, mode); err != nil {
		a.fail(s, err)
		return
	}
	_ = s.Send(MsgOK, []byte(`{}`))
}

func (a *Agent) handleWatch(s *Stream, frame Frame) {
	var body struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
	}
	if err := json.Unmarshal(frame.Data, &body); err != nil {
		a.fail(s, err)
		return
	}
	root, err := cleanAbs(body.Path)
	if err != nil {
		a.fail(s, err)
		return
	}
	prev, err := snapshot(root, body.Recursive)
	if err != nil {
		a.fail(s, err)
		return
	}
	_ = s.Send(MsgOK, []byte(`{}`))
	every := a.watchEvery
	if every <= 0 {
		every = 200 * time.Millisecond
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			cur, err := snapshot(root, body.Recursive)
			if err != nil {
				a.fail(s, err)
				return
			}
			for _, ev := range diffSnapshot(prev, cur) {
				if err := s.Send(MsgFileEvent, mustJSON(ev)); err != nil {
					return
				}
			}
			prev = cur
		}
	}
}

func (a *Agent) handleDial(s *Stream, frame Frame) {
	var body struct {
		Port int `json:"port"`
	}
	if err := json.Unmarshal(frame.Data, &body); err != nil {
		a.fail(s, err)
		return
	}
	if body.Port < 1 || body.Port > 65535 {
		a.fail(s, errors.New("invalid port"))
		return
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(s.ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", body.Port))
	if err != nil {
		a.fail(s, err)
		return
	}
	defer conn.Close()
	if err := s.Send(MsgOK, []byte(`{}`)); err != nil {
		return
	}
	go func() {
		buf := make([]byte, ChunkSize)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				if err := s.Send(MsgData, append([]byte(nil), buf[:n]...)); err != nil {
					return
				}
			}
			if err != nil {
				_ = s.Send(MsgEnd, nil)
				return
			}
		}
	}()
	for {
		f, err := s.Recv(s.ctx)
		if err != nil {
			return
		}
		switch f.Type {
		case MsgData:
			if _, err := conn.Write(f.Data); err != nil {
				return
			}
		case MsgStdinEOF, MsgKill:
			return
		}
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{}`)
	}
	return b
}
