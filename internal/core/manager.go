// Package core owns sandbox lifecycle: quotas, templates, TTL, and which
// driver a request is sent to.
package core

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/id"
	"github.com/TorstenDittmann/sbx/internal/store"
)

// Actor is the authenticated caller.
type Actor struct {
	KeyID string
	Admin bool
}

// CreateInput is a sandbox create request after JSON decoding.
type CreateInput struct {
	Template           string
	Image              string
	CPU                *float64
	MemoryMB           *int
	DiskMB             *int
	PIDs               *int
	TTLSeconds         *int
	IdleTimeoutSeconds *int
	Env                map[string]string
	Network            string
	Allow              []string
	Deny               []string
	HasAllow           bool
	HasDeny            bool
	EgressKbps         *int
	MaxConns           *int
	Backend            string
	Labels             map[string]string
	Mounts             []MountInput
	Secrets            map[string]string
}

// ExecInput runs a command. TimeoutSeconds 0 uses the server default.
// Stdin is optional raw input. Background processes treat timeout 0 as none.
type ExecInput struct {
	Cmd            []string
	Cwd            string
	Env            map[string]string
	TimeoutSeconds int
	Stdin          []byte
	Background     bool
}

// TemplateInput creates a template.
type TemplateInput struct {
	Name         string
	Image        string
	Description  string
	CPU          float64
	MemoryMB     int
	DiskMB       int
	Env          map[string]string
	Network      string
	Setup        []string
	SetupTimeout int
	Warm         int
}

// Counters are process-local totals for /metrics.
type Counters struct {
	Creates    atomic.Int64
	Deletes    atomic.Int64
	Execs      atomic.Int64
	ExecErrors atomic.Int64
	FileOps    atomic.Int64
	Reaped     atomic.Int64
}

// Manager is safe for concurrent use.
type Manager struct {
	cfg      config.Config
	store    *store.Store
	drivers  map[string]driver.Driver
	log      *slog.Logger
	now      func() time.Time
	mu       sync.Mutex
	previews map[string]Preview
	streams  map[string]int
	budgets  map[string]*byteBudget
	ready    atomic.Bool
	Counts   Counters
}

// New builds a manager. drivers is keyed by backend name ("docker", "gvisor").
func New(cfg config.Config, st *store.Store, drivers map[string]driver.Driver, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		cfg: cfg, store: st, drivers: drivers, log: log,
		now:      func() time.Time { return time.Now().UTC() },
		previews: map[string]Preview{},
	}
}

// SetClock replaces the clock. Tests use it.
func (m *Manager) SetClock(now func() time.Time) { m.now = now }

// Store exposes the state store for HTTP handlers that own keys directly.
func (m *Manager) Store() *store.Store { return m.store }

// Config returns the resolved config.
func (m *Manager) Config() config.Config { return m.cfg }

// Driver returns a named backend.
func (m *Manager) Driver(name string) (driver.Driver, bool) {
	d, ok := m.drivers[name]
	return d, ok
}

// Seed installs the built-in templates.
func (m *Manager) Seed(ctx context.Context) error {
	now := m.now()
	builtins := []store.Template{
		{Name: "base", Image: "alpine:3.20", Description: "Alpine Linux", CPU: 0.5, MemoryMB: 128, DiskMB: 256, CreatedAt: now},
		{Name: "python", Image: "python:3.12-slim", Description: "CPython 3.12", CPU: 1, MemoryMB: 512, DiskMB: 1024, CreatedAt: now},
		{Name: "node", Image: "node:22-bookworm-slim", Description: "Node.js 22", CPU: 1, MemoryMB: 512, DiskMB: 1024, CreatedAt: now},
	}
	for _, t := range builtins {
		if err := m.store.UpsertBuiltin(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

// Create starts a sandbox.
func (m *Manager) Create(ctx context.Context, actor Actor, in CreateInput) (store.Sandbox, error) {
	backend := in.Backend
	if backend == "" {
		backend = m.cfg.Backend
	}
	d, ok := m.drivers[backend]
	if !ok {
		return store.Sandbox{}, invalid("unknown backend " + backend)
	}
	if err := validEnv(in.Env); err != nil {
		return store.Sandbox{}, err
	}
	if err := validEnv(in.Secrets); err != nil {
		return store.Sandbox{}, err
	}
	if err := validLabels(in.Labels); err != nil {
		return store.Sandbox{}, err
	}

	var tpl store.Template
	var err error
	if in.Template != "" {
		if err := validTemplateName(in.Template); err != nil {
			return store.Sandbox{}, err
		}
		tpl, err = m.store.GetTemplate(ctx, in.Template)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return store.Sandbox{}, notFound("template not found")
			}
			return store.Sandbox{}, err
		}
	}
	netIn := in
	if netIn.Network == "" && tpl.Network != "" {
		netIn.Network = tpl.Network
	}
	mode, network, policy, err := m.effectiveNet(ctx, actor, netIn)
	if err != nil {
		return store.Sandbox{}, err
	}
	image := in.Image
	if image == "" {
		image = tpl.Image
	}
	if err := validImage(image); err != nil {
		return store.Sandbox{}, err
	}
	cpu := pickFloat(in.CPU, pickFloat(nilIfZero(tpl.CPU), m.cfg.Limits.DefaultCPU))
	mem := pickInt(in.MemoryMB, pickInt(nilIfZeroInt(tpl.MemoryMB), m.cfg.Limits.DefaultMemoryMB))
	disk := pickInt(in.DiskMB, pickInt(nilIfZeroInt(tpl.DiskMB), m.cfg.Limits.DefaultDiskMB))
	pids := pickInt(in.PIDs, m.cfg.Limits.DefaultPIDs)
	ttl := pickInt(in.TTLSeconds, m.cfg.Limits.DefaultTTLSeconds)
	idle := 0
	if in.IdleTimeoutSeconds != nil {
		idle = *in.IdleTimeoutSeconds
	}
	if err := m.checkResources(cpu, mem, disk, pids, ttl, idle); err != nil {
		return store.Sandbox{}, err
	}
	env := map[string]string{}
	for k, v := range tpl.Env {
		env[k] = v
	}
	for k, v := range in.Env {
		env[k] = v
	}
	mounts, err := m.resolveMounts(ctx, actor, backend, in.Mounts)
	if err != nil {
		return store.Sandbox{}, err
	}
	baseCPU := pickFloat(nilIfZero(tpl.CPU), m.cfg.Limits.DefaultCPU)
	baseMem := pickInt(nilIfZeroInt(tpl.MemoryMB), m.cfg.Limits.DefaultMemoryMB)
	baseDisk := pickInt(nilIfZeroInt(tpl.DiskMB), m.cfg.Limits.DefaultDiskMB)
	basePIDs := m.cfg.Limits.DefaultPIDs
	for k := range in.Secrets {
		if _, ok := env[k]; ok {
			return store.Sandbox{}, fmtInvalid("secret %s is also set in env", k)
		}
	}

	now := m.now()
	sb := store.Sandbox{
		ID: id.Sandbox(), Template: in.Template, Image: image, Backend: backend, Status: "creating",
		CPU: cpu, MemoryMB: mem, DiskMB: disk, PIDs: pids, Env: env, Network: mode,
		Allow: policy.Allow, Deny: policy.Deny, EgressKbps: policy.EgressKbps, MaxConns: policy.MaxConns,
		Labels: nonNil(in.Labels), Secrets: nonNil(in.Secrets), OwnerKeyID: actor.KeyID, CreatedAt: now, ExpiresAt: now.Add(time.Duration(ttl) * time.Second),
		IdleTimeoutSeconds: idle, LastActivityAt: now, Mounts: mounts,
	}

	m.mu.Lock()
	n, err := m.store.CountActive(ctx, actor.KeyID)
	if err != nil {
		m.mu.Unlock()
		return store.Sandbox{}, err
	}
	if n >= m.cfg.Limits.MaxSandboxesPerKey {
		m.mu.Unlock()
		return store.Sandbox{}, quota(fmt.Sprintf("sandbox limit of %d reached", m.cfg.Limits.MaxSandboxesPerKey))
	}
	if m.cfg.Limits.MaxSandboxes > 0 {
		total, err := m.store.CountAllActive(ctx)
		if err != nil {
			m.mu.Unlock()
			return store.Sandbox{}, err
		}
		if total >= m.cfg.Limits.MaxSandboxes {
			m.mu.Unlock()
			return store.Sandbox{}, quota(fmt.Sprintf("node sandbox limit of %d reached", m.cfg.Limits.MaxSandboxes))
		}
	}
	claimed, okClaim, err := m.tryClaim(ctx, actor, in, tpl, backend, baseCPU, baseMem, baseDisk, basePIDs, ttl, idle)
	if err != nil {
		m.mu.Unlock()
		return store.Sandbox{}, err
	}
	if okClaim {
		m.mu.Unlock()
		return claimed, nil
	}
	if err := m.store.InsertSandbox(ctx, sb); err != nil {
		m.mu.Unlock()
		return store.Sandbox{}, err
	}
	m.mu.Unlock()

	handle, err := d.Create(ctx, driver.SandboxSpec{
		ID: sb.ID, Image: image, CPU: cpu, MemoryMB: mem, DiskMB: disk, PIDs: pids,
		Env: mergeEnv(env, in.Secrets), Network: network, Policy: policy, Labels: sb.Labels, Workdir: "/workspace",
		Mounts: driverMounts(mounts),
	})
	if err != nil {
		sb.Status = "error"
		sb.Error = err.Error()
		finished := m.now()
		sb.FinishedAt = &finished
		_ = m.store.UpdateSandbox(ctx, sb)
		return store.Sandbox{}, m.asDriver(err)
	}
	sb.ContainerID = handle.ID
	sb.Status = "running"
	if err := m.store.UpdateSandbox(ctx, sb); err != nil {
		_ = d.Delete(context.Background(), handle.ID)
		return store.Sandbox{}, err
	}
	if err := m.runSetup(ctx, d, sb.ContainerID, tpl); err != nil {
		_ = m.destroy(ctx, sb, "setup failed")
		return store.Sandbox{}, err
	}
	m.Counts.Creates.Add(1)
	m.log.Info("sandbox created", "id", sb.ID, "backend", backend, "image", image)
	return sb, nil
}

func (m *Manager) checkResources(cpu float64, mem, disk, pids, ttl, idle int) error {
	lim := m.cfg.Limits
	if cpu <= 0 || cpu > lim.MaxCPU {
		return fmtInvalid("cpu must be between 0 and %v", lim.MaxCPU)
	}
	if mem < 16 || mem > lim.MaxMemoryMB {
		return fmtInvalid("memory_mb must be between 16 and %d", lim.MaxMemoryMB)
	}
	if disk < 16 || disk > lim.MaxDiskMB {
		return fmtInvalid("disk_mb must be between 16 and %d", lim.MaxDiskMB)
	}
	if pids < 8 || pids > lim.MaxPIDs {
		return fmtInvalid("pids must be between 8 and %d", lim.MaxPIDs)
	}
	if ttl < 1 || ttl > lim.MaxTTLSeconds {
		return fmtInvalid("ttl_seconds must be between 1 and %d", lim.MaxTTLSeconds)
	}
	if idle < 0 || (idle > 0 && idle > lim.MaxTTLSeconds) {
		return invalid("idle_timeout_seconds is out of range")
	}
	return nil
}

// Get returns a sandbox visible to actor and counts as activity.
func (m *Manager) Get(ctx context.Context, actor Actor, sandboxID string) (store.Sandbox, error) {
	sb, err := m.visible(ctx, actor, sandboxID)
	if err != nil {
		return store.Sandbox{}, err
	}
	if sb.Status == "running" || sb.Status == "paused" {
		sb.LastActivityAt = m.now()
		_ = m.store.UpdateSandbox(ctx, sb)
	}
	return sb, nil
}

// ListInput filters sandboxes. Label matches are exact. Query is a substring.
type ListInput struct {
	Status   string
	Backend  string
	Template string
	Query    string
	Labels   map[string]string
}

// List returns sandboxes visible to actor.
func (m *Manager) List(ctx context.Context, actor Actor, in ListInput) ([]store.Sandbox, error) {
	if in.Status == "warm" && !actor.Admin {
		return []store.Sandbox{}, nil
	}
	if len(in.Query) > 128 || strings.ContainsRune(in.Query, 0) {
		return nil, invalid("q must be at most 128 characters")
	}
	f := store.SandboxFilter{
		Status: in.Status, Backend: in.Backend, Template: in.Template,
		Query: in.Query, Labels: in.Labels,
	}
	if !actor.Admin {
		f.OwnerID = actor.KeyID
	}
	return m.store.ListSandboxes(ctx, f)
}

// Delete stops a sandbox. Deleting a stopped sandbox is a no-op success.
func (m *Manager) Delete(ctx context.Context, actor Actor, sandboxID string) (store.Sandbox, error) {
	sb, err := m.visible(ctx, actor, sandboxID)
	if err != nil {
		return store.Sandbox{}, err
	}
	if sb.Status == "stopped" || sb.Status == "error" {
		return sb, nil
	}
	if err := m.destroy(ctx, sb, ""); err != nil {
		return store.Sandbox{}, err
	}
	m.Counts.Deletes.Add(1)
	return m.store.GetSandbox(ctx, sandboxID)
}

// SetTTL sets the remaining lifetime measured from now.
func (m *Manager) SetTTL(ctx context.Context, actor Actor, sandboxID string, ttl int) (store.Sandbox, error) {
	if ttl < 1 || ttl > m.cfg.Limits.MaxTTLSeconds {
		return store.Sandbox{}, fmtInvalid("ttl_seconds must be between 1 and %d", m.cfg.Limits.MaxTTLSeconds)
	}
	sb, err := m.alive(ctx, actor, sandboxID)
	if err != nil {
		return store.Sandbox{}, err
	}
	now := m.now()
	sb.ExpiresAt = now.Add(time.Duration(ttl) * time.Second)
	sb.LastActivityAt = now
	if err := m.store.UpdateSandbox(ctx, sb); err != nil {
		return store.Sandbox{}, err
	}
	return sb, nil
}

// Pause pauses a running sandbox when the backend supports it.
func (m *Manager) Pause(ctx context.Context, actor Actor, sandboxID string) (store.Sandbox, error) {
	sb, err := m.alive(ctx, actor, sandboxID)
	if err != nil {
		return store.Sandbox{}, err
	}
	if sb.Status == "paused" {
		return sb, nil
	}
	d, err := m.backend(sb)
	if err != nil {
		return store.Sandbox{}, err
	}
	if !d.Capabilities().Pause {
		return store.Sandbox{}, unsupported(sb.Backend + " does not support pause")
	}
	if err := d.Pause(ctx, sb.ContainerID); err != nil {
		return store.Sandbox{}, m.asDriver(err)
	}
	sb.Status = "paused"
	sb.LastActivityAt = m.now()
	if err := m.store.UpdateSandbox(ctx, sb); err != nil {
		return store.Sandbox{}, err
	}
	return sb, nil
}

// Resume continues a paused sandbox.
func (m *Manager) Resume(ctx context.Context, actor Actor, sandboxID string) (store.Sandbox, error) {
	sb, err := m.visible(ctx, actor, sandboxID)
	if err != nil {
		return store.Sandbox{}, err
	}
	if sb.Status == "running" {
		return sb, nil
	}
	if sb.Status != "paused" {
		return store.Sandbox{}, conflict("sandbox is not paused")
	}
	d, err := m.backend(sb)
	if err != nil {
		return store.Sandbox{}, err
	}
	if err := d.Resume(ctx, sb.ContainerID); err != nil {
		return store.Sandbox{}, m.asDriver(err)
	}
	sb.Status = "running"
	sb.LastActivityAt = m.now()
	if err := m.store.UpdateSandbox(ctx, sb); err != nil {
		return store.Sandbox{}, err
	}
	return sb, nil
}

// Exec runs a command to completion.
func (m *Manager) Exec(ctx context.Context, actor Actor, sandboxID string, in ExecInput) (driver.ExecResult, error) {
	sb, d, err := m.running(ctx, actor, sandboxID)
	if err != nil {
		return driver.ExecResult{}, err
	}
	req, err := m.execReq(in, sb.Secrets)
	if err != nil {
		return driver.ExecResult{}, err
	}
	res, err := d.Exec(ctx, sb.ContainerID, req)
	m.touch(ctx, sb)
	if err != nil {
		m.Counts.ExecErrors.Add(1)
		if errors.Is(err, context.DeadlineExceeded) {
			return driver.ExecResult{}, wrap(504, "timeout", "exec timed out")
		}
		return driver.ExecResult{}, m.asDriver(err)
	}
	m.Counts.Execs.Add(1)
	return res, nil
}

// ExecStream runs a command and emits output chunks.
func (m *Manager) ExecStream(ctx context.Context, actor Actor, sandboxID string, in ExecInput, emit func(driver.ExecChunk) error) (driver.ExecResult, error) {
	sb, d, err := m.running(ctx, actor, sandboxID)
	if err != nil {
		return driver.ExecResult{}, err
	}
	req, err := m.execReq(in, sb.Secrets)
	if err != nil {
		return driver.ExecResult{}, err
	}
	res, err := d.ExecStream(ctx, sb.ContainerID, req, emit)
	m.touch(ctx, sb)
	if err != nil {
		m.Counts.ExecErrors.Add(1)
		if errors.Is(err, context.DeadlineExceeded) {
			return res, wrap(504, "timeout", "exec timed out")
		}
		return res, m.asDriver(err)
	}
	m.Counts.Execs.Add(1)
	return res, nil
}

func (m *Manager) execReq(in ExecInput, secrets map[string]string) (driver.ExecRequest, error) {
	if err := validCmd(in.Cmd); err != nil {
		return driver.ExecRequest{}, err
	}
	if err := validEnv(in.Env); err != nil {
		return driver.ExecRequest{}, err
	}
	cwd := in.Cwd
	if cwd == "" {
		cwd = "/workspace"
	}
	var err error
	cwd, err = cleanPath(cwd)
	if err != nil {
		return driver.ExecRequest{}, err
	}
	timeout := in.TimeoutSeconds
	if timeout == 0 && !in.Background {
		timeout = m.cfg.Limits.DefaultExecSeconds
	}
	if in.Background {
		if timeout < 0 || timeout > m.cfg.Limits.MaxExecSeconds {
			return driver.ExecRequest{}, fmtInvalid("timeout_seconds must be between 0 and %d", m.cfg.Limits.MaxExecSeconds)
		}
	} else if timeout < 1 || timeout > m.cfg.Limits.MaxExecSeconds {
		return driver.ExecRequest{}, fmtInvalid("timeout_seconds must be between 1 and %d", m.cfg.Limits.MaxExecSeconds)
	}
	req := driver.ExecRequest{Cmd: in.Cmd, Cwd: cwd, Env: mergeEnv(secrets, in.Env), TimeoutSeconds: timeout}
	if len(in.Stdin) > 0 {
		req.Stdin = bytes.NewReader(in.Stdin)
	}
	return req, nil
}

// ListFiles lists one directory.
func (m *Manager) ListFiles(ctx context.Context, actor Actor, sandboxID, p string) ([]driver.FileEntry, error) {
	sb, d, p, err := m.fileReady(ctx, actor, sandboxID, p)
	if err != nil {
		return nil, err
	}
	entries, err := d.ListFiles(ctx, sb.ContainerID, p)
	m.touch(ctx, sb)
	m.Counts.FileOps.Add(1)
	if err != nil {
		return nil, m.asDriver(err)
	}
	return entries, nil
}

// ReadFile returns file bytes.
func (m *Manager) ReadFile(ctx context.Context, actor Actor, sandboxID, p string) ([]byte, error) {
	sb, d, p, err := m.fileReady(ctx, actor, sandboxID, p)
	if err != nil {
		return nil, err
	}
	b, err := d.ReadFile(ctx, sb.ContainerID, p, m.cfg.Limits.MaxUploadBytes)
	m.touch(ctx, sb)
	m.Counts.FileOps.Add(1)
	if err != nil {
		return nil, m.asDriver(err)
	}
	return b, nil
}

// WriteFile writes a file, creating parent directories.
func (m *Manager) WriteFile(ctx context.Context, actor Actor, sandboxID, p string, content []byte, mode uint32) error {
	if len(content) > m.cfg.Limits.MaxUploadBytes {
		return invalid("upload exceeds the size limit")
	}
	sb, d, p, err := m.fileReady(ctx, actor, sandboxID, p)
	if err != nil {
		return err
	}
	if err := d.WriteFile(ctx, sb.ContainerID, p, content, mode); err != nil {
		return m.asDriver(err)
	}
	m.touch(ctx, sb)
	m.Counts.FileOps.Add(1)
	return nil
}

// RemoveFile deletes a file or directory inside the sandbox.
func (m *Manager) RemoveFile(ctx context.Context, actor Actor, sandboxID, p string) error {
	sb, d, p, err := m.fileReady(ctx, actor, sandboxID, p)
	if err != nil {
		return err
	}
	if err := d.RemoveFile(ctx, sb.ContainerID, p); err != nil {
		return m.asDriver(err)
	}
	m.touch(ctx, sb)
	m.Counts.FileOps.Add(1)
	return nil
}

// WriteArchive extracts a tar or gzip-compressed tar into dest.
func (m *Manager) WriteArchive(ctx context.Context, actor Actor, sandboxID, dest string, body []byte) error {
	if len(body) > m.cfg.Limits.MaxUploadBytes {
		return invalid("archive exceeds the size limit")
	}
	sb, d, dest, err := m.fileReady(ctx, actor, sandboxID, dest)
	if err != nil {
		return err
	}
	gz := len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b
	raw := body
	if gz {
		r, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return invalid("invalid gzip archive")
		}
		defer r.Close()
		raw, err = io.ReadAll(io.LimitReader(r, int64(m.cfg.Limits.MaxUploadBytes)+1))
		if err != nil {
			return invalid("invalid gzip archive")
		}
		if len(raw) > m.cfg.Limits.MaxUploadBytes {
			return invalid("uncompressed archive exceeds the size limit")
		}
	}
	if arc, ok := d.(driver.Archiver); ok && arc.NativeArchive() {
		if err := arc.ExtractArchive(ctx, sb.ContainerID, dest, bytes.NewReader(raw), int64(m.cfg.Limits.MaxUploadBytes)); err != nil {
			return m.asDriver(err)
		}
		m.touch(ctx, sb)
		m.Counts.FileOps.Add(1)
		return nil
	}
	tmp := "/tmp/sbx-upload.tar"
	if err := d.WriteFile(ctx, sb.ContainerID, tmp, raw, 0o644); err != nil {
		return m.asDriver(err)
	}
	res, err := d.Exec(ctx, sb.ContainerID, driver.ExecRequest{
		Cmd:            []string{"tar", "-xf", tmp, "-C", dest},
		TimeoutSeconds: 60,
	})
	_ = d.RemoveFile(ctx, sb.ContainerID, tmp)
	if err != nil {
		return m.asDriver(err)
	}
	if res.ExitCode != 0 {
		return m.asDriver(fmt.Errorf("%s", string(res.Stderr)))
	}
	m.touch(ctx, sb)
	m.Counts.FileOps.Add(1)
	return nil
}

// ReadArchive returns a gzip-compressed tar of dir.
func (m *Manager) ReadArchive(ctx context.Context, actor Actor, sandboxID, dir string) ([]byte, error) {
	sb, d, dir, err := m.fileReady(ctx, actor, sandboxID, dir)
	if err != nil {
		return nil, err
	}
	if arc, ok := d.(driver.Archiver); ok && arc.NativeArchive() {
		b, err := arc.PackArchive(ctx, sb.ContainerID, dir, m.cfg.Limits.MaxOutputBytes)
		if err != nil {
			return nil, m.asDriver(err)
		}
		m.touch(ctx, sb)
		m.Counts.FileOps.Add(1)
		return b, nil
	}
	res, err := d.Exec(ctx, sb.ContainerID, driver.ExecRequest{
		Cmd:            []string{"tar", "-czf", "-", "-C", dir, "."},
		TimeoutSeconds: 60,
	})
	if err != nil {
		return nil, m.asDriver(err)
	}
	if res.ExitCode != 0 {
		return nil, m.asDriver(fmt.Errorf("%s", string(res.Stderr)))
	}
	if res.Truncated {
		return nil, invalid(fmt.Sprintf("archive exceeds %d bytes", m.cfg.Limits.MaxOutputBytes))
	}
	m.touch(ctx, sb)
	m.Counts.FileOps.Add(1)
	return res.Stdout, nil
}

// CreateTemplate stores a user template.
func (m *Manager) CreateTemplate(ctx context.Context, in TemplateInput) (store.Template, error) {
	if err := validTemplateName(in.Name); err != nil {
		return store.Template{}, err
	}
	if err := validImage(in.Image); err != nil {
		return store.Template{}, err
	}
	if err := validEnv(in.Env); err != nil {
		return store.Template{}, err
	}
	if in.Warm < 0 || in.Warm > 8 {
		return store.Template{}, invalid("warm must be between 0 and 8")
	}
	if len(in.Setup) > 0 {
		if err := validCmd(in.Setup); err != nil {
			return store.Template{}, err
		}
	}
	if in.SetupTimeout < 0 || in.SetupTimeout > m.cfg.Limits.MaxExecSeconds {
		return store.Template{}, invalid("setup_timeout_seconds is out of range")
	}
	network := in.Network
	if network == "" {
		network = "none"
	}
	switch network {
	case "none", "full":
	default:
		return store.Template{}, invalid("template network must be none or full")
	}
	if _, err := m.store.GetTemplate(ctx, in.Name); err == nil {
		return store.Template{}, conflict("template already exists")
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Template{}, err
	}
	setup := in.Setup
	if setup == nil {
		setup = []string{}
	}
	t := store.Template{
		Name: in.Name, Image: in.Image, Description: in.Description,
		CPU: in.CPU, MemoryMB: in.MemoryMB, DiskMB: in.DiskMB, Env: nonNil(in.Env),
		Network: network, Setup: setup, SetupTimeout: in.SetupTimeout, Warm: in.Warm,
		CreatedAt: m.now(),
	}
	if err := m.store.CreateTemplate(ctx, t); err != nil {
		return store.Template{}, err
	}
	if t.Warm > 0 {
		if err := m.fillTemplate(ctx, t); err != nil {
			m.drainPool(ctx, t.Name)
			_ = m.store.DeleteTemplate(ctx, t.Name)
			return store.Template{}, err
		}
	}
	return t, nil
}

// DeleteTemplate removes a user template.
func (m *Manager) DeleteTemplate(ctx context.Context, name string) error {
	err := m.store.DeleteTemplate(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return notFound("template not found")
	}
	if err != nil && err.Error() == "built-in template" {
		return forbidden("built-in templates cannot be deleted")
	}
	if err != nil {
		return err
	}
	m.drainPool(ctx, name)
	return nil
}

// ListTemplates returns every template.
func (m *Manager) ListTemplates(ctx context.Context) ([]store.Template, error) {
	return m.store.ListTemplates(ctx)
}

// Sweep stops expired, idle, and stuck sandboxes, deletes orphan containers,
// and prunes old rows. It returns how many sandboxes were stopped.
func (m *Manager) Sweep(ctx context.Context) (int, error) {
	now := m.now()
	creatingBefore := now.Add(-time.Duration(m.cfg.Limits.CreatingTimeoutSeconds) * time.Second)
	due, err := m.store.ListDue(ctx, now, creatingBefore)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, sb := range due {
		reason := "ttl"
		if sb.Status == "creating" {
			reason = "create timed out"
		} else if sb.IdleTimeoutSeconds > 0 && !sb.LastActivityAt.Add(time.Duration(sb.IdleTimeoutSeconds)*time.Second).After(now) && sb.ExpiresAt.After(now) {
			reason = "idle"
		}
		if err := m.destroy(ctx, sb, reason); err != nil {
			m.log.Error("reap failed", "id", sb.ID, "err", err)
			continue
		}
		n++
		m.Counts.Reaped.Add(1)
	}
	m.reapOrphans(ctx)
	cutoff := now.Add(-time.Duration(m.cfg.Limits.RetainStoppedSeconds) * time.Second)
	if _, err := m.store.PruneFinished(ctx, cutoff); err != nil {
		m.log.Error("prune failed", "err", err)
	}
	return n, nil
}

func (m *Manager) reapOrphans(ctx context.Context) {
	for name, d := range m.drivers {
		list, err := d.List(ctx)
		if err != nil {
			m.log.Warn("driver list failed", "backend", name, "err", err)
			continue
		}
		for _, st := range list {
			if st.SandboxID == "" {
				_ = d.Delete(ctx, st.ID)
				continue
			}
			sb, err := m.store.GetSandbox(ctx, st.SandboxID)
			if errors.Is(err, store.ErrNotFound) || (err == nil && (sb.Status == "stopped" || sb.Status == "error")) {
				if err := d.Delete(ctx, st.ID); err != nil {
					m.log.Warn("orphan delete failed", "id", st.ID, "err", err)
				}
			}
		}
	}
}

// RunReaper sweeps until ctx is canceled. It also refreshes domain egress rules.
func (m *Manager) RunReaper(ctx context.Context) {
	m.RestoreNetworks(ctx)
	interval := time.Duration(m.cfg.Limits.ReaperIntervalSeconds) * time.Second
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.RefreshNetworks(ctx)
			if _, err := m.Sweep(ctx); err != nil {
				m.log.Error("sweep failed", "err", err)
			}
			m.Replenish(ctx)
		}
	}
}

// RestoreNetworks reinstalls egress rules for sandboxes that survived a restart.
func (m *Manager) RestoreNetworks(ctx context.Context) {
	rows, err := m.store.ListSandboxes(ctx, store.SandboxFilter{})
	if err != nil {
		m.log.Warn("network restore failed", "err", err)
		return
	}
	for _, sb := range rows {
		if sb.Status != "running" && sb.Status != "paused" {
			continue
		}
		if sb.Network != "full" && sb.Network != "allow" {
			continue
		}
		m.applyStoredPolicy(ctx, sb)
	}
}

// RefreshNetworks re-resolves domains for policies this process installed.
func (m *Manager) RefreshNetworks(ctx context.Context) {
	for name, d := range m.drivers {
		ref, ok := d.(driver.NetEnforcer)
		if !ok {
			continue
		}
		if err := ref.RefreshNetPolicies(ctx); err != nil {
			m.log.Warn("network refresh failed", "backend", name, "err", err)
		}
	}
}

func (m *Manager) applyStoredPolicy(ctx context.Context, sb store.Sandbox) {
	d, ok := m.drivers[sb.Backend]
	if !ok || sb.ContainerID == "" {
		return
	}
	ref, ok := d.(driver.NetEnforcer)
	if !ok {
		return
	}
	spec := driver.SandboxSpec{
		ID: sb.ID, Network: driver.NetFull,
		Policy: driver.NetPolicy{
			Allow: sb.Allow, Deny: sb.Deny,
			BlockPrivate: m.cfg.Network.BlockPrivate, BlockMetadata: m.cfg.Network.BlockMetadata,
			PrivateAllow: append([]string{}, m.cfg.Network.PrivateAllow...),
			DNS:          append([]string{}, m.cfg.Network.DNS...),
			EgressKbps:   sb.EgressKbps, MaxConns: sb.MaxConns,
		},
	}
	if err := ref.ApplyNetPolicy(ctx, sb.ContainerID, spec); err != nil {
		m.log.Warn("network restore failed", "id", sb.ID, "err", err)
	}
}

func (m *Manager) destroy(ctx context.Context, sb store.Sandbox, reason string) error {
	m.mu.Lock()
	cur, err := m.store.GetSandbox(ctx, sb.ID)
	if errors.Is(err, store.ErrNotFound) {
		m.mu.Unlock()
		return nil
	}
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if cur.Status == "stopped" || cur.Status == "error" {
		m.mu.Unlock()
		return nil
	}
	cur.Status = "stopped"
	if reason != "" {
		cur.Error = reason
	}
	finished := m.now()
	cur.FinishedAt = &finished
	if err := m.store.UpdateSandbox(ctx, cur); err != nil {
		m.mu.Unlock()
		return err
	}
	containerID := cur.ContainerID
	backend := cur.Backend
	m.dropPreviews(cur.ID)
	m.mu.Unlock()

	if containerID != "" {
		if d, ok := m.drivers[backend]; ok {
			if err := d.Delete(ctx, containerID); err != nil && !errors.Is(err, driver.ErrNotFound) {
				m.log.Warn("container delete failed", "id", sb.ID, "err", err)
			}
		}
	}
	m.log.Info("sandbox stopped", "id", sb.ID, "reason", reason)
	return nil
}

func (m *Manager) visible(ctx context.Context, actor Actor, sandboxID string) (store.Sandbox, error) {
	sb, err := m.store.GetSandbox(ctx, sandboxID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Sandbox{}, notFound("sandbox not found")
	}
	if err != nil {
		return store.Sandbox{}, err
	}
	if !actor.Admin && sb.OwnerKeyID != actor.KeyID {
		return store.Sandbox{}, notFound("sandbox not found")
	}
	return sb, nil
}

func (m *Manager) alive(ctx context.Context, actor Actor, sandboxID string) (store.Sandbox, error) {
	sb, err := m.visible(ctx, actor, sandboxID)
	if err != nil {
		return store.Sandbox{}, err
	}
	if sb.Status != "running" && sb.Status != "paused" {
		return store.Sandbox{}, conflict("sandbox is not running")
	}
	return sb, nil
}

func (m *Manager) running(ctx context.Context, actor Actor, sandboxID string) (store.Sandbox, driver.Driver, error) {
	sb, err := m.visible(ctx, actor, sandboxID)
	if err != nil {
		return store.Sandbox{}, nil, err
	}
	if sb.Status == "paused" {
		return store.Sandbox{}, nil, conflict("sandbox is paused")
	}
	if sb.Status != "running" {
		return store.Sandbox{}, nil, conflict("sandbox is not running")
	}
	d, err := m.backend(sb)
	if err != nil {
		return store.Sandbox{}, nil, err
	}
	return sb, d, nil
}

func (m *Manager) fileReady(ctx context.Context, actor Actor, sandboxID, p string) (store.Sandbox, driver.Driver, string, error) {
	sb, d, err := m.running(ctx, actor, sandboxID)
	if err != nil {
		return store.Sandbox{}, nil, "", err
	}
	p, err = cleanPath(p)
	if err != nil {
		return store.Sandbox{}, nil, "", err
	}
	return sb, d, p, nil
}

func (m *Manager) backend(sb store.Sandbox) (driver.Driver, error) {
	d, ok := m.drivers[sb.Backend]
	if !ok {
		return nil, invalid("backend " + sb.Backend + " is not enabled")
	}
	return d, nil
}

func (m *Manager) touch(ctx context.Context, sb store.Sandbox) {
	sb.LastActivityAt = m.now()
	_ = m.store.UpdateSandbox(ctx, sb)
}

func (m *Manager) asDriver(err error) error {
	if err == nil {
		return nil
	}
	var op *OpError
	if errors.As(err, &op) {
		return err
	}
	if errors.Is(err, driver.ErrRuntimeUnavailable) {
		return unsupported(err.Error())
	}
	if errors.Is(err, driver.ErrImage) {
		return invalid(err.Error())
	}
	if errors.Is(err, driver.ErrNotFound) {
		return notFound("sandbox container is gone")
	}
	return wrap(502, "driver", err.Error())
}

func pickFloat(v *float64, fallback float64) float64 {
	if v == nil {
		return fallback
	}
	return *v
}

func pickInt(v *int, fallback int) int {
	if v == nil {
		return fallback
	}
	return *v
}

func nilIfZero(v float64) *float64 {
	if v == 0 {
		return nil
	}
	return &v
}

func nilIfZeroInt(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func mergeEnv(base, over map[string]string) map[string]string {
	if len(base) == 0 && len(over) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// SetSecrets replaces the secret environment for a running sandbox.
// Values are not returned by Get or List. Later execs see the new values.
func (m *Manager) SetSecrets(ctx context.Context, actor Actor, sandboxID string, secrets map[string]string) (store.Sandbox, error) {
	if err := validEnv(secrets); err != nil {
		return store.Sandbox{}, err
	}
	sb, err := m.visible(ctx, actor, sandboxID)
	if err != nil {
		return store.Sandbox{}, err
	}
	if sb.Status != "running" && sb.Status != "paused" {
		return store.Sandbox{}, invalid("sandbox is not running")
	}
	for k := range secrets {
		if _, ok := sb.Env[k]; ok {
			return store.Sandbox{}, fmtInvalid("secret %s is also set in env", k)
		}
	}
	secrets = nonNil(secrets)
	if err := m.store.UpdateSecrets(ctx, sb.ID, secrets); err != nil {
		return store.Sandbox{}, err
	}
	sb.Secrets = secrets
	return sb, nil
}
