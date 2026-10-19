package fake

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

func TestGuestErrorsAndWatch(t *testing.T) {
	d := New("docker")
	ctx := context.Background()
	if _, err := d.StatFile(ctx, "missing", "/workspace"); !errors.Is(err, driver.ErrNotFound) {
		t.Fatal(err)
	}
	if err := d.Mkdir(ctx, "missing", "/workspace/a", 0o755); !errors.Is(err, driver.ErrNotFound) {
		t.Fatal(err)
	}
	if err := d.Watch(ctx, "missing", "/workspace", false, nil); !errors.Is(err, driver.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := d.StartProcess(ctx, "missing", driver.ExecRequest{Cmd: []string{"echo"}}, false, 0, 0); !errors.Is(err, driver.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := d.ListProcesses(ctx, "missing"); !errors.Is(err, driver.ErrNotFound) {
		t.Fatal(err)
	}
	if err := d.SignalProcess(ctx, "missing", "proc_1", "KILL"); err == nil {
		t.Fatal("expected missing signal")
	}
	if err := d.WriteProcessStdin(ctx, "missing", "proc_1", []byte("x")); err == nil {
		t.Fatal("expected missing stdin")
	}
	if _, err := d.StreamProcess(ctx, "missing", "proc_1", nil); err == nil {
		t.Fatal("expected missing stream")
	}
	if err := d.ResizeProcess(ctx, "missing", "proc_1", 1, 1); err == nil {
		t.Fatal("expected missing resize")
	}
	if _, err := d.DialPort(ctx, "missing", 80); !errors.Is(err, driver.ErrNotFound) {
		t.Fatal(err)
	}

	sb, err := d.Create(ctx, driver.SandboxSpec{ID: "sb", Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Mkdir(ctx, sb.ID, "/workspace/sub", 0); err != nil {
		t.Fatal(err)
	}
	entry, err := d.StatFile(ctx, sb.ID, "/workspace/sub")
	if err != nil || entry.Type != driver.FileDir {
		t.Fatalf("%+v %v", entry, err)
	}
	if _, err := d.StatFile(ctx, sb.ID, "/workspace/nope"); err == nil {
		t.Fatal("expected missing file")
	}
	proc, err := d.StartProcess(ctx, sb.ID, driver.ExecRequest{Cmd: []string{"sleep"}}, true, 0, 0)
	if err != nil || proc.ID == "" {
		t.Fatal(err)
	}
	if err := d.SignalProcess(ctx, sb.ID, proc.ID, "TERM"); err != nil {
		t.Fatal(err)
	}
	if err := d.SignalProcess(ctx, sb.ID, proc.ID, "TERM"); err != nil {
		t.Fatal(err)
	}
	running, err := d.StartProcess(ctx, sb.ID, driver.ExecRequest{Cmd: []string{"sleep"}}, false, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.StreamProcess(ctx, sb.ID, running.ID, func(driver.ExecChunk) error { return errors.New("stop") }); err == nil {
		t.Fatal("expected emit error")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := d.StreamProcess(canceled, sb.ID, running.ID, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	watchCtx, stop := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() {
		errc <- d.Watch(watchCtx, sb.ID, "/workspace", false, func(ev driver.FileEvent) error {
			if ev.Path == "/workspace/nested/a.txt" {
				return errors.New("skip")
			}
			return errors.New("got " + ev.Path)
		})
	}()
	time.Sleep(20 * time.Millisecond)
	if err := d.WriteFile(ctx, sb.ID, "/workspace/nested/a.txt", []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteFile(ctx, sb.ID, "/workspace/b.txt", []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errc:
		if err == nil || err.Error() != "got /workspace/b.txt" {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not emit")
	}
	stop()

	d.DialErr = errors.New("no dial")
	if _, err := d.DialPort(ctx, sb.ID, 80); err == nil {
		t.Fatal("expected dial error")
	}
	d.DialErr = nil
	if _, err := d.DialPort(ctx, sb.ID, 0); err == nil {
		t.Fatal("expected port error")
	}
	conn, err := d.DialPort(ctx, sb.ID, 9)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil || n == 0 {
		t.Fatalf("preview %q %v", buf[:n], err)
	}
	if base("plain") != "plain" || base("/a/b") != "b" {
		t.Fatal(base("/a/b"))
	}
}
