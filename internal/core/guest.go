package core

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/id"
	"github.com/TorstenDittmann/sbx/internal/store"
)

// Preview is a loopback port published through the API.
type Preview struct {
	ID        string
	SandboxID string
	Port      int
	CreatedAt time.Time
}

func (m *Manager) guest(ctx context.Context, actor Actor, sandboxID string) (store.Sandbox, driver.Guest, error) {
	sb, d, err := m.running(ctx, actor, sandboxID)
	if err != nil {
		return store.Sandbox{}, nil, err
	}
	g, ok := d.(driver.Guest)
	if !ok || !g.GuestEnabled() {
		return store.Sandbox{}, nil, unsupported("sandbox agent is not enabled")
	}
	return sb, g, nil
}

func (m *Manager) mapGuest(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "not found") || strings.Contains(msg, "no such") {
		return notFound(msg)
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return m.asDriver(err)
}

// StatFile returns one path inside a sandbox.
func (m *Manager) StatFile(ctx context.Context, actor Actor, sandboxID, path string) (driver.FileEntry, error) {
	sb, g, err := m.guest(ctx, actor, sandboxID)
	if err != nil {
		return driver.FileEntry{}, err
	}
	path, err = cleanPath(path)
	if err != nil {
		return driver.FileEntry{}, err
	}
	entry, err := g.StatFile(ctx, sb.ContainerID, path)
	m.touch(ctx, sb)
	m.Counts.FileOps.Add(1)
	if err != nil {
		return driver.FileEntry{}, m.mapGuest(err)
	}
	return entry, nil
}

// Mkdir creates a directory inside a sandbox.
func (m *Manager) Mkdir(ctx context.Context, actor Actor, sandboxID, path string, mode uint32) error {
	if mode > 0o777 {
		return invalid("mode must be an octal permission")
	}
	sb, g, err := m.guest(ctx, actor, sandboxID)
	if err != nil {
		return err
	}
	path, err = cleanPath(path)
	if err != nil {
		return err
	}
	if err := g.Mkdir(ctx, sb.ContainerID, path, mode); err != nil {
		return m.mapGuest(err)
	}
	m.touch(ctx, sb)
	m.Counts.FileOps.Add(1)
	return nil
}

// Watch emits filesystem events until ctx ends.
func (m *Manager) Watch(ctx context.Context, actor Actor, sandboxID, path string, recursive bool, emit func(driver.FileEvent) error) error {
	sb, g, err := m.guest(ctx, actor, sandboxID)
	if err != nil {
		return err
	}
	path, err = cleanPath(path)
	if err != nil {
		return err
	}
	m.touch(ctx, sb)
	err = g.Watch(ctx, sb.ContainerID, path, recursive, emit)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return m.mapGuest(err)
}

// StartProcess starts a background command or terminal.
func (m *Manager) StartProcess(ctx context.Context, actor Actor, sandboxID string, in ExecInput, tty bool, cols, rows int) (driver.Process, error) {
	if tty {
		if cols == 0 {
			cols = 80
		}
		if rows == 0 {
			rows = 24
		}
		if cols < 1 || cols > 512 || rows < 1 || rows > 512 {
			return driver.Process{}, invalid("cols and rows must be between 1 and 512")
		}
	}
	in.Background = true
	sb, g, err := m.guest(ctx, actor, sandboxID)
	if err != nil {
		return driver.Process{}, err
	}
	req, err := m.execReq(in)
	if err != nil {
		return driver.Process{}, err
	}
	proc, err := g.StartProcess(ctx, sb.ContainerID, req, tty, cols, rows)
	m.touch(ctx, sb)
	if err != nil {
		return driver.Process{}, m.mapGuest(err)
	}
	return proc, nil
}

// ListProcesses returns commands tracked by the agent.
func (m *Manager) ListProcesses(ctx context.Context, actor Actor, sandboxID string) ([]driver.Process, error) {
	sb, g, err := m.guest(ctx, actor, sandboxID)
	if err != nil {
		return nil, err
	}
	items, err := g.ListProcesses(ctx, sb.ContainerID)
	m.touch(ctx, sb)
	if err != nil {
		return nil, m.mapGuest(err)
	}
	if items == nil {
		items = []driver.Process{}
	}
	return items, nil
}

// GetProcess returns one tracked command.
func (m *Manager) GetProcess(ctx context.Context, actor Actor, sandboxID, procID string) (driver.Process, error) {
	items, err := m.ListProcesses(ctx, actor, sandboxID)
	if err != nil {
		return driver.Process{}, err
	}
	for _, item := range items {
		if item.ID == procID {
			return item, nil
		}
	}
	return driver.Process{}, notFound("process not found")
}

// SignalProcess delivers a signal to a tracked command.
func (m *Manager) SignalProcess(ctx context.Context, actor Actor, sandboxID, procID, signal string) error {
	switch signal {
	case "TERM", "KILL", "INT", "HUP", "QUIT":
	default:
		return invalid("signal must be TERM, KILL, INT, HUP, or QUIT")
	}
	sb, g, err := m.guest(ctx, actor, sandboxID)
	if err != nil {
		return err
	}
	if err := g.SignalProcess(ctx, sb.ContainerID, procID, signal); err != nil {
		return m.mapGuest(err)
	}
	m.touch(ctx, sb)
	return nil
}

// WriteProcessStdin appends bytes to a background process and leaves stdin open.
func (m *Manager) WriteProcessStdin(ctx context.Context, actor Actor, sandboxID, procID string, data []byte) error {
	if len(data) > m.cfg.Limits.MaxUploadBytes {
		return invalid("stdin exceeds the size limit")
	}
	sb, g, err := m.guest(ctx, actor, sandboxID)
	if err != nil {
		return err
	}
	if err := g.WriteProcessStdin(ctx, sb.ContainerID, procID, data); err != nil {
		return m.mapGuest(err)
	}
	m.touch(ctx, sb)
	return nil
}

// StreamProcess follows a background process until it exits.
func (m *Manager) StreamProcess(ctx context.Context, actor Actor, sandboxID, procID string, emit func(driver.ExecChunk) error) (driver.ExecResult, error) {
	sb, g, err := m.guest(ctx, actor, sandboxID)
	if err != nil {
		return driver.ExecResult{}, err
	}
	res, err := g.StreamProcess(ctx, sb.ContainerID, procID, emit)
	m.touch(ctx, sb)
	if err != nil {
		return res, m.mapGuest(err)
	}
	return res, nil
}

// ResizeProcess changes a terminal's window size.
func (m *Manager) ResizeProcess(ctx context.Context, actor Actor, sandboxID, procID string, cols, rows int) error {
	if cols < 1 || cols > 512 || rows < 1 || rows > 512 {
		return invalid("cols and rows must be between 1 and 512")
	}
	sb, g, err := m.guest(ctx, actor, sandboxID)
	if err != nil {
		return err
	}
	if err := g.ResizeProcess(ctx, sb.ContainerID, procID, cols, rows); err != nil {
		return m.mapGuest(err)
	}
	m.touch(ctx, sb)
	return nil
}

// CreatePreview registers a loopback port on a sandbox.
func (m *Manager) CreatePreview(ctx context.Context, actor Actor, sandboxID string, port int) (Preview, error) {
	if port < 1 || port > 65535 {
		return Preview{}, invalid("port must be between 1 and 65535")
	}
	sb, _, err := m.guest(ctx, actor, sandboxID)
	if err != nil {
		return Preview{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, pv := range m.previews {
		if pv.SandboxID == sb.ID {
			if pv.Port == port {
				return Preview{}, conflict("that port is already previewed")
			}
			n++
		}
	}
	if n >= 16 {
		return Preview{}, invalid("too many previews")
	}
	pv := Preview{ID: id.Preview(), SandboxID: sb.ID, Port: port, CreatedAt: m.now()}
	m.previews[pv.ID] = pv
	m.touch(ctx, sb)
	return pv, nil
}

// ListPreviews returns previews for one sandbox.
func (m *Manager) ListPreviews(ctx context.Context, actor Actor, sandboxID string) ([]Preview, error) {
	sb, err := m.visible(ctx, actor, sandboxID)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Preview{}
	for _, pv := range m.previews {
		if pv.SandboxID == sb.ID {
			out = append(out, pv)
		}
	}
	return out, nil
}

// DeletePreview removes one preview.
func (m *Manager) DeletePreview(ctx context.Context, actor Actor, sandboxID, previewID string) error {
	sb, err := m.visible(ctx, actor, sandboxID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	pv, ok := m.previews[previewID]
	if !ok || pv.SandboxID != sb.ID {
		return notFound("preview not found")
	}
	delete(m.previews, previewID)
	return nil
}

// DialPreview opens a stream to a previewed loopback port.
func (m *Manager) DialPreview(ctx context.Context, actor Actor, sandboxID, previewID string) (io.ReadWriteCloser, Preview, error) {
	sb, g, err := m.guest(ctx, actor, sandboxID)
	if err != nil {
		return nil, Preview{}, err
	}
	m.mu.Lock()
	pv, ok := m.previews[previewID]
	m.mu.Unlock()
	if !ok || pv.SandboxID != sb.ID {
		return nil, Preview{}, notFound("preview not found")
	}
	conn, err := g.DialPort(ctx, sb.ContainerID, pv.Port)
	if err != nil {
		return nil, Preview{}, m.mapGuest(err)
	}
	m.touch(ctx, sb)
	return conn, pv, nil
}

// dropPreviews removes previews for a sandbox. The caller holds m.mu.
func (m *Manager) dropPreviews(sandboxID string) {
	for id, pv := range m.previews {
		if pv.SandboxID == sandboxID {
			delete(m.previews, id)
		}
	}
}
