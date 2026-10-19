package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// Client is a multiplexed session to an agent.
type Client struct {
	mux    *Mux
	closer io.Closer
}

// Handshake authenticates and starts the read loop.
func Handshake(ctx context.Context, conn io.ReadWriteCloser, token string) (*Client, error) {
	if d, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		if deadline, ok := ctx.Deadline(); ok {
			_ = d.SetDeadline(deadline)
		}
		defer d.SetDeadline(time.Time{})
	}
	if err := WriteFrame(conn, 0, MsgHello, []byte(token)); err != nil {
		return nil, err
	}
	frame, err := ReadFrame(conn)
	if err != nil {
		return nil, err
	}
	if frame.Type == MsgError {
		return nil, ErrUnauthorized
	}
	if frame.Type != MsgHelloOk {
		return nil, fmt.Errorf("unexpected handshake frame %d", frame.Type)
	}
	mux := NewMux(conn, true)
	go mux.ReadLoop(conn)
	return &Client{mux: mux, closer: conn}, nil
}

// Alive reports whether the read loop is still running.
func (c *Client) Alive() bool {
	select {
	case <-c.mux.Done():
		return false
	default:
		return true
	}
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	if c.closer == nil {
		return nil
	}
	return c.closer.Close()
}

// Exec runs a foreground command. emit may be nil. stdin may be nil.
func (c *Client) Exec(ctx context.Context, spec ExecSpec, stdin io.Reader, emit func(kind uint8, data []byte) error) (ExecOutput, error) {
	spec.Background = false
	s, err := c.mux.Open()
	if err != nil {
		return ExecOutput{}, err
	}
	defer s.Close()
	if err := s.Send(MsgExec, mustJSON(spec)); err != nil {
		return ExecOutput{}, err
	}
	go func() {
		if stdin == nil {
			_ = s.Send(MsgStdinEOF, nil)
			return
		}
		buf := make([]byte, ChunkSize)
		for {
			n, rerr := stdin.Read(buf)
			if n > 0 {
				if err := s.Send(MsgStdin, append([]byte(nil), buf[:n]...)); err != nil {
					return
				}
			}
			if rerr != nil {
				_ = s.Send(MsgStdinEOF, nil)
				return
			}
		}
	}()
	var out ExecOutput
	for {
		frame, err := s.Recv(ctx)
		if err != nil {
			_ = s.Send(MsgKill, nil)
			return out, err
		}
		switch frame.Type {
		case MsgStdout, MsgStderr:
			if emit != nil {
				if err := emit(frame.Type, frame.Data); err != nil {
					_ = s.Send(MsgKill, nil)
					return out, err
				}
			}
		case MsgExit:
			var res ExitResult
			if err := json.Unmarshal(frame.Data, &res); err != nil {
				return out, err
			}
			out.ExitCode = res.ExitCode
			out.Truncated = res.Truncated
			out.Duration = res.DurationMs
			return out, nil
		case MsgError:
			return out, errors.New(string(frame.Data))
		}
	}
}

// Start launches a background process.
func (c *Client) Start(ctx context.Context, spec ExecSpec) (ProcessInfo, error) {
	spec.Background = true
	var info ProcessInfo
	err := c.rpc(ctx, MsgExec, spec, &info)
	return info, err
}

// List returns tracked processes.
func (c *Client) List(ctx context.Context) ([]ProcessInfo, error) {
	var body struct {
		Processes []ProcessInfo `json:"processes"`
	}
	if err := c.rpc(ctx, MsgProcList, map[string]any{}, &body); err != nil {
		return nil, err
	}
	if body.Processes == nil {
		body.Processes = []ProcessInfo{}
	}
	return body.Processes, nil
}

// Signal sends a signal to a tracked process.
func (c *Client) Signal(ctx context.Context, id, signal string) error {
	return c.rpc(ctx, MsgProcSignal, map[string]string{"id": id, "signal": signal}, nil)
}

// Resize changes the terminal size of a PTY process.
func (c *Client) Resize(ctx context.Context, id string, cols, rows int) error {
	return c.rpc(ctx, MsgResize, map[string]any{"id": id, "cols": cols, "rows": rows}, nil)
}

// WriteStdin writes bytes to a background process and does not close stdin.
func (c *Client) WriteStdin(ctx context.Context, id string, data []byte) error {
	s, err := c.mux.Open()
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Send(MsgProcStdin, mustJSON(map[string]string{"id": id})); err != nil {
		return err
	}
	for len(data) > 0 {
		n := len(data)
		if n > ChunkSize {
			n = ChunkSize
		}
		if err := s.Send(MsgData, data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	if err := s.Send(MsgStdinEOF, nil); err != nil {
		return err
	}
	// MsgStdinEOF on a non-tty process closes stdin. Background cat would then
	// exit. Callers that need a still-open stdin use WriteStdinKeep.
	return c.readOK(ctx, s)
}

// WriteStdinKeep writes bytes and leaves stdin open.
func (c *Client) WriteStdinKeep(ctx context.Context, id string, data []byte) error {
	s, err := c.mux.Open()
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Send(MsgProcStdin, mustJSON(map[string]string{"id": id})); err != nil {
		return err
	}
	for len(data) > 0 {
		n := len(data)
		if n > ChunkSize {
			n = ChunkSize
		}
		if err := s.Send(MsgData, data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	// A zero-length data frame is not an EOF. Ask for OK by sending a dedicated
	// end that the server treats as "bytes are done but stdin stays open".
	if err := s.Send(MsgEnd, nil); err != nil {
		return err
	}
	return c.readOK(ctx, s)
}

// Attach replays and follows a process until it exits or ctx ends.
func (c *Client) Attach(ctx context.Context, id string, emit func(kind uint8, data []byte) error) (ExecOutput, error) {
	s, err := c.mux.Open()
	if err != nil {
		return ExecOutput{}, err
	}
	defer s.Close()
	if err := s.Send(MsgProcAttach, mustJSON(map[string]string{"id": id})); err != nil {
		return ExecOutput{}, err
	}
	var out ExecOutput
	for {
		frame, err := s.Recv(ctx)
		if err != nil {
			return out, err
		}
		switch frame.Type {
		case MsgStdout, MsgStderr:
			if emit != nil {
				if err := emit(frame.Type, frame.Data); err != nil {
					return out, err
				}
			}
		case MsgExit:
			var res ExitResult
			if err := json.Unmarshal(frame.Data, &res); err != nil {
				return out, err
			}
			out.ExitCode = res.ExitCode
			out.Truncated = res.Truncated
			out.Duration = res.DurationMs
			return out, nil
		case MsgError:
			return out, errors.New(string(frame.Data))
		}
	}
}

// ListFiles lists one directory.
func (c *Client) ListFiles(ctx context.Context, path string) ([]FileInfo, error) {
	var body struct {
		Entries []FileInfo `json:"entries"`
	}
	if err := c.rpc(ctx, MsgFileList, map[string]string{"path": path}, &body); err != nil {
		return nil, err
	}
	if body.Entries == nil {
		body.Entries = []FileInfo{}
	}
	return body.Entries, nil
}

// Stat returns one path.
func (c *Client) Stat(ctx context.Context, path string) (FileInfo, error) {
	var info FileInfo
	err := c.rpc(ctx, MsgFileStat, map[string]string{"path": path}, &info)
	return info, err
}

// ReadFile returns the file bytes, refusing files larger than max when max > 0.
func (c *Client) ReadFile(ctx context.Context, path string, max int) ([]byte, error) {
	s, err := c.mux.Open()
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if err := s.Send(MsgFileRead, mustJSON(map[string]any{"path": path, "max": max})); err != nil {
		return nil, err
	}
	var buf []byte
	for {
		frame, err := s.Recv(ctx)
		if err != nil {
			return nil, err
		}
		switch frame.Type {
		case MsgData:
			buf = append(buf, frame.Data...)
		case MsgOK:
			return buf, nil
		case MsgError:
			return nil, errors.New(string(frame.Data))
		}
	}
}

// WriteFile writes content with the given permission bits.
func (c *Client) WriteFile(ctx context.Context, path string, content []byte, mode uint32) error {
	s, err := c.mux.Open()
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Send(MsgFileWrite, mustJSON(map[string]any{"path": path, "mode": mode})); err != nil {
		return err
	}
	for len(content) > 0 {
		n := len(content)
		if n > ChunkSize {
			n = ChunkSize
		}
		if err := s.Send(MsgData, content[:n]); err != nil {
			return err
		}
		content = content[n:]
	}
	if err := s.Send(MsgStdinEOF, nil); err != nil {
		return err
	}
	return c.readOK(ctx, s)
}

// ExtractArchive unpacks a tar stream into path. max is the uncompressed
// byte budget; zero means no limit.
func (c *Client) ExtractArchive(ctx context.Context, path string, r io.Reader, max int64) error {
	s, err := c.mux.Open()
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Send(MsgArchivePut, mustJSON(map[string]any{"path": path, "max": max})); err != nil {
		return err
	}
	buf := make([]byte, ChunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if sendErr := s.Send(MsgData, append([]byte(nil), buf[:n]...)); sendErr != nil {
				return sendErr
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	if err := s.Send(MsgStdinEOF, nil); err != nil {
		return err
	}
	return c.readOK(ctx, s)
}

// PackArchive returns a gzip-compressed tar of the directory. max limits the
// compressed size; zero means no limit.
func (c *Client) PackArchive(ctx context.Context, path string, max int) ([]byte, error) {
	s, err := c.mux.Open()
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if err := s.Send(MsgArchiveGet, mustJSON(map[string]any{"path": path, "max": max})); err != nil {
		return nil, err
	}
	var buf []byte
	for {
		frame, err := s.Recv(ctx)
		if err != nil {
			return nil, err
		}
		switch frame.Type {
		case MsgData:
			buf = append(buf, frame.Data...)
			if max > 0 && len(buf) > max {
				return nil, errors.New("archive exceeds the size limit")
			}
		case MsgOK:
			return buf, nil
		case MsgError:
			return nil, errors.New(string(frame.Data))
		}
	}
}

// RemoveFile deletes a file or directory.
func (c *Client) RemoveFile(ctx context.Context, path string) error {
	return c.rpc(ctx, MsgFileRemove, map[string]string{"path": path}, nil)
}

// Mkdir creates a directory and any missing parents.
func (c *Client) Mkdir(ctx context.Context, path string, mode uint32) error {
	return c.rpc(ctx, MsgFileMkdir, map[string]any{"path": path, "mode": mode}, nil)
}

// Watch emits filesystem events until ctx is cancelled or the peer fails.
func (c *Client) Watch(ctx context.Context, path string, recursive bool, emit func(FileEvent) error) error {
	s, err := c.mux.Open()
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Send(MsgFileWatch, mustJSON(map[string]any{"path": path, "recursive": recursive})); err != nil {
		return err
	}
	frame, err := s.Recv(ctx)
	if err != nil {
		return err
	}
	if frame.Type == MsgError {
		return errors.New(string(frame.Data))
	}
	if frame.Type != MsgOK {
		return fmt.Errorf("unexpected frame %d", frame.Type)
	}
	for {
		frame, err := s.Recv(ctx)
		if err != nil {
			return err
		}
		switch frame.Type {
		case MsgFileEvent:
			var ev FileEvent
			if err := json.Unmarshal(frame.Data, &ev); err != nil {
				return err
			}
			if emit != nil {
				if err := emit(ev); err != nil {
					return err
				}
			}
		case MsgError:
			return errors.New(string(frame.Data))
		}
	}
}

// Dial connects to a TCP port on the sandbox loopback and returns a stream.
func (c *Client) Dial(ctx context.Context, port int) (net.Conn, error) {
	s, err := c.mux.Open()
	if err != nil {
		return nil, err
	}
	if err := s.Send(MsgDial, mustJSON(map[string]int{"port": port})); err != nil {
		s.Close()
		return nil, err
	}
	frame, err := s.Recv(ctx)
	if err != nil {
		s.Close()
		return nil, err
	}
	if frame.Type == MsgError {
		s.Close()
		return nil, errors.New(string(frame.Data))
	}
	if frame.Type != MsgOK {
		s.Close()
		return nil, fmt.Errorf("unexpected frame %d", frame.Type)
	}
	return &streamConn{c: c, s: s}, nil
}

func (c *Client) rpc(ctx context.Context, typ uint8, payload any, dst any) error {
	s, err := c.mux.Open()
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Send(typ, mustJSON(payload)); err != nil {
		return err
	}
	return c.readOK(ctx, s, dst)
}

func (c *Client) readOK(ctx context.Context, s *Stream, dst ...any) error {
	for {
		frame, err := s.Recv(ctx)
		if err != nil {
			return err
		}
		switch frame.Type {
		case MsgOK, MsgExit:
			if len(dst) > 0 && dst[0] != nil && len(frame.Data) > 0 {
				return json.Unmarshal(frame.Data, dst[0])
			}
			return nil
		case MsgError:
			return errors.New(string(frame.Data))
		case MsgStdout, MsgStderr, MsgData, MsgFileEvent:
			continue
		default:
			return fmt.Errorf("unexpected frame %d", frame.Type)
		}
	}
}

// streamConn is a net.Conn over a dialed agent stream.
type streamConn struct {
	c    *Client
	s    *Stream
	buf  []byte
	once bool
}

func (s *streamConn) Read(p []byte) (int, error) {
	if len(s.buf) == 0 {
		for {
			frame, err := s.s.Recv(context.Background())
			if err != nil {
				return 0, io.EOF
			}
			if frame.Type == MsgData && len(frame.Data) > 0 {
				s.buf = frame.Data
				break
			}
			if frame.Type == MsgError || frame.Type == MsgEnd || frame.Type == MsgKill {
				return 0, io.EOF
			}
		}
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

func (s *streamConn) Write(p []byte) (int, error) {
	if err := s.s.Send(MsgData, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *streamConn) Close() error {
	if s.once {
		return nil
	}
	s.once = true
	s.s.Close()
	return nil
}

func (s *streamConn) LocalAddr() net.Addr              { return streamAddr("local") }
func (s *streamConn) RemoteAddr() net.Addr             { return streamAddr("sandbox") }
func (s *streamConn) SetDeadline(time.Time) error      { return nil }
func (s *streamConn) SetReadDeadline(time.Time) error  { return nil }
func (s *streamConn) SetWriteDeadline(time.Time) error { return nil }

type streamAddr string

func (a streamAddr) Network() string { return "sbx" }
func (a streamAddr) String() string  { return string(a) }
