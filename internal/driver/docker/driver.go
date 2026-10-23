package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/TorstenDittmann/sbx/internal/agent"
	"github.com/TorstenDittmann/sbx/internal/driver"
)

const (
	labelManaged = "sbx.managed"
	labelID      = "sbx.id"
	labelBackend = "sbx.backend"
)

// Options configures a Docker-backed driver. Name is "docker" or "gvisor".
// Runtime is empty for the daemon default, or "runsc" for gVisor.
type Options struct {
	Host           string
	Name           string
	Runtime        string
	ReadOnlyRootfs bool
	UID            int
	GID            int
	MaxOutputBytes int
	// NoEgressFilter skips the metadata firewall. Tests set it. Production
	// creates with network=full always install the filter or fail closed.
	NoEgressFilter bool
	// ConfiguredPolicy keeps BlockPrivate and BlockMetadata as given.
	// Without it, both default to true.
	ConfiguredPolicy bool
	BlockPrivate     bool
	BlockMetadata    bool
	PrivateAllow     []string
	// IPv6 is auto, on, or off. Empty means auto.
	IPv6 string
	// Root runs the sandbox as uid 0. The default keeps uid 1000.
	Root bool
	// ScriptRunner applies the firewall script. When nil, the driver tries
	// the host iptables and then a privileged helper.
	ScriptRunner func(ctx context.Context, script string) error
	// AgentBin is the host path of the static sbx-agent binary. When set,
	// sandboxes run it as their entrypoint and exec, files, terminals, and
	// previews go through it. When empty, exec and files use the engine API.
	AgentBin string
}

// Driver implements driver.Driver using the Docker Engine API.
type Driver struct {
	name        string
	runtime     string
	opt         Options
	client      *Client
	egressMu    sync.Mutex
	egressReady bool
	// egressChecked is when verifyEgress last looked at the baseline.
	egressChecked time.Time
	bridge        string

	agentMu    sync.Mutex
	agentReady string
	guestHook  func(context.Context, string) (*agent.Client, error)

	sessMu    sync.Mutex
	tokens    map[string]string
	sessions  map[string]*agent.Client
	sessLocks map[string]*sync.Mutex

	polMu    sync.Mutex
	policies map[string]appliedNet
}

type appliedNet struct {
	spec    driver.SandboxSpec
	ipv4    string
	ipv6    string
	chain   string
	hostDev string
}

// New connects to the Docker socket.
func New(opt Options) (*Driver, error) {
	if opt.Name == "" {
		opt.Name = "docker"
	}
	opt = normalizeIdentity(opt)
	if opt.MaxOutputBytes == 0 {
		opt.MaxOutputBytes = 8 << 20
	}
	opt = normalizeNetOptions(opt)
	cli, err := NewClient(opt.Host)
	if err != nil {
		return nil, err
	}
	d := &Driver{name: opt.Name, runtime: opt.Runtime, opt: opt, client: cli}
	d.initSessions()
	return d, nil
}

// NewWithClient builds a driver around an existing client. Tests use it.
func NewWithClient(opt Options, cli *Client) *Driver {
	if opt.Name == "" {
		opt.Name = "docker"
	}
	opt = normalizeIdentity(opt)
	if opt.MaxOutputBytes == 0 {
		opt.MaxOutputBytes = 8 << 20
	}
	opt = normalizeNetOptions(opt)
	d := &Driver{name: opt.Name, runtime: opt.Runtime, opt: opt, client: cli}
	d.initSessions()
	return d
}

func normalizeIdentity(opt Options) Options {
	if opt.Root {
		opt.UID = 0
		opt.GID = 0
		return opt
	}
	if opt.UID == 0 {
		opt.UID = 1000
	}
	if opt.GID == 0 {
		opt.GID = 1000
	}
	return opt
}

func normalizeNetOptions(opt Options) Options {
	if opt.IPv6 == "" {
		opt.IPv6 = "auto"
	}
	if !opt.ConfiguredPolicy {
		opt.BlockPrivate = true
		opt.BlockMetadata = true
	}
	return opt
}

var _ driver.Driver = (*Driver)(nil)

func (d *Driver) Name() string { return d.name }

func (d *Driver) Capabilities() driver.Caps {
	level := "container"
	reqs := []string{"docker"}
	if d.runtime != "" {
		level = "gvisor"
		reqs = append(reqs, d.runtime)
	}
	guest := d.GuestEnabled()
	return driver.Caps{
		Pause: true, NetPolicy: true, IsolationLevel: level,
		Exec: true, Files: true, PTY: guest, Watch: guest, Preview: guest,
		Networks:     []driver.NetMode{driver.NetNone, driver.NetFull},
		Requirements: reqs,
	}
}

// Report probes the daemon and, for gVisor, the configured runtime.
func (d *Driver) Report(ctx context.Context) driver.Info {
	err := d.Check(ctx)
	detail := "daemon reachable"
	if d.runtime != "" && err == nil {
		detail = d.runtime + " is registered"
	}
	if err != nil {
		detail = err.Error()
	}
	return driver.Describe(d.name, d.Capabilities(), err == nil, detail)
}

func (d *Driver) Ping(ctx context.Context) error { return d.client.Ping(ctx) }

// DaemonInfo returns the runtime table. Other backends use it to tell a
// registered runtime from a Kata runtime class.
func (d *Driver) DaemonInfo(ctx context.Context) (Info, error) {
	return d.client.Info(ctx)
}

// Check pings the daemon and, for a gVisor driver, verifies the runtime exists.
func (d *Driver) Check(ctx context.Context) error {
	if err := d.Ping(ctx); err != nil {
		return err
	}
	if d.runtime == "" {
		return nil
	}
	info, err := d.client.Info(ctx)
	if err != nil {
		return err
	}
	if _, ok := info.Runtimes[d.runtime]; !ok {
		return fmt.Errorf("%w: Docker has no %q runtime", driver.ErrRuntimeUnavailable, d.runtime)
	}
	return nil
}

func (d *Driver) EnsureImage(ctx context.Context, ref string) error {
	if _, err := d.client.ImageInspect(ctx, ref); err == nil {
		return nil
	}
	if err := d.client.ImagePull(ctx, ref); err != nil {
		return fmt.Errorf("%w: %s", driver.ErrImage, err.Error())
	}
	if _, err := d.client.ImageInspect(ctx, ref); err != nil {
		return fmt.Errorf("%w: %s", driver.ErrImage, err.Error())
	}
	return nil
}

func (d *Driver) Create(ctx context.Context, spec driver.SandboxSpec) (driver.Handle, error) {
	if d.runtime != "" {
		info, err := d.client.Info(ctx)
		if err != nil {
			return driver.Handle{}, err
		}
		if _, ok := info.Runtimes[d.runtime]; !ok {
			hint := "sbx doctor prints the steps"
			if d.name == "gvisor" {
				hint = "install runsc and register it; sbx doctor prints the steps"
			}
			return driver.Handle{}, fmt.Errorf("%w: Docker has no %q runtime (%s)", driver.ErrRuntimeUnavailable, d.runtime, hint)
		}
	}
	if spec.Network == driver.NetFull {
		if err := d.ensureFullNetwork(ctx); err != nil {
			return driver.Handle{}, err
		}
	}
	token := ""
	if d.opt.AgentBin != "" {
		if err := d.ensureAgentVolume(ctx); err != nil {
			return driver.Handle{}, err
		}
		token = newAgentToken()
	}
	if err := d.prepareMounts(ctx, spec.Mounts); err != nil {
		return driver.Handle{}, err
	}
	if err := d.EnsureImage(ctx, spec.Image); err != nil {
		return driver.Handle{}, err
	}
	meta, err := d.client.ImageInspect(ctx, spec.Image)
	if err != nil {
		return driver.Handle{}, err
	}
	body := buildCreate(spec, d.opt, meta.Env, token)
	id, err := d.createAndStart(ctx, spec.ID, body)
	if err != nil && spec.Network == driver.NetFull && networkMissing(err) {
		// Something removed the sbx network after it was ensured, for
		// example docker network prune. Recreate it and try once more.
		d.forgetEgress()
		if err := d.ensureFullNetwork(ctx); err != nil {
			return driver.Handle{}, err
		}
		id, err = d.createAndStart(ctx, spec.ID, body)
	}
	if err != nil {
		return driver.Handle{}, err
	}
	if token != "" {
		d.rememberToken(id, token)
	}
	if spec.Network == driver.NetFull && !d.opt.NoEgressFilter {
		if err := d.ApplyNetPolicy(ctx, id, spec); err != nil {
			_ = d.client.ContainerRemove(context.Background(), id)
			return driver.Handle{}, err
		}
	}
	rt := d.runtime
	if rt == "" {
		rt = "runc"
	}
	return driver.Handle{ID: id, RuntimeName: rt}, nil
}

func (d *Driver) createAndStart(ctx context.Context, name string, body createRequest) (string, error) {
	id, err := d.client.ContainerCreate(ctx, name, body)
	if err != nil {
		return "", err
	}
	if err := d.client.ContainerStart(ctx, id); err != nil {
		_ = d.client.ContainerRemove(context.Background(), id)
		return "", err
	}
	return id, nil
}

func (d *Driver) Delete(ctx context.Context, id string) error {
	d.dropNetPolicy(ctx, id)
	d.closeSession(id)
	return d.client.ContainerRemove(ctx, id)
}

func (d *Driver) Inspect(ctx context.Context, id string) (driver.Status, error) {
	st, err := d.client.ContainerInspect(ctx, id)
	if err != nil {
		return driver.Status{}, err
	}
	return driver.Status{
		ID: st.ID, SandboxID: st.SandboxID, State: mapState(st), Image: st.Image,
		OOMKilled: st.OOMKilled, ExitCode: st.ExitCode,
	}, nil
}

// WatchEvents reports oom and die events for sbx containers. Every
// Docker-backed driver shares the daemon, so each sees every sbx container;
// the caller keeps the events whose sandbox belongs to this backend.
func (d *Driver) WatchEvents(ctx context.Context, emit func(driver.Event)) error {
	return d.client.Events(ctx, func(ev EngineEvent) {
		out := driver.Event{ID: ev.ID, SandboxID: ev.Attributes[labelID]}
		switch ev.Action {
		case "oom":
			out.Kind = driver.EventOOM
		case "die":
			out.Kind = driver.EventDie
			out.ExitCode, _ = strconv.Atoi(ev.Attributes["exitCode"])
		default:
			return
		}
		emit(out)
	})
}

func (d *Driver) List(ctx context.Context) ([]driver.Status, error) {
	items, err := d.client.ContainerList(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]driver.Status, 0, len(items))
	for _, st := range items {
		out = append(out, driver.Status{ID: st.ID, SandboxID: st.SandboxID, State: mapState(st), Image: st.Image})
	}
	return out, nil
}

func mapState(st ContainerState) driver.State {
	if st.Paused || st.Status == "paused" {
		return driver.StatePaused
	}
	switch st.Status {
	case "running":
		return driver.StateRunning
	case "created", "exited", "dead", "removing":
		return driver.StateStopped
	default:
		if st.Running {
			return driver.StateRunning
		}
		return driver.StateUnknown
	}
}

func (d *Driver) Pause(ctx context.Context, id string) error {
	return d.client.ContainerPause(ctx, id)
}

func (d *Driver) Resume(ctx context.Context, id string) error {
	return d.client.ContainerUnpause(ctx, id)
}

func (d *Driver) Exec(ctx context.Context, id string, req driver.ExecRequest) (driver.ExecResult, error) {
	var stdout, stderr bytes.Buffer
	res, err := d.exec(ctx, id, req, 0, func(chunk driver.ExecChunk) error {
		switch chunk.Kind {
		case driver.StreamStdout:
			stdout.Write(chunk.Data)
		case driver.StreamStderr:
			stderr.Write(chunk.Data)
		}
		return nil
	})
	res.Stdout = stdout.Bytes()
	res.Stderr = stderr.Bytes()
	return res, err
}

func (d *Driver) ExecStream(ctx context.Context, id string, req driver.ExecRequest, emit func(driver.ExecChunk) error) (driver.ExecResult, error) {
	return d.exec(ctx, id, req, 0, emit)
}

func (d *Driver) exec(ctx context.Context, id string, req driver.ExecRequest, limit int, emit func(driver.ExecChunk) error) (driver.ExecResult, error) {
	guest, gerr := d.guest(ctx, id)
	if gerr == nil {
		if limit <= 0 {
			limit = d.opt.MaxOutputBytes
		}
		return execViaAgent(ctx, guest, req, limit, emit)
	}
	if !errors.Is(gerr, errNoGuest) {
		return driver.ExecResult{}, gerr
	}
	start := time.Now()
	if len(req.Cmd) == 0 {
		return driver.ExecResult{}, fmt.Errorf("cmd is required")
	}
	cwd := req.Cwd
	if cwd == "" {
		cwd = "/workspace"
	}
	cfg := execConfig{
		AttachStdout: true,
		AttachStderr: true,
		AttachStdin:  req.Stdin != nil,
		Cmd:          withTimeout(req.Cmd, req.TimeoutSeconds),
		Env:          envSlice(req.Env),
		WorkingDir:   cwd,
		User:         fmt.Sprintf("%d:%d", d.opt.UID, d.opt.GID),
	}
	execID, err := d.client.ExecCreate(ctx, id, cfg)
	if err != nil {
		return driver.ExecResult{}, err
	}
	runCtx := ctx
	cancel := func() {}
	if req.TimeoutSeconds > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds+5)*time.Second)
	}
	defer cancel()
	stream, err := d.client.ExecStart(runCtx, execID, req.Stdin)
	if err != nil {
		return driver.ExecResult{}, err
	}
	defer stream.Close()
	if limit <= 0 {
		limit = d.opt.MaxOutputBytes
	}
	_, truncated, err := CopyDemux(stream, limit, func(streamByte byte, data []byte) error {
		if emit == nil || len(data) == 0 {
			return nil
		}
		kind := driver.StreamStdout
		if streamByte == 2 {
			kind = driver.StreamStderr
		}
		return emit(driver.ExecChunk{Kind: kind, Data: data})
	})
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return driver.ExecResult{Duration: time.Since(start), Truncated: truncated}, context.DeadlineExceeded
	}
	state, inspectErr := d.waitExec(context.Background(), execID)
	if inspectErr != nil {
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return driver.ExecResult{Duration: time.Since(start), Truncated: truncated}, err
		}
		return driver.ExecResult{Duration: time.Since(start), Truncated: truncated}, inspectErr
	}
	if state.Running && err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return driver.ExecResult{Duration: time.Since(start), Truncated: truncated}, err
	}
	return driver.ExecResult{ExitCode: state.ExitCode, Duration: time.Since(start), Truncated: truncated}, nil
}

func (d *Driver) waitExec(ctx context.Context, execID string) (ExecState, error) {
	deadline := time.Now().Add(2 * time.Second)
	var last ExecState
	for {
		st, err := d.client.ExecInspect(ctx, execID)
		if err != nil {
			return ExecState{}, err
		}
		last = st
		if !st.Running {
			return st, nil
		}
		if time.Now().After(deadline) {
			return last, nil
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func (d *Driver) ListFiles(ctx context.Context, id, dir string) ([]driver.FileEntry, error) {
	if c, err := d.guest(ctx, id); err == nil {
		return listViaAgent(ctx, c, dir)
	} else if !errors.Is(err, errNoGuest) {
		return nil, err
	}
	// POSIX sh + BusyBox/GNU stat. The name is the last field so it may contain '|'.
	script := `find "$1" -mindepth 1 -maxdepth 1 -exec stat -c '%F|%s|%Y|%f|%n' {} \;`
	res, err := d.Exec(ctx, id, driver.ExecRequest{
		Cmd:            []string{"sh", "-c", script, "sh", dir},
		TimeoutSeconds: 30,
	})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(string(res.Stderr))
		if msg == "" {
			msg = "not a directory"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	var entries []driver.FileEntry
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 5)
		if len(parts) != 5 {
			continue
		}
		size, _ := strconv.ParseInt(parts[1], 10, 64)
		mtime, _ := strconv.ParseInt(parts[2], 10, 64)
		mode64, _ := strconv.ParseUint(parts[3], 16, 32)
		full := parts[4]
		entries = append(entries, driver.FileEntry{
			Name:    path.Base(full),
			Path:    full,
			Type:    fileType(parts[0]),
			Size:    size,
			Mode:    uint32(mode64) & 0o777,
			ModTime: time.Unix(mtime, 0).UTC(),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

func fileType(kind string) driver.FileType {
	k := strings.ToLower(kind)
	switch {
	case strings.Contains(k, "directory"):
		return driver.FileDir
	case strings.Contains(k, "link"):
		return driver.FileLink
	default:
		return driver.FileFile
	}
}

func (d *Driver) ReadFile(ctx context.Context, id, filePath string, max int) ([]byte, error) {
	if c, err := d.guest(ctx, id); err == nil {
		if max <= 0 {
			max = d.opt.MaxOutputBytes
		}
		return c.ReadFile(ctx, filePath, max)
	} else if !errors.Is(err, errNoGuest) {
		return nil, err
	}
	if max <= 0 {
		max = d.opt.MaxOutputBytes
	}
	var stdout, stderr bytes.Buffer
	res, err := d.exec(ctx, id, driver.ExecRequest{
		Cmd:            []string{"cat", filePath},
		TimeoutSeconds: 60,
	}, max, func(chunk driver.ExecChunk) error {
		switch chunk.Kind {
		case driver.StreamStdout:
			stdout.Write(chunk.Data)
		case driver.StreamStderr:
			stderr.Write(chunk.Data)
		}
		return nil
	})
	res.Stdout = stdout.Bytes()
	res.Stderr = stderr.Bytes()
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(string(res.Stderr))
		if msg == "" {
			msg = "cannot read file"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	if res.Truncated {
		return res.Stdout, fmt.Errorf("file exceeds %d bytes", max)
	}
	return res.Stdout, nil
}

func (d *Driver) WriteFile(ctx context.Context, id, filePath string, content []byte, mode uint32) error {
	if c, err := d.guest(ctx, id); err == nil {
		return c.WriteFile(ctx, filePath, content, mode)
	} else if !errors.Is(err, errNoGuest) {
		return err
	}
	parent := path.Dir(filePath)
	mk, err := d.Exec(ctx, id, driver.ExecRequest{
		Cmd:            []string{"mkdir", "-p", parent},
		TimeoutSeconds: 30,
	})
	if err != nil {
		return err
	}
	if mk.ExitCode != 0 {
		return fmt.Errorf("mkdir: %s", strings.TrimSpace(string(mk.Stderr)))
	}
	// cat reads stdin and the shell redirect creates the file. stdin is the
	// raw body, so this works on a read-only rootfs (docker cp does not) and
	// keeps binary content intact.
	res, err := d.Exec(ctx, id, driver.ExecRequest{
		Cmd:            []string{"sh", "-c", `cat > "$1"`, "sh", filePath},
		Stdin:          bytes.NewReader(content),
		TimeoutSeconds: 60,
	})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(string(res.Stderr))
		if msg == "" {
			msg = "write failed"
		}
		return fmt.Errorf("%s", msg)
	}
	if mode == 0 {
		mode = 0o644
	}
	ch, err := d.Exec(ctx, id, driver.ExecRequest{
		Cmd:            []string{"chmod", fmt.Sprintf("%o", mode), filePath},
		TimeoutSeconds: 15,
	})
	if err != nil {
		return err
	}
	if ch.ExitCode != 0 {
		return fmt.Errorf("chmod: %s", strings.TrimSpace(string(ch.Stderr)))
	}
	return nil
}

func (d *Driver) RemoveFile(ctx context.Context, id, filePath string) error {
	if c, err := d.guest(ctx, id); err == nil {
		return c.RemoveFile(ctx, filePath)
	} else if !errors.Is(err, errNoGuest) {
		return err
	}
	switch filePath {
	case "/", "/workspace", "/tmp":
		return fmt.Errorf("refusing to delete %s", filePath)
	}
	res, err := d.Exec(ctx, id, driver.ExecRequest{
		Cmd:            []string{"rm", "-rf", filePath},
		TimeoutSeconds: 30,
	})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s", strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}
