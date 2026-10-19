package agent

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunUsageAndRejects(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	if err := Run([]string{"--help"}); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"help"}); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"-h"}); err != nil {
		t.Fatal(err)
	}
	w.Close()
	body, err := io.ReadAll(r)
	if err != nil || !strings.Contains(string(body), "sbx-agent init") {
		t.Fatalf("usage %q %v", body, err)
	}
	if err := Run([]string{"nope"}); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown %v", err)
	}
	t.Setenv(EnvToken, "")
	if err := Run(nil); err == nil || !strings.Contains(err.Error(), "SBX_AGENT_TOKEN") {
		t.Fatalf("init %v", err)
	}
	if err := Run([]string{"serve", "-bogus"}); err == nil {
		t.Fatal("expected flag error")
	}
	if err := Run([]string{"serve", "-listen", "127.0.0.1:0"}); err == nil {
		t.Fatal("expected missing token")
	}
	t.Setenv(EnvToken, "tok")
	if err := Run([]string{"serve", "-listen", "not a host"}); err == nil {
		t.Fatal("expected listen error")
	}
}

func TestNextDelay(t *testing.T) {
	if nextDelay(0) != 5*time.Millisecond {
		t.Fatal(nextDelay(0))
	}
	if nextDelay(600*time.Millisecond) != time.Second {
		t.Fatal(nextDelay(600 * time.Millisecond))
	}
	if nextDelay(time.Second) != time.Second {
		t.Fatal("cap")
	}
}

func TestDialUnixAndCopyError(t *testing.T) {
	_, err := dialUnix(filepath.Join(t.TempDir(), "missing.sock"), 40*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "agent socket") {
		t.Fatal(err)
	}

	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, conn)
			conn.Close()
		}
	}()
	t.Setenv(EnvSocket, sock)
	err = Dial(errReader{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatal(err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestInitRejectsAndDefaultSocket(t *testing.T) {
	t.Setenv(EnvToken, "")
	t.Setenv(EnvSocket, filepath.Join(t.TempDir(), "a.sock"))
	if err := Init(context.Background()); err == nil {
		t.Fatal("expected token error")
	}
	t.Setenv(EnvToken, "tok")
	t.Setenv(EnvSocket, filepath.Join(t.TempDir(), "missing-dir", "a.sock"))
	if err := Init(context.Background()); err == nil {
		t.Fatal("expected listen error")
	}

	t.Setenv(EnvSocket, "")
	_ = os.Remove(DefaultSocket)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- Init(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(DefaultSocket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("default socket did not appear")
		}
		time.Sleep(10 * time.Millisecond)
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
	_ = os.Remove(DefaultSocket)
}

func TestServeTCP(t *testing.T) {
	t.Setenv(EnvToken, "serve-tok")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = old })

	errc := make(chan error, 1)
	go func() { errc <- Run([]string{"serve", "-listen", "127.0.0.1:0"}) }()

	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "sbx-agent listening on "
	if !strings.HasPrefix(line, prefix) {
		t.Fatalf("line %q", line)
	}
	addr := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := Handshake(ctx, conn, "serve-tok")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Alive() {
		t.Fatal("expected a live session")
	}
	out, err := c.Exec(ctx, ExecSpec{Cmd: []string{"echo", "served"}}, nil, nil)
	if err != nil || out.ExitCode != 0 {
		t.Fatalf("%+v %v", out, err)
	}
	c.Close()

	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not stop")
	}
}
