package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/TorstenDittmann/sbx/internal/agent"
	"github.com/TorstenDittmann/sbx/internal/driver"
)

const (
	agentBinary   = "/opt/sbx/sbx-agent"
	agentVolume   = "sbx-agent-bin"
	agentTokenEnv = agent.EnvToken
	agentSockEnv  = agent.EnvSocket
	agentSocket   = agent.DefaultSocket
	agentSeedName = "sbx-agent-seed"
	agentHelper   = "alpine:3.20"
)

var errNoGuest = errors.New("no guest agent")

func (d *Driver) initSessions() {
	d.tokens = map[string]string{}
	d.sessions = map[string]*agent.Client{}
	d.sessLocks = map[string]*sync.Mutex{}
}

func newAgentToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString(b[:])
	}
	return hex.EncodeToString(b[:])
}

func (d *Driver) rememberToken(id, token string) {
	d.sessMu.Lock()
	d.tokens[id] = token
	d.sessMu.Unlock()
}

func (d *Driver) closeSession(id string) {
	d.sessMu.Lock()
	c := d.sessions[id]
	delete(d.sessions, id)
	delete(d.tokens, id)
	d.sessMu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

func (d *Driver) sessionLock(id string) *sync.Mutex {
	d.sessMu.Lock()
	defer d.sessMu.Unlock()
	mu, ok := d.sessLocks[id]
	if !ok {
		mu = &sync.Mutex{}
		d.sessLocks[id] = mu
	}
	return mu
}

// GuestEnabled reports whether new sandbox calls go through sbx-agent.
func (d *Driver) GuestEnabled() bool {
	return d.opt.AgentBin != "" || d.guestHook != nil
}

func (d *Driver) guest(ctx context.Context, id string) (*agent.Client, error) {
	if d.guestHook != nil {
		return d.guestHook(ctx, id)
	}
	if d.opt.AgentBin == "" {
		return nil, errNoGuest
	}
	mu := d.sessionLock(id)
	mu.Lock()
	defer mu.Unlock()
	d.sessMu.Lock()
	c := d.sessions[id]
	d.sessMu.Unlock()
	if c != nil && c.Alive() {
		return c, nil
	}
	if c != nil {
		_ = c.Close()
	}
	next, err := d.dial(ctx, id)
	if err != nil {
		return nil, err
	}
	d.sessMu.Lock()
	d.sessions[id] = next
	d.sessMu.Unlock()
	return next, nil
}

func (d *Driver) dial(ctx context.Context, id string) (*agent.Client, error) {
	token, err := d.lookupToken(ctx, id)
	if err != nil {
		return nil, err
	}
	cfg := execConfig{
		AttachStdout: true,
		AttachStderr: true,
		AttachStdin:  true,
		Cmd:          []string{agentBinary, "dial"},
		User:         fmt.Sprintf("%d:%d", d.opt.UID, d.opt.GID),
	}
	execID, err := d.client.ExecCreate(ctx, id, cfg)
	if err != nil {
		return nil, err
	}
	conn, err := d.client.ExecAttach(ctx, execID)
	if err != nil {
		return nil, err
	}
	hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client, err := agent.Handshake(hctx, conn, token)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return client, nil
}

func (d *Driver) lookupToken(ctx context.Context, id string) (string, error) {
	d.sessMu.Lock()
	token := d.tokens[id]
	d.sessMu.Unlock()
	if token != "" {
		return token, nil
	}
	st, err := d.client.ContainerInspect(ctx, id)
	if err != nil {
		return "", err
	}
	for _, item := range st.Env {
		key, value, ok := bytesCut(item)
		if ok && key == agentTokenEnv && value != "" {
			d.rememberToken(id, value)
			return value, nil
		}
	}
	return "", errors.New("sandbox has no agent token")
}

func bytesCut(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

func (d *Driver) ensureAgentVolume(ctx context.Context) error {
	sum, err := fileSHA(d.opt.AgentBin)
	if err != nil {
		return fmt.Errorf("agent binary: %w", err)
	}
	d.agentMu.Lock()
	defer d.agentMu.Unlock()
	if d.agentReady == sum {
		return nil
	}
	if err := d.seedVolume(ctx, sum); err != nil {
		return err
	}
	d.agentReady = sum
	return nil
}

func (d *Driver) seedVolume(ctx context.Context, sum string) error {
	bin, err := os.ReadFile(d.opt.AgentBin)
	if err != nil {
		return err
	}
	archive, err := agentTar(bin)
	if err != nil {
		return err
	}
	if err := d.EnsureImage(ctx, agentHelper); err != nil {
		return err
	}
	_ = d.client.VolumeCreate(ctx, agentVolume, map[string]string{
		labelManaged:       "true",
		"sbx.agent.sha256": sum,
	})
	_ = d.client.ContainerRemove(ctx, agentSeedName)
	body := createRequest{
		Image:      agentHelper,
		User:       "0",
		Entrypoint: []string{"/bin/sleep", "30"},
		Cmd:        &[]string{},
		HostConfig: hostConfig{
			NetworkMode: "none",
			CapDrop:     []string{"ALL"},
			CapAdd:      []string{},
			SecurityOpt: []string{"no-new-privileges:true"},
			LogConfig:   logConfig{Type: "json-file", Config: map[string]string{"max-size": "1m", "max-file": "1"}},
			IpcMode:     "private",
			Binds:       []string{agentVolume + ":/opt/sbx"},
		},
	}
	id, err := d.client.ContainerCreate(ctx, agentSeedName, body)
	if err != nil {
		return err
	}
	defer d.client.ContainerRemove(context.Background(), id)
	if err := d.client.ContainerStart(ctx, id); err != nil {
		return err
	}
	if err := d.client.PutArchive(ctx, id, "/opt/sbx", archive); err != nil {
		return fmt.Errorf("install agent: %w", err)
	}
	return nil
}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func agentTar(bin []byte) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{Name: "sbx-agent", Mode: 0o755, Size: int64(len(bin))}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, err
	}
	if _, err := tw.Write(bin); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func execViaAgent(ctx context.Context, c *agent.Client, req driver.ExecRequest, limit int, emit func(driver.ExecChunk) error) (driver.ExecResult, error) {
	start := time.Now()
	spec := agent.ExecSpec{
		Cmd: req.Cmd, Cwd: req.Cwd, Env: req.Env,
		TimeoutSeconds: req.TimeoutSeconds, MaxOutput: limit,
	}
	out, err := c.Exec(ctx, spec, req.Stdin, func(kind uint8, data []byte) error {
		if emit == nil || len(data) == 0 {
			return nil
		}
		stream := driver.StreamStdout
		if kind == agent.MsgStderr {
			stream = driver.StreamStderr
		}
		return emit(driver.ExecChunk{Kind: stream, Data: append([]byte(nil), data...)})
	})
	res := driver.ExecResult{ExitCode: out.ExitCode, Duration: time.Since(start), Truncated: out.Truncated}
	if err != nil {
		return res, err
	}
	return res, nil
}

func listViaAgent(ctx context.Context, c *agent.Client, dir string) ([]driver.FileEntry, error) {
	items, err := c.ListFiles(ctx, dir)
	if err != nil {
		return nil, err
	}
	out := make([]driver.FileEntry, 0, len(items))
	for _, item := range items {
		out = append(out, toEntry(item))
	}
	return out, nil
}

func toEntry(item agent.FileInfo) driver.FileEntry {
	return driver.FileEntry{
		Name: item.Name, Path: item.Path, Type: driver.FileType(item.Type),
		Size: item.Size, Mode: item.Mode, ModTime: time.UnixMilli(item.Mtime).UTC(),
	}
}

func toProcess(item agent.ProcessInfo) driver.Process {
	return driver.Process{
		ID: item.ID, PID: item.PID, Cmd: item.Cmd, Cwd: item.Cwd, TTY: item.TTY,
		Status: item.Status, ExitCode: item.ExitCode, StartedAt: time.UnixMilli(item.StartedAt).UTC(),
	}
}

func (d *Driver) StatFile(ctx context.Context, id, path string) (driver.FileEntry, error) {
	c, err := d.guest(ctx, id)
	if err != nil {
		return driver.FileEntry{}, guestErr(err)
	}
	info, err := c.Stat(ctx, path)
	if err != nil {
		return driver.FileEntry{}, err
	}
	return toEntry(info), nil
}

func (d *Driver) Mkdir(ctx context.Context, id, path string, mode uint32) error {
	c, err := d.guest(ctx, id)
	if err != nil {
		return guestErr(err)
	}
	return c.Mkdir(ctx, path, mode)
}

func (d *Driver) Watch(ctx context.Context, id, path string, recursive bool, emit func(driver.FileEvent) error) error {
	c, err := d.guest(ctx, id)
	if err != nil {
		return guestErr(err)
	}
	err = c.Watch(ctx, path, recursive, func(ev agent.FileEvent) error {
		if emit == nil {
			return nil
		}
		return emit(driver.FileEvent{Type: ev.Type, Path: ev.Path})
	})
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return err
}

func (d *Driver) StartProcess(ctx context.Context, id string, req driver.ExecRequest, tty bool, cols, rows int) (driver.Process, error) {
	c, err := d.guest(ctx, id)
	if err != nil {
		return driver.Process{}, guestErr(err)
	}
	info, err := c.Start(ctx, agent.ExecSpec{
		Cmd: req.Cmd, Cwd: req.Cwd, Env: req.Env, TimeoutSeconds: req.TimeoutSeconds,
		TTY: tty, Cols: cols, Rows: rows,
	})
	if err != nil {
		return driver.Process{}, err
	}
	return toProcess(info), nil
}

func (d *Driver) ListProcesses(ctx context.Context, id string) ([]driver.Process, error) {
	c, err := d.guest(ctx, id)
	if err != nil {
		return nil, guestErr(err)
	}
	items, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]driver.Process, 0, len(items))
	for _, item := range items {
		out = append(out, toProcess(item))
	}
	return out, nil
}

func (d *Driver) SignalProcess(ctx context.Context, id, procID, signal string) error {
	c, err := d.guest(ctx, id)
	if err != nil {
		return guestErr(err)
	}
	return c.Signal(ctx, procID, signal)
}

func (d *Driver) WriteProcessStdin(ctx context.Context, id, procID string, data []byte) error {
	c, err := d.guest(ctx, id)
	if err != nil {
		return guestErr(err)
	}
	return c.WriteStdinKeep(ctx, procID, data)
}

func (d *Driver) StreamProcess(ctx context.Context, id, procID string, emit func(driver.ExecChunk) error) (driver.ExecResult, error) {
	c, err := d.guest(ctx, id)
	if err != nil {
		return driver.ExecResult{}, guestErr(err)
	}
	start := time.Now()
	out, err := c.Attach(ctx, procID, func(kind uint8, data []byte) error {
		if emit == nil || len(data) == 0 {
			return nil
		}
		stream := driver.StreamStdout
		if kind == agent.MsgStderr {
			stream = driver.StreamStderr
		}
		return emit(driver.ExecChunk{Kind: stream, Data: append([]byte(nil), data...)})
	})
	res := driver.ExecResult{ExitCode: out.ExitCode, Duration: time.Since(start), Truncated: out.Truncated}
	return res, err
}

func (d *Driver) ResizeProcess(ctx context.Context, id, procID string, cols, rows int) error {
	c, err := d.guest(ctx, id)
	if err != nil {
		return guestErr(err)
	}
	return c.Resize(ctx, procID, cols, rows)
}

func (d *Driver) DialPort(ctx context.Context, id string, port int) (io.ReadWriteCloser, error) {
	c, err := d.guest(ctx, id)
	if err != nil {
		return nil, guestErr(err)
	}
	return c.Dial(ctx, port)
}

var _ driver.Guest = (*Driver)(nil)

func guestErr(err error) error {
	if errors.Is(err, errNoGuest) {
		return errors.New("sandbox agent is not enabled")
	}
	return err
}
