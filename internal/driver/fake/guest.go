package fake

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

var _ driver.Guest = (*Driver)(nil)

// GuestEnabled reports whether agent-only calls are available.
func (d *Driver) GuestEnabled() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.NoGuest
}

// ProcStdin returns bytes written to a background process. Tests use it.
func (d *Driver) ProcStdin(id, procID string) []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.procs[id][procID]
	if p == nil {
		return nil
	}
	out := make([]byte, len(p.stdin))
	copy(out, p.stdin)
	return out
}

// ProcSize returns the last recorded terminal size.
func (d *Driver) ProcSize(id, procID string) (int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.procs[id][procID]
	if p == nil {
		return 0, 0
	}
	return p.cols, p.rows
}

func (d *Driver) StatFile(_ context.Context, id, path string) (driver.FileEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.boxes[id]; !ok {
		return driver.FileEntry{}, driver.ErrNotFound
	}
	if content, ok := d.files[id][path]; ok {
		return driver.FileEntry{Name: base(path), Path: path, Type: driver.FileFile, Size: int64(len(content)), Mode: 0o644, ModTime: time.Unix(1700000000, 0).UTC()}, nil
	}
	if d.isDir(id, path) {
		return driver.FileEntry{Name: base(path), Path: path, Type: driver.FileDir, Mode: 0o755, ModTime: time.Unix(1700000000, 0).UTC()}, nil
	}
	return driver.FileEntry{}, fmt.Errorf("no such file")
}

func (d *Driver) Mkdir(_ context.Context, id, path string, mode uint32) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.boxes[id]; !ok {
		return driver.ErrNotFound
	}
	if mode == 0 {
		mode = 0o755
	}
	if d.dirs[id] == nil {
		d.dirs[id] = map[string]struct{}{}
	}
	d.dirs[id][path] = struct{}{}
	_ = mode
	return nil
}

func (d *Driver) Watch(ctx context.Context, id, path string, recursive bool, emit func(driver.FileEvent) error) error {
	d.mu.Lock()
	if _, ok := d.boxes[id]; !ok {
		d.mu.Unlock()
		return driver.ErrNotFound
	}
	sub := &watch{path: path, recursive: recursive, ch: make(chan driver.FileEvent, 8)}
	d.watchers[id] = append(d.watchers[id], sub)
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		list := d.watchers[id][:0]
		for _, item := range d.watchers[id] {
			if item != sub {
				list = append(list, item)
			}
		}
		d.watchers[id] = list
		d.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-sub.ch:
			if emit != nil {
				if err := emit(ev); err != nil {
					return err
				}
			}
		}
	}
}

func (d *Driver) StartProcess(_ context.Context, id string, req driver.ExecRequest, tty bool, cols, rows int) (driver.Process, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.boxes[id]; !ok {
		return driver.Process{}, driver.ErrNotFound
	}
	d.seq++
	prefix := "proc_"
	if tty {
		prefix = "term_"
	}
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	p := &proc{
		done: make(chan struct{}),
		cols: cols,
		rows: rows,
		info: driver.Process{
			ID:        fmt.Sprintf("%s%d", prefix, d.seq),
			PID:       1000 + d.seq,
			Cmd:       append([]string(nil), req.Cmd...),
			Cwd:       req.Cwd,
			TTY:       tty,
			Status:    "running",
			StartedAt: time.Now().UTC(),
		},
	}
	if d.procs[id] == nil {
		d.procs[id] = map[string]*proc{}
	}
	d.procs[id][p.info.ID] = p
	return p.info, nil
}

func (d *Driver) ListProcesses(_ context.Context, id string) ([]driver.Process, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.boxes[id]; !ok {
		return nil, driver.ErrNotFound
	}
	out := make([]driver.Process, 0, len(d.procs[id]))
	for _, p := range d.procs[id] {
		out = append(out, p.info)
	}
	return out, nil
}

func (d *Driver) SignalProcess(_ context.Context, id, procID, signal string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.procs[id][procID]
	if p == nil {
		return fmt.Errorf("process not found")
	}
	if p.info.Status == "exited" {
		return nil
	}
	p.info.Status = "exited"
	if signal == "KILL" {
		p.info.ExitCode = 137
	} else {
		p.info.ExitCode = 143
	}
	p.exit = p.info.ExitCode
	p.once.Do(func() { close(p.done) })
	return nil
}

func (d *Driver) WriteProcessStdin(_ context.Context, id, procID string, data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.procs[id][procID]
	if p == nil {
		return fmt.Errorf("process not found")
	}
	p.stdin = append(p.stdin, data...)
	return nil
}

func (d *Driver) StreamProcess(ctx context.Context, id, procID string, emit func(driver.ExecChunk) error) (driver.ExecResult, error) {
	d.mu.Lock()
	p := d.procs[id][procID]
	d.mu.Unlock()
	if p == nil {
		return driver.ExecResult{}, fmt.Errorf("process not found")
	}
	if emit != nil {
		if err := emit(driver.ExecChunk{Kind: driver.StreamStdout, Data: []byte("ok\n")}); err != nil {
			return driver.ExecResult{}, err
		}
	}
	select {
	case <-ctx.Done():
		return driver.ExecResult{}, ctx.Err()
	case <-p.done:
		d.mu.Lock()
		code := p.exit
		d.mu.Unlock()
		return driver.ExecResult{ExitCode: code}, nil
	}
}

func (d *Driver) ResizeProcess(_ context.Context, id, procID string, cols, rows int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.procs[id][procID]
	if p == nil {
		return fmt.Errorf("process not found")
	}
	p.cols, p.rows = cols, rows
	return nil
}

func (d *Driver) DialPort(_ context.Context, id string, port int) (io.ReadWriteCloser, error) {
	d.mu.Lock()
	_, ok := d.boxes[id]
	dialErr := d.DialErr
	drop := d.DropDial
	d.mu.Unlock()
	if !ok {
		return nil, driver.ErrNotFound
	}
	if dialErr != nil {
		return nil, dialErr
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid port")
	}
	client, server := net.Pipe()
	if drop {
		server.Close()
		return client, nil
	}
	go func() {
		defer server.Close()
		req, err := http.ReadRequest(bufio.NewReader(server))
		if err != nil {
			return
		}
		body := "preview-ok"
		resp := &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        make(http.Header),
			ContentLength: int64(len(body)),
			Body:          io.NopCloser(strings.NewReader(body)),
			Request:       req,
		}
		resp.Header.Set("Content-Type", "text/plain")
		resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
		// Tests read back which cookies the proxy forwarded.
		resp.Header.Set("X-Fake-Cookie", req.Header.Get("Cookie"))
		resp.Header.Set("X-Fake-Referer", req.Header.Get("Referer"))
		// A hostile app asks for a root-scoped service worker and sets a
		// cookie; the proxy must drop both on the API origin.
		resp.Header.Set("Service-Worker-Allowed", "/")
		resp.Header.Set("Set-Cookie", "app=1; Path=/")
		_ = resp.Write(server)
	}()
	return client, nil
}

func (d *Driver) isDir(id, path string) bool {
	if _, ok := d.dirs[id][path]; ok {
		return true
	}
	prefix := strings.TrimRight(path, "/") + "/"
	for name := range d.files[id] {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	for name := range d.dirs[id] {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return path == "/workspace" || path == "/tmp" || path == "/"
}

func (d *Driver) notify(id string, ev driver.FileEvent) {
	for _, sub := range d.watchers[id] {
		if !strings.HasPrefix(ev.Path, strings.TrimRight(sub.path, "/")+"/") && ev.Path != sub.path {
			continue
		}
		if !sub.recursive {
			rest := strings.TrimPrefix(ev.Path, strings.TrimRight(sub.path, "/")+"/")
			if strings.Contains(rest, "/") {
				continue
			}
		}
		select {
		case sub.ch <- ev:
		default:
		}
	}
}

func base(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
