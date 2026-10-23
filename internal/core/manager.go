// Package core owns sandbox lifecycle: quotas, templates, TTL, and which
// driver a request is sent to.
package core

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
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
	// Steps are build steps run after the template's own steps. They need
	// an admin key unless builds.allow_sandbox_keys is set.
	Steps []store.BuildStep
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
	Backend      string
	Network      string
	Setup        []string
	SetupTimeout int
	Warm         int
	Steps        []store.BuildStep
}

// Counters are process-local totals for /metrics.
type Counters struct {
	Creates    atomic.Int64
	Deletes    atomic.Int64
	Execs      atomic.Int64
	ExecErrors atomic.Int64
	FileOps    atomic.Int64
	Reaped     atomic.Int64
	Builds     atomic.Int64
	ImagesGCed atomic.Int64
}

// Manager is safe for concurrent use.
type Manager struct {
	cfg      config.Config
	store    *store.Store
	drivers  map[string]driver.Driver
	log      *slog.Logger
	now      func() time.Time
	mu       sync.Mutex
	poolMu   sync.Mutex
	previews map[string]Preview
	streams  map[string]int
	budgets  map[string]*byteBudget
	ready    atomic.Bool
	Counts   Counters
	// buildLocks holds one mutex per build tag so concurrent creates build once.
	buildLocks sync.Map
	// evWake is closed and replaced on every lifecycle event. evStreams
	// counts open event streams per key.
	evMu      sync.Mutex
	evWake    chan struct{}
	evStreams map[string]int
}

// New builds a manager. drivers is keyed by backend name ("docker", "gvisor").
func New(cfg config.Config, st *store.Store, drivers map[string]driver.Driver, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		cfg: cfg, store: st, drivers: drivers, log: log,
		now:       func() time.Time { return time.Now().UTC() },
		previews:  map[string]Preview{},
		evWake:    make(chan struct{}),
		evStreams: map[string]int{},
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

// ListBackends reports every ladder rung. A missing driver is unavailable,
// not an error, so clients can show what this host can run.
func (m *Manager) ListBackends(ctx context.Context) []driver.Info {
	seen := map[string]bool{}
	var out []driver.Info
	for _, name := range driver.Known() {
		seen[name] = true
		out = append(out, m.backendInfo(ctx, name))
	}
	extra := make([]string, 0)
	for name := range m.drivers {
		if !seen[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		out = append(out, m.backendInfo(ctx, name))
	}
	return out
}

func (m *Manager) backendInfo(ctx context.Context, name string) driver.Info {
	d, ok := m.drivers[name]
	if !ok {
		return driver.Info{
			Name: name, Available: false, Detail: "not enabled on this server",
			Networks: []string{}, Requirements: []string{},
		}
	}
	if r, ok := d.(driver.Reporter); ok {
		info := r.Report(ctx)
		if info.Name == "" {
			info.Name = name
		}
		if info.Networks == nil {
			info.Networks = []string{}
		}
		if info.Requirements == nil {
			info.Requirements = []string{}
		}
		return info
	}
	err := d.Ping(ctx)
	detail := "ready"
	if err != nil {
		detail = err.Error()
	}
	return driver.Describe(name, d.Capabilities(), err == nil, detail)
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
		if tpl, err = m.template(ctx, in.Template); err != nil {
			return store.Sandbox{}, err
		}
	}
	// Template steps on a caller-chosen base are a new build too.
	newBase := len(tpl.Steps) > 0 && in.Image != "" && in.Image != tpl.Image
	if len(in.Steps) > 0 || newBase {
		if err := m.canBuild(actor); err != nil {
			return store.Sandbox{}, err
		}
		if err := validSteps(joinSteps(tpl.Steps, in.Steps)); err != nil {
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
	backend := in.Backend
	if backend == "" {
		backend = tpl.Backend
	}
	if backend == "" {
		backend = m.cfg.Backend
	}
	d, ok := m.drivers[backend]
	if !ok {
		return store.Sandbox{}, invalid("unknown backend " + backend)
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
	if image, _, err = m.buildImage(ctx, backend, image, joinSteps(tpl.Steps, in.Steps)); err != nil {
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
	// A claim turns a pooled sandbox into the caller's without adding a
	// container, so it is tried before the node cap, which counts the pool.
	claimed, okClaim, err := m.tryClaim(ctx, actor, in, tpl, backend, mode, policy, baseCPU, baseMem, baseDisk, basePIDs, ttl, idle)
	if err != nil {
		m.mu.Unlock()
		return store.Sandbox{}, err
	}
	if okClaim {
		m.mu.Unlock()
		return claimed, nil
	}
	if err := m.checkNodeCap(ctx); err != nil {
		m.mu.Unlock()
		return store.Sandbox{}, err
	}
	if err := m.mountsStillExist(ctx, mounts); err != nil {
		m.mu.Unlock()
		return store.Sandbox{}, err
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
		if m.store.UpdateSandboxFrom(ctx, sb, "creating") == nil {
			m.emit(ctx, EventFailed, sb, map[string]any{"reason": "create failed"})
		}
		return store.Sandbox{}, m.asDriver(err)
	}
	sb.ContainerID = handle.ID
	sb.Status = "running"
	if err := m.store.UpdateSandboxFrom(ctx, sb, "creating"); err != nil {
		_ = d.Delete(context.Background(), handle.ID)
		if errors.Is(err, store.ErrNotFound) {
			return store.Sandbox{}, conflict("sandbox was stopped while it was being created")
		}
		return store.Sandbox{}, err
	}
	if err := m.runSetup(ctx, d, sb.ContainerID, tpl); err != nil {
		_ = m.destroy(ctx, sb, "setup failed")
		return store.Sandbox{}, err
	}
	m.Counts.Creates.Add(1)
	m.log.Info("sandbox created", "id", sb.ID, "backend", backend, "image", image)
	m.emit(ctx, EventCreated, sb, map[string]any{"warm": false})
	return sb, nil
}

// checkNodeCap enforces max_sandboxes over every live container, warm pool
// included.
func (m *Manager) checkNodeCap(ctx context.Context) error {
	if m.cfg.Limits.MaxSandboxes <= 0 {
		return nil
	}
	total, err := m.store.CountAllActive(ctx)
	if err != nil {
		return err
	}
	if total >= m.cfg.Limits.MaxSandboxes {
		return quota(fmt.Sprintf("node sandbox limit of %d reached", m.cfg.Limits.MaxSandboxes))
	}
	return nil
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
		_ = m.store.TouchSandbox(ctx, sb.ID, sb.LastActivityAt)
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
	if err := m.store.UpdateSandboxFrom(ctx, sb, "running", "paused"); err != nil {
		return store.Sandbox{}, stale(err)
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
	if err := m.store.UpdateSandboxFrom(ctx, sb, "running"); err != nil {
		return store.Sandbox{}, stale(err)
	}
	m.emit(ctx, EventPaused, sb, nil)
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
	if err := m.store.UpdateSandboxFrom(ctx, sb, "paused"); err != nil {
		return store.Sandbox{}, stale(err)
	}
	m.emit(ctx, EventResumed, sb, nil)
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

// WriteArchive extracts a tar or gzip-compressed tar into dest. With a
// native archiver the body streams through to the sandbox, so the server
// never holds the whole archive; max_archive_bytes caps both the body and the
// tar after gunzip.
func (m *Manager) WriteArchive(ctx context.Context, actor Actor, sandboxID, dest string, body io.Reader) error {
	sb, d, dest, err := m.fileReady(ctx, actor, sandboxID, dest)
	if err != nil {
		return err
	}
	limit := m.cfg.Limits.MaxArchiveBytes
	in := &capReader{r: body, left: int64(limit)}
	br := bufio.NewReader(in)
	magic, _ := br.Peek(2)
	var tarStream io.Reader = br
	var unzipped *capReader
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return archiveErr(limit, in, nil, invalid("invalid gzip archive"))
		}
		defer zr.Close()
		unzipped = &capReader{r: zr, left: int64(limit)}
		tarStream = unzipped
	}
	if arc, ok := d.(driver.Archiver); ok && arc.NativeArchive() {
		if err := arc.ExtractArchive(ctx, sb.ContainerID, dest, tarStream, int64(limit)); err != nil {
			return archiveErr(limit, in, unzipped, m.asDriver(err))
		}
		m.touch(ctx, sb)
		m.Counts.FileOps.Add(1)
		return nil
	}
	raw, err := io.ReadAll(tarStream)
	if err != nil {
		return archiveErr(limit, in, unzipped, invalid("could not read archive"))
	}
	// A fixed name let two concurrent uploads to one sandbox overwrite each
	// other's archive before tar read it.
	tmp := "/tmp/sbx-upload-" + strings.TrimPrefix(id.RecordID(), "key_") + ".tar"
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

// archiveErr names the real cause of a failed archive upload. A streamed
// archive surfaces a limit or gzip error as whatever the reader's consumer
// made of it, so the readers record what they saw. fallback is used when
// neither did.
func archiveErr(max int, body, unzipped *capReader, fallback error) error {
	limit := FormatBytes(max)
	switch {
	case body.over:
		return tooLarge("archive is larger than the " + limit + " limit (limits.max_archive_bytes, SBX_MAX_ARCHIVE_BYTES)")
	case body.err != nil:
		return invalid("could not read archive")
	case unzipped != nil && unzipped.over:
		return tooLarge("archive is larger than the " + limit + " limit after gunzip (limits.max_archive_bytes, SBX_MAX_ARCHIVE_BYTES)")
	case unzipped != nil && unzipped.err != nil:
		return invalid("invalid gzip archive")
	}
	return fallback
}

var errArchiveTooLarge = errors.New("archive is over max_archive_bytes")

// capReader fails once more than its budget has been read, and records
// whether that happened or the underlying reader failed.
type capReader struct {
	r    io.Reader
	left int64
	over bool
	err  error
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.over {
		return 0, errArchiveTooLarge
	}
	if int64(len(p)) > c.left+1 {
		p = p[:c.left+1]
	}
	n, err := c.r.Read(p)
	if int64(n) > c.left {
		c.over = true
		n = int(c.left)
		c.left = 0
		return n, errArchiveTooLarge
	}
	c.left -= int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		c.err = err
	}
	return n, err
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
	if err := validSteps(in.Steps); err != nil {
		return store.Template{}, err
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
	if in.Backend != "" {
		if _, ok := m.drivers[in.Backend]; !ok {
			return store.Template{}, invalid("unknown backend " + in.Backend)
		}
	}
	setup := in.Setup
	if setup == nil {
		setup = []string{}
	}
	backend := in.Backend
	if backend == "" {
		backend = m.cfg.Backend
	}
	// Build now so a broken step fails the template, not the first create.
	if _, _, err := m.buildImage(ctx, backend, in.Image, in.Steps); err != nil {
		return store.Template{}, err
	}
	t := store.Template{
		Name: in.Name, Image: in.Image, Description: in.Description,
		CPU: in.CPU, MemoryMB: in.MemoryMB, DiskMB: in.DiskMB, Env: nonNil(in.Env),
		Backend: in.Backend, Network: network, Setup: setup, SetupTimeout: in.SetupTimeout, Warm: in.Warm,
		Steps: in.Steps, CreatedAt: m.now(),
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
	m.checkLiveness(ctx)
	m.reapOrphans(ctx)
	cutoff := now.Add(-time.Duration(m.cfg.Limits.RetainStoppedSeconds) * time.Second)
	if _, err := m.store.PruneFinished(ctx, cutoff); err != nil {
		m.log.Error("prune failed", "err", err)
	}
	if _, err := m.store.PruneEvents(ctx, cutoff); err != nil {
		m.log.Error("event prune failed", "err", err)
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

// RunReaper sweeps until ctx is canceled. It also refreshes domain egress
// rules and follows backend runtime events.
func (m *Manager) RunReaper(ctx context.Context) {
	m.RestoreNetworks(ctx)
	m.WatchDrivers(ctx)
	interval := time.Duration(m.cfg.Limits.ReaperIntervalSeconds) * time.Second
	t := time.NewTicker(interval)
	defer t.Stop()
	var lastGC time.Time
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
			if now := m.now(); now.Sub(lastGC) >= imageGCInterval {
				lastGC = now
				if _, err := m.CollectImages(ctx); err != nil {
					m.log.Error("image collection failed", "err", err)
				}
			}
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
	m.dropPreviews(ctx, cur.ID)
	m.mu.Unlock()

	if containerID != "" {
		if d, ok := m.drivers[backend]; ok {
			if err := d.Delete(ctx, containerID); err != nil && !errors.Is(err, driver.ErrNotFound) {
				m.log.Warn("container delete failed", "id", sb.ID, "err", err)
			}
		}
	}
	m.log.Info("sandbox stopped", "id", sb.ID, "reason", reason)
	typ, data := stopEvent(reason)
	m.emit(ctx, typ, cur, data)
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

// touch records activity without writing back the rest of sb, which may be
// stale after a long operation (a concurrent delete must stay deleted).
func (m *Manager) touch(ctx context.Context, sb store.Sandbox) {
	_ = m.store.TouchSandbox(ctx, sb.ID, m.now())
}

// stale maps a conditional update that matched no row to a conflict: the
// sandbox changed state while the request was in flight.
func stale(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return conflict("sandbox is not running")
	}
	return err
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
	if errors.Is(err, driver.ErrConfig) {
		return invalid(err.Error())
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
