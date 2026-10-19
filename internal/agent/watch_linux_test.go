//go:build linux

package agent

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestWatchWriteRemoveRecursiveAndFile(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "sub")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	a := New("tok")
	c, cleanup := pairAgent(t, a)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events := make(chan FileEvent, 16)
	watchCtx, stop := context.WithCancel(ctx)
	go func() {
		_ = c.Watch(watchCtx, dir, false, func(ev FileEvent) error {
			events <- ev
			return nil
		})
	}()
	waitWatch(t, events, func() {
		if err := os.WriteFile(filepath.Join(nested, "hidden.txt"), []byte("h"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "n.txt"), []byte("z"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	ev := expectEvent(t, events, "create", "n.txt")
	if strings.Contains(ev.Path, "hidden.txt") {
		t.Fatalf("non-recursive watch saw %s", ev.Path)
	}
	select {
	case extra := <-events:
		if strings.Contains(extra.Path, "hidden.txt") {
			t.Fatalf("non-recursive watch saw %s", extra.Path)
		}
		if filepath.Base(extra.Path) == "n.txt" && extra.Type != "write" && extra.Type != "create" {
			t.Fatalf("unexpected %+v", extra)
		}
	case <-time.After(150 * time.Millisecond):
	}
	if err := os.WriteFile(filepath.Join(dir, "n.txt"), []byte("zz"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectEvent(t, events, "write", "n.txt")
	if err := os.Remove(filepath.Join(dir, "n.txt")); err != nil {
		t.Fatal(err)
	}
	expectEvent(t, events, "remove", "n.txt")
	stop()

	file := filepath.Join(dir, "one.txt")
	if err := os.WriteFile(file, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	fileCtx, stopFile := context.WithCancel(ctx)
	go func() {
		_ = c.Watch(fileCtx, file, false, func(ev FileEvent) error {
			events <- ev
			return nil
		})
	}()
	waitWatch(t, events, func() {
		if err := os.WriteFile(file, []byte("bb"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	expectEvent(t, events, "write", "one.txt")
	stopFile()

	recCtx, stopRec := context.WithCancel(ctx)
	go func() {
		_ = c.Watch(recCtx, dir, true, func(ev FileEvent) error {
			events <- ev
			return nil
		})
	}()
	time.Sleep(40 * time.Millisecond)
	deeper := filepath.Join(dir, "fresh")
	if err := os.Mkdir(deeper, 0o755); err != nil {
		t.Fatal(err)
	}
	expectEvent(t, events, "create", "fresh")
	if err := os.WriteFile(filepath.Join(deeper, "leaf.txt"), []byte("q"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectEvent(t, events, "create", "leaf.txt")
	stopRec()
}

func TestWatchHidesAtomicTempAndReplaces(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "n.txt")
	if err := os.WriteFile(dest, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := New("tok")
	c, cleanup := pairAgent(t, a)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := make(chan FileEvent, 8)
	watchCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		_ = c.Watch(watchCtx, dir, false, func(ev FileEvent) error {
			events <- ev
			return nil
		})
	}()
	waitWatch(t, events, func() {
		tmp := dest + ".sbx-tmp"
		if err := os.WriteFile(tmp, []byte("new"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, dest); err != nil {
			t.Fatal(err)
		}
	})
	ev := expectEvent(t, events, "write", "n.txt")
	if strings.Contains(ev.Path, ".sbx-tmp") {
		t.Fatalf("temp path leaked: %+v", ev)
	}
}

func TestWatchPollFallbackAndLimits(t *testing.T) {
	prevInit := inotifyInit
	inotifyInit = func() (int, error) { return -1, errInotifyUnavailable }
	t.Cleanup(func() { inotifyInit = prevInit })

	dir := t.TempDir()
	a := New("tok")
	a.watchEvery = 15 * time.Millisecond
	c, cleanup := pairAgent(t, a)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	missing := c.Watch(ctx, filepath.Join(dir, "missing"), false, nil)
	if missing == nil {
		t.Fatal("expected missing path to fail")
	}

	events := make(chan FileEvent, 4)
	watchCtx, stop := context.WithCancel(ctx)
	go func() {
		_ = c.Watch(watchCtx, dir, false, func(ev FileEvent) error {
			events <- ev
			return nil
		})
	}()
	time.Sleep(40 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "p.txt"), []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectEvent(t, events, "create", "p.txt")
	stop()

	prevMax := maxInotifyWatches
	maxInotifyWatches = 1
	inotifyInit = prevInit
	t.Cleanup(func() { maxInotifyWatches = prevMax })
	if err := os.Mkdir(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := c.Watch(ctx, dir, true, func(FileEvent) error { return nil })
	if err == nil || !errors.Is(err, errWatchTooLarge) && !strings.Contains(err.Error(), "too large") {
		t.Fatalf("limit: %v", err)
	}
}

func TestConsumeInotifyEvents(t *testing.T) {
	w := &inotifyWatch{
		userRoot: "/workspace",
		dir:      "/workspace",
		wd:       map[int]string{4: "/workspace"},
		seen:     map[string]int{"/workspace": 4},
		known:    map[string]struct{}{"/workspace/old.txt": {}},
		skip:     map[string]struct{}{"/workspace/skip.txt": {}},
		count:    1,
	}
	buf := append(inotifyBytes(4, unix.IN_CREATE, "new.txt"), inotifyBytes(4, unix.IN_CLOSE_WRITE, "new.txt")...)
	buf = append(buf, inotifyBytes(4, unix.IN_MOVED_TO, "old.txt")...)
	buf = append(buf, inotifyBytes(4, unix.IN_CREATE, "note.txt.sbx-tmp")...)
	buf = append(buf, inotifyBytes(4, unix.IN_CREATE, "skip.txt")...)
	buf = append(buf, inotifyBytes(4, unix.IN_DELETE, "old.txt")...)
	buf = append(buf, inotifyBytes(4, unix.IN_MODIFY, "other.txt")...)
	got, err := w.consume(buf)
	if err != nil {
		t.Fatal(err)
	}
	got = coalesceWatch(got)
	want := []FileEvent{
		{Type: "create", Path: "/workspace/new.txt"},
		{Type: "write", Path: "/workspace/old.txt"},
		{Type: "remove", Path: "/workspace/old.txt"},
		{Type: "write", Path: "/workspace/other.txt"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %+v want %+v", got, want)
		}
	}

	fileWatch := &inotifyWatch{
		userRoot: "/workspace/one.txt",
		dir:      "/workspace",
		file:     "one.txt",
		wd:       map[int]string{2: "/workspace"},
		known:    map[string]struct{}{"/workspace/one.txt": {}},
		skip:     map[string]struct{}{},
	}
	evs, err := fileWatch.consume(inotifyBytes(2, unix.IN_MODIFY, "nope.txt"))
	if err != nil || len(evs) != 0 {
		t.Fatalf("%+v %v", evs, err)
	}
	evs, err = fileWatch.consume(inotifyBytes(2, unix.IN_CLOSE_WRITE, "one.txt"))
	if err != nil || len(evs) != 1 || evs[0].Type != "write" || evs[0].Path != "/workspace/one.txt" {
		t.Fatalf("%+v %v", evs, err)
	}
	self, err := fileWatch.consume(inotifyBytes(2, unix.IN_DELETE_SELF, ""))
	if err != nil || len(self) != 1 || self[0].Type != "remove" {
		t.Fatalf("%+v %v", self, err)
	}

	if _, err := w.consume(inotifyBytes(-1, unix.IN_Q_OVERFLOW, "")); err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatal(err)
	}
	if _, err := w.consume([]byte{1, 2, 3}); err == nil || !strings.Contains(err.Error(), "short") {
		t.Fatal(err)
	}
	ignored := inotifyBytes(4, unix.IN_IGNORED, "")
	if _, err := w.consume(ignored); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.wd[4]; ok {
		t.Fatal("ignored watch was kept")
	}
	if _, ok := classifyInotify(unix.IN_ATTRIB); !ok {
		t.Fatal("attrib")
	}
	if _, ok := classifyInotify(unix.IN_IGNORED); ok {
		t.Fatal("ignored should not classify")
	}
	if len(coalesceWatch(nil)) != 0 || len(coalesceWatch([]FileEvent{{Type: "create", Path: "a"}})) != 1 {
		t.Fatal("coalesce short")
	}
}

func TestWatchNewDirSeesExistingChild(t *testing.T) {
	dir := t.TempDir()
	fd, err := inotifyInit()
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	w := &inotifyWatch{
		fd: fd, userRoot: dir, dir: dir, recursive: true,
		wd: map[int]string{}, seen: map[string]int{}, known: map[string]struct{}{}, skip: map[string]struct{}{},
	}
	if err := w.add(dir, true); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(dir, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "leaf.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	extra, err := w.watchNewDir(child)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range extra {
		if ev.Type == "create" && filepath.Base(ev.Path) == "leaf.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("%+v", extra)
	}
	again, err := w.watchNewDir(child)
	if err != nil || len(again) != 0 {
		t.Fatalf("%+v %v", again, err)
	}
}

func waitWatch(t *testing.T, events <-chan FileEvent, write func()) {
	t.Helper()
	time.Sleep(40 * time.Millisecond)
	write()
}

func expectEvent(t *testing.T, events <-chan FileEvent, typ, base string) FileEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if strings.HasSuffix(ev.Path, ".sbx-tmp") {
				continue
			}
			if filepath.Base(ev.Path) == base && ev.Type == typ {
				return ev
			}
			if filepath.Base(ev.Path) == base && ev.Type == "write" && typ == "remove" {
				continue
			}
			if filepath.Base(ev.Path) == base {
				t.Fatalf("event %+v, want %s", ev, typ)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s %s", typ, base)
		}
	}
}

func inotifyBytes(wd int32, mask uint32, name string) []byte {
	rawLen := len(name) + 1
	buf := make([]byte, unix.SizeofInotifyEvent+rawLen)
	binary.NativeEndian.PutUint32(buf[0:4], uint32(wd))
	binary.NativeEndian.PutUint32(buf[4:8], mask)
	binary.NativeEndian.PutUint32(buf[12:16], uint32(rawLen))
	copy(buf[unix.SizeofInotifyEvent:], name)
	return buf
}
