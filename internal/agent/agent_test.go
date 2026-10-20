package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	pr, pw := io.Pipe()
	done := make(chan Frame, 1)
	go func() {
		f, err := ReadFrame(pr)
		if err != nil {
			t.Errorf("read: %v", err)
		}
		done <- f
	}()
	payload := bytes.Repeat([]byte("abc"), 1000)
	if err := WriteFrame(pw, 7, MsgStdout, payload); err != nil {
		t.Fatal(err)
	}
	f := <-done
	if f.Stream != 7 || f.Type != MsgStdout || !bytes.Equal(f.Data, payload) {
		t.Fatalf("frame %+v", f)
	}
	if err := WriteFrame(io.Discard, 1, MsgData, bytes.Repeat([]byte{1}, MaxPayload+1)); err == nil {
		t.Fatal("expected oversized frame to fail")
	}
}

func TestHandshakeRejectsBadToken(t *testing.T) {
	client, server := net.Pipe()
	go New("secret").ServeConn(server)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := Handshake(ctx, client, "nope"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err %v", err)
	}
}

func TestExecStdinExitAndFiles(t *testing.T) {
	c, cleanup := startAgent(t, "tok")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	out, err := c.Exec(ctx, ExecSpec{Cmd: []string{"sh", "-c", "printf out; printf err >&2"}}, nil, func(kind uint8, data []byte) error {
		if kind == MsgStderr {
			stderr.Write(data)
		} else {
			stdout.Write(data)
		}
		return nil
	})
	if err != nil || out.ExitCode != 0 || stdout.String() != "out" || stderr.String() != "err" {
		t.Fatalf("out %q err %q res %+v exec %v", stdout.String(), stderr.String(), out, err)
	}

	out, err = c.Exec(ctx, ExecSpec{Cmd: []string{"sh", "-c", "cat"}}, bytes.NewReader([]byte("hello")), func(kind uint8, data []byte) error {
		if kind == MsgStdout {
			stdout.Reset()
			stdout.Write(data)
		}
		return nil
	})
	if err != nil || out.ExitCode != 0 || stdout.String() != "hello" {
		t.Fatalf("stdin %q %+v %v", stdout.String(), out, err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "a.txt")
	if err := c.WriteFile(ctx, path, []byte("abc"), 0o640); err != nil {
		t.Fatal(err)
	}
	got, err := c.ReadFile(ctx, path, 100)
	if err != nil || string(got) != "abc" {
		t.Fatalf("read %q %v", got, err)
	}
	info, err := c.Stat(ctx, path)
	if err != nil || info.Type != "file" || info.Size != 3 || info.Name != "a.txt" {
		t.Fatalf("stat %+v %v", info, err)
	}
	entries, err := c.ListFiles(ctx, filepath.Join(dir, "sub"))
	if err != nil || len(entries) != 1 || entries[0].Name != "a.txt" {
		t.Fatalf("list %+v %v", entries, err)
	}
	if err := c.Mkdir(ctx, filepath.Join(dir, "nested", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nested", "x")); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveFile(ctx, path); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReadFile(ctx, path, 10); err == nil {
		t.Fatal("expected missing file")
	}
	if err := c.RemoveFile(ctx, "/"); err == nil {
		t.Fatal("expected refusal to delete /")
	}
}

func TestBackgroundSignalAttachAndReconnect(t *testing.T) {
	a := New("tok")
	c1, done1 := pair(t, a)
	c2, done2 := pair(t, a)
	defer done1()
	defer done2()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	info, err := c1.Start(ctx, ExecSpec{Cmd: []string{"sleep", "30"}})
	if err != nil || info.PID == 0 || info.Status != "running" {
		t.Fatalf("%+v %v", info, err)
	}
	list, err := c2.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != info.ID {
		t.Fatalf("list %+v %v", list, err)
	}
	if err := c2.Signal(ctx, info.ID, "KILL"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		list, err = c2.List(ctx)
		if err == nil && len(list) == 1 && list[0].Status == "exited" && list[0].ExitCode == 137 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after kill %+v %v", list, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var buf bytes.Buffer
	out, err := c1.Attach(ctx, info.ID, func(kind uint8, data []byte) error {
		buf.Write(data)
		return nil
	})
	if err != nil || out.ExitCode != 137 {
		t.Fatalf("attach %+v %v %q", out, err, buf.String())
	}
}

func TestBackgroundStdinReplay(t *testing.T) {
	c, cleanup := startAgent(t, "tok")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := c.Start(ctx, ExecSpec{Cmd: []string{"cat"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WriteStdinKeep(ctx, info.ID, []byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	seen := make(chan struct{}, 1)
	go func() {
		_, _ = c.Attach(ctx, info.ID, func(_ uint8, data []byte) error {
			if bytes.Contains(data, []byte("ping")) {
				select {
				case seen <- struct{}{}:
				default:
				}
			}
			return nil
		})
	}()
	select {
	case <-seen:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stdin replay")
	}
	if err := c.Signal(ctx, info.ID, "KILL"); err != nil {
		t.Fatal(err)
	}
}

func TestExecTimeout(t *testing.T) {
	c, cleanup := startAgent(t, "tok")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := c.Exec(ctx, ExecSpec{Cmd: []string{"sleep", "30"}, TimeoutSeconds: 1}, nil, nil)
	if err != nil || out.ExitCode != 137 {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestWatchAndDial(t *testing.T) {
	dir := t.TempDir()
	a := New("tok")
	a.watchEvery = 20 * time.Millisecond
	c, cleanup := pairAgent(t, a)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events := make(chan FileEvent, 4)
	watchCtx, stopWatch := context.WithCancel(ctx)
	go func() {
		_ = c.Watch(watchCtx, dir, false, func(ev FileEvent) error {
			events <- ev
			return nil
		})
	}()
	time.Sleep(40 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "n.txt"), []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		if ev.Type != "create" || filepath.Base(ev.Path) != "n.txt" {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch event")
	}
	stopWatch()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 16)
		n, _ := conn.Read(buf)
		_, _ = conn.Write(append([]byte("pong:"), buf[:n]...))
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	conn, err := c.Dial(ctx, port)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != "pong:hi" {
		t.Fatalf("dial %q %v", buf[:n], err)
	}
}

func TestPTY(t *testing.T) {
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		t.Skip("no ptmx")
	}
	c, cleanup := startAgent(t, "tok")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := c.Start(ctx, ExecSpec{Cmd: []string{"sh", "-c", "printf pty-ok; sleep 30"}, TTY: true, Cols: 80, Rows: 24})
	if err != nil {
		if os.IsPermission(err) || strings.Contains(err.Error(), "operation not permitted") {
			t.Skip(err.Error())
		}
		t.Fatal(err)
	}
	if err := c.Resize(ctx, info.ID, 100, 40); err != nil {
		t.Fatal(err)
	}
	seen := make(chan struct{}, 1)
	go func() {
		_, _ = c.Attach(ctx, info.ID, func(_ uint8, data []byte) error {
			if bytes.Contains(data, []byte("pty-ok")) {
				select {
				case seen <- struct{}{}:
				default:
				}
			}
			return nil
		})
	}()
	select {
	case <-seen:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pty output")
	}
	if err := c.Signal(ctx, info.ID, "KILL"); err != nil {
		t.Fatal(err)
	}
}

func TestExecWithoutProcessPATH(t *testing.T) {
	t.Setenv("PATH", "")
	c, cleanup := startAgent(t, "tok")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	out, err := c.Exec(ctx, ExecSpec{Cmd: []string{"echo", "hi"}}, nil, func(kind uint8, data []byte) error {
		if kind == MsgStdout {
			stdout.Write(data)
		}
		return nil
	})
	if err != nil || out.ExitCode != 0 || stdout.String() != "hi\n" {
		t.Fatalf("%q %+v %v", stdout.String(), out, err)
	}
}

func TestChildEnvStripsAgentToken(t *testing.T) {
	t.Setenv(EnvToken, "sekret")
	c, cleanup := startAgent(t, "tok")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	out, err := c.Exec(ctx, ExecSpec{Cmd: []string{"sh", "-c", `printf %s "$SBX_AGENT_TOKEN"`}}, nil, func(kind uint8, data []byte) error {
		if kind == MsgStdout {
			stdout.Write(data)
		}
		return nil
	})
	if err != nil || out.ExitCode != 0 || stdout.Len() != 0 {
		t.Fatalf("token leaked %q %+v %v", stdout.String(), out, err)
	}
}

func TestDialSplice(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "agent.sock")
	t.Setenv(EnvSocket, sock)
	t.Setenv(EnvToken, "splice")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- Init(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket did not appear")
		}
		time.Sleep(10 * time.Millisecond)
	}
	leftR, leftW := io.Pipe()
	rightR, rightW := io.Pipe()
	go func() { _ = Dial(leftR, rightW) }()
	clientCtx, clientCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer clientCancel()
	// Dial copies rightW from the socket and leftR into the socket.
	// Handshake reads and writes the opposite ends.
	conn := &pipeConn{r: rightR, w: leftW}
	c, err := Handshake(clientCtx, conn, "splice")
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Exec(clientCtx, ExecSpec{Cmd: []string{"echo", "spliced"}}, nil, nil)
	if err != nil || out.ExitCode != 0 {
		t.Fatalf("%+v %v", out, err)
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("init did not stop")
	}
}

func TestOutputCap(t *testing.T) {
	c, cleanup := startAgent(t, "tok")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var n int
	out, err := c.Exec(ctx, ExecSpec{Cmd: []string{"sh", "-c", "printf '0123456789'"}, MaxOutput: 4}, nil, func(_ uint8, data []byte) error {
		n += len(data)
		return nil
	})
	if err != nil || !out.Truncated || n != 4 {
		t.Fatalf("n %d %+v %v", n, out, err)
	}
}

type pipeConn struct {
	r io.ReadCloser
	w io.WriteCloser
}

func (p *pipeConn) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeConn) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipeConn) Close() error {
	_ = p.r.Close()
	return p.w.Close()
}

func startAgent(t *testing.T, token string) (*Client, func()) {
	t.Helper()
	return pairAgent(t, New(token))
}

func pairAgent(t *testing.T, a *Agent) (*Client, func()) {
	t.Helper()
	c, done := pair(t, a)
	return c, done
}

func pair(t *testing.T, a *Agent) (*Client, func()) {
	t.Helper()
	client, server := net.Pipe()
	go a.ServeConn(server)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	c, err := Handshake(ctx, client, string(a.token))
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	return c, func() { c.Close() }
}
