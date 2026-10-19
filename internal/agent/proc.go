package agent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const replayLimit = 256 << 10

type proc struct {
	id        string
	pid       int
	cmd       []string
	cwd       string
	tty       bool
	started   time.Time
	stdin     io.WriteCloser
	master    *os.File
	maxOut    int
	sent      int
	truncated bool

	mu         sync.Mutex
	replay     []outChunk
	replaySize int
	subs       []*subscriber
	exited     bool
	exitCode   int
	finished   chan struct{}
}

type outChunk struct {
	kind uint8
	data []byte
}

type subscriber struct {
	ch   chan outChunk
	done chan struct{}
}

func defaultWorkdir() string {
	const sandbox = "/workspace"
	if st, err := os.Stat(sandbox); err == nil && st.IsDir() {
		return sandbox
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "/"
}

func (a *Agent) spawn(spec ExecSpec) (*proc, error) {
	if len(spec.Cmd) == 0 || spec.Cmd[0] == "" {
		return nil, errors.New("cmd is required")
	}
	cwd := spec.Cwd
	if cwd == "" {
		cwd = defaultWorkdir()
	}
	if !strings.HasPrefix(cwd, "/") {
		return nil, errors.New("cwd must be absolute")
	}
	if err := a.makeRoom(); err != nil {
		return nil, err
	}

	prefix := "proc_"
	if spec.TTY {
		prefix = "term_"
	}
	p := &proc{
		id:       newID(prefix),
		cmd:      append([]string(nil), spec.Cmd...),
		cwd:      cwd,
		tty:      spec.TTY,
		started:  time.Now(),
		maxOut:   spec.MaxOutput,
		finished: make(chan struct{}),
	}
	command := exec.Command(spec.Cmd[0], spec.Cmd[1:]...)
	command.Dir = cwd
	command.Env = childEnv(spec.Env, spec.TTY)
	prepareCmd(command)

	var stdout, stderr io.ReadCloser
	if spec.TTY {
		master, err := startPTY(command, spec.Cols, spec.Rows)
		if err != nil {
			return nil, err
		}
		p.master = master
		p.stdin = master
		stdout = master
	} else {
		in, err := command.StdinPipe()
		if err != nil {
			return nil, err
		}
		out, err := command.StdoutPipe()
		if err != nil {
			return nil, err
		}
		errPipe, err := command.StderrPipe()
		if err != nil {
			return nil, err
		}
		if err := command.Start(); err != nil {
			return nil, err
		}
		p.stdin = in
		stdout = out
		stderr = errPipe
	}
	p.pid = command.Process.Pid
	if spec.TimeoutSeconds > 0 {
		time.AfterFunc(time.Duration(spec.TimeoutSeconds)*time.Second, func() {
			_ = p.signal("KILL")
		})
	}
	a.mu.Lock()
	a.procs[p.id] = p
	a.mu.Unlock()

	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		copyOutput(p, MsgStdout, stdout)
	}()
	if stderr != nil {
		readers.Add(1)
		go func() {
			defer readers.Done()
			copyOutput(p, MsgStderr, stderr)
		}()
	}
	go func() {
		readers.Wait()
		err := command.Wait()
		code := exitCode(err)
		p.finish(code)
	}()
	return p, nil
}

func copyOutput(p *proc, kind uint8, r io.Reader) {
	buf := make([]byte, ChunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			p.emit(kind, buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (p *proc) emit(kind uint8, data []byte) {
	p.mu.Lock()
	if p.maxOut > 0 && p.sent >= p.maxOut {
		p.truncated = true
		p.mu.Unlock()
		return
	}
	if p.maxOut > 0 && p.sent+len(data) > p.maxOut {
		data = data[:p.maxOut-p.sent]
		p.truncated = true
	}
	p.sent += len(data)
	cp := append([]byte(nil), data...)
	p.appendReplay(kind, cp)
	subs := append([]*subscriber(nil), p.subs...)
	p.mu.Unlock()
	for _, sub := range subs {
		sendChunk(sub, outChunk{kind: kind, data: cp})
	}
}

func (p *proc) appendReplay(kind uint8, data []byte) {
	p.replay = append(p.replay, outChunk{kind: kind, data: data})
	p.replaySize += len(data)
	for p.replaySize > replayLimit && len(p.replay) > 1 {
		p.replaySize -= len(p.replay[0].data)
		p.replay = p.replay[1:]
	}
}

func sendChunk(sub *subscriber, c outChunk) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case sub.ch <- c:
	case <-sub.done:
	case <-timer.C:
	}
}

func (p *proc) finish(code int) {
	p.mu.Lock()
	if p.exited {
		p.mu.Unlock()
		return
	}
	p.exited = true
	p.exitCode = code
	subs := p.subs
	p.subs = nil
	p.mu.Unlock()
	close(p.finished)
	for _, sub := range subs {
		close(sub.done)
	}
}

func (p *proc) subscribe() (*subscriber, []outChunk, bool, int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	replay := make([]outChunk, len(p.replay))
	copy(replay, p.replay)
	if p.exited {
		return nil, replay, true, p.exitCode, p.truncated
	}
	sub := &subscriber{ch: make(chan outChunk, 32), done: make(chan struct{})}
	p.subs = append(p.subs, sub)
	return sub, replay, false, 0, p.truncated
}

func (p *proc) unsubscribe(sub *subscriber) {
	if sub == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.subs[:0]
	for _, s := range p.subs {
		if s != sub {
			out = append(out, s)
		}
	}
	p.subs = out
}

func (p *proc) writeStdin(b []byte) error {
	p.mu.Lock()
	exited := p.exited
	w := p.stdin
	p.mu.Unlock()
	if exited {
		return errors.New("process has exited")
	}
	if w == nil {
		return errors.New("process has no stdin")
	}
	_, err := w.Write(b)
	return err
}

func (p *proc) closeStdin() error {
	if p.tty {
		return nil
	}
	p.mu.Lock()
	w := p.stdin
	p.stdin = nil
	p.mu.Unlock()
	if w == nil {
		return nil
	}
	return w.Close()
}

func (p *proc) signal(name string) error {
	sig, err := parseSignal(name)
	if err != nil {
		return err
	}
	p.mu.Lock()
	exited := p.exited
	pid := p.pid
	p.mu.Unlock()
	if exited {
		return errors.New("process has exited")
	}
	return killGroup(pid, sig)
}

func (p *proc) resize(cols, rows int) error {
	if !p.tty || p.master == nil {
		return errors.New("process has no terminal")
	}
	return resizePTY(p.master, cols, rows)
}

func (p *proc) info() ProcessInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	status := "running"
	if p.exited {
		status = "exited"
	}
	return ProcessInfo{
		ID: p.id, PID: p.pid, Cmd: append([]string(nil), p.cmd...), Cwd: p.cwd,
		TTY: p.tty, Status: status, ExitCode: p.exitCode, StartedAt: p.started.UnixMilli(),
	}
}

func (p *proc) result() ExitResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	return ExitResult{ExitCode: p.exitCode, Truncated: p.truncated, DurationMs: time.Since(p.started).Milliseconds()}
}

func (a *Agent) makeRoom() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.procs) < a.maxProcs {
		return nil
	}
	for id, p := range a.procs {
		p.mu.Lock()
		exited := p.exited
		p.mu.Unlock()
		if exited {
			delete(a.procs, id)
		}
	}
	if len(a.procs) >= a.maxProcs {
		return errors.New("too many processes")
	}
	return nil
}

func (a *Agent) get(id string) (*proc, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.procs[id]
	return p, ok
}

func (a *Agent) list() []ProcessInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ProcessInfo, 0, len(a.procs))
	for _, p := range a.procs {
		out = append(out, p.info())
	}
	return out
}

func childEnv(extra map[string]string, tty bool) []string {
	var out []string
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if !ok || strings.HasPrefix(key, "SBX_AGENT_") {
			continue
		}
		out = append(out, item)
	}
	if !envHas(out, "PATH") {
		out = append(out, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	if !envHas(out, "HOME") {
		out = append(out, "HOME=/workspace")
	}
	if tty && !envHas(out, "TERM") {
		out = append(out, "TERM=xterm-256color")
	}
	for k, v := range extra {
		if strings.HasPrefix(k, "SBX_AGENT_") || k == "" {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

func envHas(env []string, key string) bool {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}

func parseSignal(name string) (int, error) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "", "TERM", "SIGTERM":
		return 15, nil
	case "KILL", "SIGKILL":
		return 9, nil
	case "INT", "SIGINT":
		return 2, nil
	case "HUP", "SIGHUP":
		return 1, nil
	case "QUIT", "SIGQUIT":
		return 3, nil
	default:
		n := 0
		for _, c := range name {
			if c < '0' || c > '9' {
				return 0, fmt.Errorf("unknown signal %q", name)
			}
			n = n*10 + int(c-'0')
		}
		if n < 1 || n > 64 {
			return 0, fmt.Errorf("unknown signal %q", name)
		}
		return n, nil
	}
}

func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "0000000000000000"
	}
	return prefix + hex.EncodeToString(b[:])
}
