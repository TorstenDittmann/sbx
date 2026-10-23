// Package fake is an in-memory Driver for tests.
package fake

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

// Driver stores sandboxes and files in memory.
type Driver struct {
	mu           sync.Mutex
	name         string
	caps         driver.Caps
	boxes        map[string]*box
	files        map[string]map[string][]byte
	dirs         map[string]map[string]struct{}
	procs        map[string]map[string]*proc
	watchers     map[string][]*watch
	volumes      map[string]struct{}
	seq          int
	PingErr      error
	CreateErr    error
	ListErr      error
	Deleted      []string
	TruncateExec bool
	ExecCode     int
	execs        []ExecCall
	// NoGuest makes the optional agent surface report itself disabled.
	NoGuest bool
	// DialErr is returned by DialPort when set.
	DialErr error
	// DropDial accepts a preview connection and closes it without a response.
	DropDial bool
	// StatsErr fails Stats when set.
	StatsErr error
	// BuildErr fails BuildImage when set.
	BuildErr error
	builds   []driver.BuildSpec
	images   map[string]time.Time
	// SnapshotErr fails Snapshot when set.
	SnapshotErr error
	snapshots   []driver.SnapshotSpec
	// seeds holds the /workspace files a snapshot image starts with.
	seeds  map[string]map[string][]byte
	events chan driver.Event
	// Registry, when set, stands in for the registry on every pull: a forced
	// pull, a build's base, and PullImage. A nil return lets the pull through.
	Registry func(ref string, auth *driver.RegistryAuth) error
	pulls    []PullCall
}

// PullCall is one pull that reached the fake registry.
type PullCall struct {
	Ref  string
	Auth *driver.RegistryAuth
}

type proc struct {
	info  driver.Process
	stdin []byte
	cols  int
	rows  int
	exit  int
	done  chan struct{}
	once  sync.Once
}

type watch struct {
	path      string
	recursive bool
	ch        chan driver.FileEvent
}

type box struct {
	spec  driver.SandboxSpec
	state driver.State
	image string
	oom   bool
	exit  int
}

// New returns a docker-like fake that supports pause.
func New(name string) *Driver {
	if name == "" {
		name = "docker"
	}
	level := "container"
	if name == "gvisor" {
		level = "gvisor"
	}
	return &Driver{
		name: name,
		caps: driver.Caps{
			Pause: true, Snapshot: name != "gvisor", NetPolicy: true, IsolationLevel: level,
			Exec: true, Files: true, PTY: true, Watch: true, Preview: true,
			Networks:     []driver.NetMode{driver.NetNone, driver.NetFull},
			Requirements: []string{"docker"},
		},
		boxes:    map[string]*box{},
		files:    map[string]map[string][]byte{},
		dirs:     map[string]map[string]struct{}{},
		procs:    map[string]map[string]*proc{},
		watchers: map[string][]*watch{},
		volumes:  map[string]struct{}{},
		images:   map[string]time.Time{},
		seeds:    map[string]map[string][]byte{},
		events:   make(chan driver.Event, 16),
	}
}

// Crash stops a container the way a dying main process would. oom marks it
// OOM-killed.
func (d *Driver) Crash(id string, oom bool, exitCode int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if b, ok := d.boxes[id]; ok {
		b.state = driver.StateStopped
		b.oom = oom
		b.exit = exitCode
	}
}

// Vanish removes a container behind the manager's back.
func (d *Driver) Vanish(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.boxes, id)
}

// Push queues a runtime event for WatchEvents.
func (d *Driver) Push(ev driver.Event) { d.events <- ev }

// WatchEvents delivers pushed events until ctx ends.
func (d *Driver) WatchEvents(ctx context.Context, emit func(driver.Event)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-d.events:
			emit(ev)
		}
	}
}

// SetPause toggles the pause capability.
func (d *Driver) SetPause(ok bool) {
	d.mu.Lock()
	d.caps.Pause = ok
	d.mu.Unlock()
}

// Seed adds a container the manager did not create, for orphan reconciliation.
func (d *Driver) Seed(st driver.Status) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.boxes[st.ID] = &box{spec: driver.SandboxSpec{ID: st.SandboxID, Image: st.Image}, state: st.State, image: st.Image}
}

var _ driver.Driver = (*Driver)(nil)

func (d *Driver) Name() string { return d.name }

func (d *Driver) Capabilities() driver.Caps {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.caps
}

// Report describes this fake. PingErr marks it unavailable.
func (d *Driver) Report(ctx context.Context) driver.Info {
	err := d.Ping(ctx)
	detail := "ready"
	if err != nil {
		detail = err.Error()
	}
	return driver.Describe(d.Name(), d.Capabilities(), err == nil, detail)
}

func (d *Driver) Ping(context.Context) error { return d.PingErr }

func (d *Driver) EnsureImage(context.Context, string) error { return nil }

var _ driver.Builder = (*Driver)(nil)

// HasImage reports whether BuildImage produced ref or a pull fetched it.
func (d *Driver) HasImage(_ context.Context, ref string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.images[ref]
	return ok, nil
}

// BuildImage records spec and marks its tag as present.
func (d *Driver) BuildImage(_ context.Context, spec driver.BuildSpec) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.builds = append(d.builds, spec)
	if d.BuildErr != nil {
		return d.BuildErr
	}
	if err := d.pullLocked(spec.Base, spec.Registry); err != nil {
		return err
	}
	d.images[spec.Tag] = time.Now().UTC()
	return nil
}

var _ driver.ImageCollector = (*Driver)(nil)

// AddImage marks ref as built at created.
func (d *Driver) AddImage(ref string, created time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.images[ref] = created
}

// ListBuiltImages returns the built images in repo.
func (d *Driver) ListBuiltImages(_ context.Context, repo string) ([]driver.BuiltImage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []driver.BuiltImage
	for ref, created := range d.images {
		if strings.HasPrefix(ref, repo+":") {
			out = append(out, driver.BuiltImage{Ref: ref, Created: created})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, nil
}

// RemoveImage forgets ref unless a sandbox that still exists uses it.
func (d *Driver) RemoveImage(_ context.Context, ref string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, b := range d.boxes {
		if b.image == ref {
			return driver.ErrImageInUse
		}
	}
	delete(d.images, ref)
	return nil
}

// PullImage asks the fake registry for ref.
func (d *Driver) PullImage(_ context.Context, ref string, auth *driver.RegistryAuth) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pullLocked(ref, auth)
}

func (d *Driver) pullLocked(ref string, auth *driver.RegistryAuth) error {
	d.pulls = append(d.pulls, PullCall{Ref: ref, Auth: auth})
	if d.Registry != nil {
		if err := d.Registry(ref, auth); err != nil {
			return fmt.Errorf("%w: %s", driver.ErrImage, err.Error())
		}
	}
	d.images[ref] = time.Now().UTC()
	return nil
}

// Pulls returns every pull that reached the fake registry.
func (d *Driver) Pulls() []PullCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]PullCall(nil), d.pulls...)
}

// Builds returns every BuildImage call so far.
func (d *Driver) Builds() []driver.BuildSpec {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]driver.BuildSpec(nil), d.builds...)
}

func (d *Driver) Create(_ context.Context, spec driver.SandboxSpec) (driver.Handle, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.CreateErr != nil {
		return driver.Handle{}, d.CreateErr
	}
	if spec.ForcePull || spec.Registry != nil {
		if err := d.pullLocked(spec.Image, spec.Registry); err != nil {
			return driver.Handle{}, err
		}
	}
	d.boxes[spec.ID] = &box{spec: spec, state: driver.StateRunning, image: spec.Image}
	if d.files[spec.ID] == nil {
		d.files[spec.ID] = map[string][]byte{}
	}
	for p, content := range d.seeds[spec.Image] {
		d.files[spec.ID][p] = append([]byte(nil), content...)
	}
	return driver.Handle{ID: spec.ID, RuntimeName: d.name}, nil
}

var _ driver.Snapshotter = (*Driver)(nil)

// Snapshot records spec, marks its tag as present, and keeps the sandbox's
// /workspace files for sandboxes created from the tag.
func (d *Driver) Snapshot(_ context.Context, id string, spec driver.SnapshotSpec) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.snapshots = append(d.snapshots, spec)
	if d.SnapshotErr != nil {
		return d.SnapshotErr
	}
	if _, ok := d.boxes[id]; !ok {
		return driver.ErrNotFound
	}
	seed := map[string][]byte{}
	if spec.Workspace {
		for p, content := range d.files[id] {
			if strings.HasPrefix(p, "/workspace/") {
				seed[p] = append([]byte(nil), content...)
			}
		}
	}
	d.seeds[spec.Tag] = seed
	d.images[spec.Tag] = time.Now().UTC()
	return nil
}

// Snapshots returns every Snapshot call so far.
func (d *Driver) Snapshots() []driver.SnapshotSpec {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]driver.SnapshotSpec(nil), d.snapshots...)
}

// CreateVolume records a named volume.
func (d *Driver) CreateVolume(_ context.Context, name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.volumes[name]; ok {
		return fmt.Errorf("volume %s already exists", name)
	}
	d.volumes[name] = struct{}{}
	return nil
}

// DeleteVolume removes a recorded volume.
func (d *Driver) DeleteVolume(_ context.Context, name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.volumes, name)
	return nil
}

// HasVolume reports whether CreateVolume recorded name.
func (d *Driver) HasVolume(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.volumes[name]
	return ok
}

// Spec returns the spec recorded for a created sandbox.
func (d *Driver) Spec(id string) (driver.SandboxSpec, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.boxes[id]
	if !ok {
		return driver.SandboxSpec{}, false
	}
	return b.spec, true
}

func (d *Driver) Delete(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Deleted = append(d.Deleted, id)
	delete(d.boxes, id)
	delete(d.files, id)
	delete(d.dirs, id)
	delete(d.procs, id)
	delete(d.watchers, id)
	return nil
}

func (d *Driver) Inspect(_ context.Context, id string) (driver.Status, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.boxes[id]
	if !ok {
		return driver.Status{}, driver.ErrNotFound
	}
	return d.status(id, b), nil
}

func (d *Driver) List(context.Context) ([]driver.Status, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ListErr != nil {
		return nil, d.ListErr
	}
	out := make([]driver.Status, 0, len(d.boxes))
	for id, b := range d.boxes {
		out = append(out, d.status(id, b))
	}
	return out, nil
}

func (d *Driver) status(id string, b *box) driver.Status {
	return driver.Status{ID: id, SandboxID: b.spec.ID, State: b.state, Image: b.image, OOMKilled: b.oom, ExitCode: b.exit}
}

func (d *Driver) Pause(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.caps.Pause {
		return fmt.Errorf("pause is not supported")
	}
	b, ok := d.boxes[id]
	if !ok {
		return driver.ErrNotFound
	}
	b.state = driver.StatePaused
	return nil
}

func (d *Driver) Resume(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.boxes[id]
	if !ok {
		return driver.ErrNotFound
	}
	b.state = driver.StateRunning
	return nil
}

// Stats reports fixed CPU and network numbers. Memory and disk follow the
// spec limits and the bytes written to the sandbox's files.
func (d *Driver) Stats(_ context.Context, id string, full bool) (driver.Stats, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.StatsErr != nil {
		return driver.Stats{}, d.StatsErr
	}
	b, ok := d.boxes[id]
	if !ok {
		return driver.Stats{}, driver.ErrNotFound
	}
	var used int64
	for _, content := range d.files[id] {
		used += int64(len(content))
	}
	st := driver.Stats{
		CPUSeconds:       1.5,
		MemoryBytes:      32 << 20,
		MemoryLimitBytes: int64(b.spec.MemoryMB) << 20,
		DiskUsedBytes:    -1,
		DiskTotalBytes:   -1,
		NetRxBytes:       1024,
		NetTxBytes:       2048,
		PIDs:             3,
	}
	if full {
		pct := 12.5
		st.CPUPercent = &pct
		st.DiskUsedBytes = used
		st.DiskTotalBytes = int64(b.spec.DiskMB) << 20
	}
	return st, nil
}

// ExecCall is one recorded exec.
type ExecCall struct {
	Cmd []string
	Env map[string]string
}

// Execs returns commands the fake has run.
func (d *Driver) Execs() []ExecCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]ExecCall, len(d.execs))
	copy(out, d.execs)
	return out
}

func (d *Driver) Exec(_ context.Context, id string, req driver.ExecRequest) (driver.ExecResult, error) {
	return d.run(id, req, nil)
}

func (d *Driver) ExecStream(_ context.Context, id string, req driver.ExecRequest, emit func(driver.ExecChunk) error) (driver.ExecResult, error) {
	return d.run(id, req, emit)
}

func (d *Driver) run(id string, req driver.ExecRequest, emit func(driver.ExecChunk) error) (driver.ExecResult, error) {
	d.mu.Lock()
	b, ok := d.boxes[id]
	truncate := d.TruncateExec
	if ok {
		env := map[string]string{}
		for k, v := range req.Env {
			env[k] = v
		}
		d.execs = append(d.execs, ExecCall{Cmd: append([]string(nil), req.Cmd...), Env: env})
	}
	code := d.ExecCode
	d.mu.Unlock()
	if code != 0 {
		return driver.ExecResult{ExitCode: code, Stderr: []byte("failed\n")}, nil
	}
	if !ok || b.state == driver.StateStopped {
		return driver.ExecResult{}, driver.ErrNotFound
	}
	start := time.Now()
	stdout := []byte("ok\n")
	if len(req.Cmd) > 0 && req.Cmd[0] == "echo" {
		stdout = []byte(strings.Join(req.Cmd[1:], " ") + "\n")
	}
	if req.Stdin != nil {
		b, err := io.ReadAll(req.Stdin)
		if err != nil {
			return driver.ExecResult{}, err
		}
		if len(req.Cmd) > 0 && req.Cmd[0] == "cat" {
			stdout = b
		}
	}
	if emit != nil && len(stdout) > 0 {
		if err := emit(driver.ExecChunk{Kind: driver.StreamStdout, Data: stdout}); err != nil {
			return driver.ExecResult{}, err
		}
		stdout = nil
	}
	return driver.ExecResult{ExitCode: 0, Stdout: stdout, Duration: time.Since(start), Truncated: truncate}, nil
}

func (d *Driver) ListFiles(_ context.Context, id, dir string) ([]driver.FileEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.boxes[id]; !ok {
		return nil, driver.ErrNotFound
	}
	dir = strings.TrimRight(dir, "/")
	var entries []driver.FileEntry
	seen := map[string]bool{}
	for p, content := range d.files[id] {
		if !strings.HasPrefix(p, dir+"/") {
			continue
		}
		rest := strings.TrimPrefix(p, dir+"/")
		name, _, more := strings.Cut(rest, "/")
		if seen[name] {
			continue
		}
		seen[name] = true
		full := dir + "/" + name
		if more {
			entries = append(entries, driver.FileEntry{Name: name, Path: full, Type: driver.FileDir, Mode: 0o755})
			continue
		}
		entries = append(entries, driver.FileEntry{Name: name, Path: full, Type: driver.FileFile, Size: int64(len(content)), Mode: 0o644})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

func (d *Driver) ReadFile(_ context.Context, id, path string, max int) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.boxes[id]; !ok {
		return nil, driver.ErrNotFound
	}
	content, ok := d.files[id][path]
	if !ok {
		return nil, fmt.Errorf("no such file")
	}
	if max > 0 && len(content) > max {
		return content[:max], fmt.Errorf("file exceeds %d bytes", max)
	}
	out := make([]byte, len(content))
	copy(out, content)
	return out, nil
}

func (d *Driver) WriteFile(_ context.Context, id, path string, content []byte, _ uint32) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.boxes[id]; !ok {
		return driver.ErrNotFound
	}
	if d.files[id] == nil {
		d.files[id] = map[string][]byte{}
	}
	buf := make([]byte, len(content))
	copy(buf, content)
	d.files[id][path] = buf
	d.notify(id, driver.FileEvent{Type: "write", Path: path})
	return nil
}

func (d *Driver) RemoveFile(_ context.Context, id, path string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.boxes[id]; !ok {
		return driver.ErrNotFound
	}
	delete(d.files[id], path)
	return nil
}
