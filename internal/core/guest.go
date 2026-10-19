package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	ID          string
	SandboxID   string
	Port        int
	Public      bool
	Token       string
	TokenHash   string
	BytesPerSec int
	MaxStreams  int
	CreatedAt   time.Time
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
	req, err := m.execReq(in, sb.Secrets)
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
// public previews can be opened without a credential when the operator allows it.
// The returned Token is shown once.
func (m *Manager) CreatePreview(ctx context.Context, actor Actor, sandboxID string, port int, public bool) (Preview, error) {
	if port < 1 || port > 65535 {
		return Preview{}, invalid("port must be between 1 and 65535")
	}
	if public && !m.cfg.Network.AllowPublicPreviews {
		return Preview{}, forbidden("public previews are disabled")
	}
	sb, _, err := m.guest(ctx, actor, sandboxID)
	if err != nil {
		return Preview{}, err
	}
	token, hash, err := newPreviewToken()
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
	maxStreams := m.cfg.Network.PreviewMaxStreams
	if maxStreams < 1 {
		maxStreams = 64
	}
	if sb.MaxConns > 0 && sb.MaxConns < maxStreams {
		maxStreams = sb.MaxConns
	}
	bytesPerSec := 0
	if sb.EgressKbps > 0 {
		bytesPerSec = sb.EgressKbps * 1000 / 8
	}
	pv := Preview{
		ID: id.Preview(), SandboxID: sb.ID, Port: port, Public: public,
		Token: token, TokenHash: hash, BytesPerSec: bytesPerSec, MaxStreams: maxStreams,
		CreatedAt: m.now(),
	}
	m.previews[pv.ID] = pv
	m.touch(ctx, sb)
	return pv, nil
}

func newPreviewToken() (string, string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	plain := "pvt_" + hex.EncodeToString(raw[:])
	return plain, id.Hash(plain), nil
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
			pv.Token = ""
			out = append(out, pv)
		}
	}
	return out, nil
}

// PreviewByID returns one preview. The token plaintext is not included.
func (m *Manager) PreviewByID(sandboxID, previewID string) (Preview, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pv, ok := m.previews[previewID]
	if !ok || pv.SandboxID != sandboxID {
		return Preview{}, false
	}
	pv.Token = ""
	return pv, true
}

// PreviewByPort returns the preview published on a sandbox port.
func (m *Manager) PreviewByPort(sandboxID string, port int) (Preview, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pv := range m.previews {
		if pv.SandboxID == sandboxID && pv.Port == port {
			pv.Token = ""
			return pv, true
		}
	}
	return Preview{}, false
}

// PreviewTokenOK reports whether plaintext matches the preview token.
func (m *Manager) PreviewTokenOK(pv Preview, plaintext string) bool {
	if plaintext == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.previews[pv.ID]
	if !ok {
		return false
	}
	return cur.TokenHash == id.Hash(plaintext)
}

// AcquirePreview reserves one concurrent stream. ReleasePreview frees it.
func (m *Manager) AcquirePreview(sandboxID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	max := m.cfg.Network.PreviewMaxStreams
	if max < 1 {
		max = 64
	}
	for _, pv := range m.previews {
		if pv.SandboxID == sandboxID && pv.MaxStreams > 0 && pv.MaxStreams < max {
			max = pv.MaxStreams
		}
	}
	if m.streams == nil {
		m.streams = map[string]int{}
	}
	if m.streams[sandboxID] >= max {
		return quota("too many preview connections")
	}
	m.streams[sandboxID]++
	return nil
}

// ReleasePreview frees a stream reserved by AcquirePreview.
func (m *Manager) ReleasePreview(sandboxID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.streams == nil {
		return
	}
	if m.streams[sandboxID] > 0 {
		m.streams[sandboxID]--
	}
}

// ThrottlePreview waits so preview traffic stays under the sandbox byte rate.
func (m *Manager) ThrottlePreview(sandboxID string, n int) {
	if n <= 0 {
		return
	}
	m.mu.Lock()
	var rate float64
	for _, pv := range m.previews {
		if pv.SandboxID == sandboxID && pv.BytesPerSec > 0 {
			rate = float64(pv.BytesPerSec)
			break
		}
	}
	if rate <= 0 {
		m.mu.Unlock()
		return
	}
	if m.budgets == nil {
		m.budgets = map[string]*byteBudget{}
	}
	b := m.budgets[sandboxID]
	if b == nil {
		b = &byteBudget{rate: rate, tokens: rate}
		m.budgets[sandboxID] = b
	}
	now := m.now()
	if !b.last.IsZero() {
		b.tokens += now.Sub(b.last).Seconds() * b.rate
		if b.tokens > b.rate {
			b.tokens = b.rate
		}
	}
	b.last = now
	b.tokens -= float64(n)
	wait := time.Duration(0)
	if b.tokens < 0 {
		wait = time.Duration((-b.tokens / b.rate) * float64(time.Second))
	}
	m.mu.Unlock()
	if wait > 0 {
		time.Sleep(wait)
	}
}

type byteBudget struct {
	rate   float64
	tokens float64
	last   time.Time
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
	return m.dialPreview(ctx, sb, g, previewID)
}

// OpenPreview dials a preview after the HTTP layer has authorized it.
// It does not check which API key owns the sandbox.
func (m *Manager) OpenPreview(ctx context.Context, sandboxID, previewID string) (io.ReadWriteCloser, Preview, error) {
	sb, err := m.store.GetSandbox(ctx, sandboxID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, Preview{}, notFound("sandbox not found")
	}
	if err != nil {
		return nil, Preview{}, err
	}
	if sb.Status == "paused" {
		return nil, Preview{}, conflict("sandbox is paused")
	}
	if sb.Status != "running" {
		return nil, Preview{}, conflict("sandbox is not running")
	}
	d, err := m.backend(sb)
	if err != nil {
		return nil, Preview{}, err
	}
	g, ok := d.(driver.Guest)
	if !ok || !g.GuestEnabled() {
		return nil, Preview{}, unsupported("sandbox agent is not enabled")
	}
	return m.dialPreview(ctx, sb, g, previewID)
}

func (m *Manager) dialPreview(ctx context.Context, sb store.Sandbox, g driver.Guest, previewID string) (io.ReadWriteCloser, Preview, error) {
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
	pv.Token = ""
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
