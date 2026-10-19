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
			Pause: true, NetPolicy: true, IsolationLevel: level,
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

func (d *Driver) Create(_ context.Context, spec driver.SandboxSpec) (driver.Handle, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.CreateErr != nil {
		return driver.Handle{}, d.CreateErr
	}
	d.boxes[spec.ID] = &box{spec: spec, state: driver.StateRunning, image: spec.Image}
	if d.files[spec.ID] == nil {
		d.files[spec.ID] = map[string][]byte{}
	}
	return driver.Handle{ID: spec.ID, RuntimeName: d.name}, nil
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
	return driver.Status{ID: id, SandboxID: b.spec.ID, State: b.state, Image: b.image}
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
