package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
)

type nilList struct{ *fake.Driver }

func (nilList) ListProcesses(context.Context, string) ([]driver.Process, error) {
	return nil, nil
}

func TestGuestValidationAndPreviews(t *testing.T) {
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()
	actor := coreActor(t, st)
	sb, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := mgr.StatFile(ctx, actor, sb.ID, "relative"); err == nil {
		t.Fatal("expected relative path")
	}
	if _, err := mgr.StatFile(ctx, actor, sb.ID, "/workspace/missing"); err == nil {
		t.Fatal("expected missing file")
	}
	if err := mgr.Mkdir(ctx, actor, sb.ID, "/workspace/dir", 0o1000); err == nil {
		t.Fatal("expected mode error")
	}
	if err := mgr.Mkdir(ctx, actor, sb.ID, "relative", 0o755); err == nil {
		t.Fatal("expected path error")
	}
	if err := mgr.Mkdir(ctx, actor, sb.ID, "/workspace/dir", 0o755); err != nil {
		t.Fatal(err)
	}
	entry, err := mgr.StatFile(ctx, actor, sb.ID, "/workspace/dir")
	if err != nil || entry.Type != driver.FileDir {
		t.Fatalf("%+v %v", entry, err)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	cancel()
	err = mgr.Watch(watchCtx, actor, sb.ID, "/workspace", false, func(driver.FileEvent) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := mgr.Watch(ctx, actor, sb.ID, "", false, nil); err == nil {
		t.Fatal("expected empty path")
	}

	if _, err := mgr.StartProcess(ctx, actor, sb.ID, ExecInput{}, false, 0, 0); err == nil {
		t.Fatal("expected empty command")
	}
	if _, err := mgr.StartProcess(ctx, actor, sb.ID, ExecInput{Cmd: []string{"sh"}}, true, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.StartProcess(ctx, actor, sb.ID, ExecInput{Cmd: []string{"sh"}}, true, 800, 10); err == nil {
		t.Fatal("expected size error")
	}
	proc, err := mgr.StartProcess(ctx, actor, sb.ID, ExecInput{Cmd: []string{"sleep"}}, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := mgr.GetProcess(ctx, actor, sb.ID, proc.ID)
	if err != nil || got.ID != proc.ID {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := mgr.GetProcess(ctx, actor, sb.ID, "proc_missing"); err == nil {
		t.Fatal("expected missing process")
	}
	if err := mgr.SignalProcess(ctx, actor, sb.ID, proc.ID, "BOOM"); err == nil {
		t.Fatal("expected signal error")
	}
	if err := mgr.SignalProcess(ctx, actor, sb.ID, "missing", "TERM"); err == nil {
		t.Fatal("expected missing signal")
	}
	if err := mgr.WriteProcessStdin(ctx, actor, sb.ID, proc.ID, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	mgr.cfg.Limits.MaxUploadBytes = 1
	if err := mgr.WriteProcessStdin(ctx, actor, sb.ID, proc.ID, []byte("too")); err == nil {
		t.Fatal("expected stdin limit")
	}
	mgr.cfg.Limits.MaxUploadBytes = 32 << 20
	if err := mgr.ResizeProcess(ctx, actor, sb.ID, proc.ID, 0, 10); err == nil {
		t.Fatal("expected resize error")
	}
	if err := mgr.ResizeProcess(ctx, actor, sb.ID, proc.ID, 20, 10); err != nil {
		t.Fatal(err)
	}

	streamCtx, streamCancel := context.WithCancel(ctx)
	streamCancel()
	if _, err := mgr.StreamProcess(streamCtx, actor, sb.ID, proc.ID, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := mgr.SignalProcess(ctx, actor, sb.ID, proc.ID, "INT"); err != nil {
		t.Fatal(err)
	}

	if _, err := mgr.CreatePreview(ctx, actor, sb.ID, 0, false); err == nil {
		t.Fatal("expected port error")
	}
	first, err := mgr.CreatePreview(ctx, actor, sb.ID, 3000, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.CreatePreview(ctx, actor, sb.ID, 3000, false); err == nil {
		t.Fatal("expected duplicate port")
	}
	for port := 3001; port < 3016; port++ {
		if _, err := mgr.CreatePreview(ctx, actor, sb.ID, port, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mgr.CreatePreview(ctx, actor, sb.ID, 4000, false); err == nil {
		t.Fatal("expected preview cap")
	}
	items, err := mgr.ListPreviews(ctx, actor, sb.ID)
	if err != nil || len(items) != 16 {
		t.Fatalf("previews %d %v", len(items), err)
	}
	if err := mgr.DeletePreview(ctx, actor, sb.ID, "prv_missing"); err == nil {
		t.Fatal("expected missing preview")
	}
	if _, _, err := mgr.DialPreview(ctx, actor, sb.ID, "prv_missing"); err == nil {
		t.Fatal("expected missing dial")
	}
	conn, pv, err := mgr.DialPreview(ctx, actor, sb.ID, first.ID)
	if err != nil || pv.Port != 3000 {
		t.Fatalf("%+v %v", pv, err)
	}
	conn.Close()
	fd.DialErr = errors.New("dial failed")
	if _, _, err := mgr.DialPreview(ctx, actor, sb.ID, first.ID); err == nil {
		t.Fatal("expected dial error")
	}
	fd.DialErr = nil
	if err := mgr.DeletePreview(ctx, actor, sb.ID, first.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := mgr.Delete(ctx, actor, sb.ID); err != nil {
		t.Fatal(err)
	}
	left, err := mgr.ListPreviews(ctx, actor, sb.ID)
	if err != nil || len(left) != 0 {
		t.Fatalf("after delete %d %v", len(left), err)
	}
	if _, err := mgr.StatFile(ctx, actor, sb.ID, "/workspace"); err == nil {
		t.Fatal("expected stopped sandbox")
	}
}

func TestGuestDisabledAndEmptyList(t *testing.T) {
	st := openStore(t)
	fd := fake.New("docker")
	fd.NoGuest = true
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()
	actor := coreActor(t, st)
	sb, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.ListProcesses(ctx, actor, sb.ID); err == nil {
		t.Fatal("expected disabled agent")
	}
	if _, err := mgr.ListPreviews(ctx, actor, "sbx_missing"); err == nil {
		t.Fatal("expected missing sandbox")
	}

	wrapped := New(config.Default(), st, map[string]driver.Driver{"docker": nilList{fd}}, discard())
	fd.NoGuest = false
	other, err := wrapped.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := wrapped.ListProcesses(ctx, actor, other.ID)
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("%#v %v", items, err)
	}
	deadline, stop := context.WithTimeout(ctx, time.Nanosecond)
	defer stop()
	time.Sleep(time.Millisecond)
	if err := wrapped.Watch(deadline, actor, other.ID, "/workspace", true, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() {
		errc <- wrapped.Watch(ctx, actor, other.ID, "/workspace", true, func(driver.FileEvent) error {
			return errors.New("watch failed")
		})
	}()
	time.Sleep(20 * time.Millisecond)
	if err := wrapped.WriteFile(ctx, actor, other.ID, "/workspace/a.txt", []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "watch failed") {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not fail")
	}
}
