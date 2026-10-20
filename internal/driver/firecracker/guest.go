package firecracker

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/TorstenDittmann/sbx/internal/agent"
	"github.com/TorstenDittmann/sbx/internal/driver"
)

func (d *Driver) dialAgent(ctx context.Context, vm *vm) (*agent.Client, error) {
	deadline := time.Now().Add(30 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	// Guest-initiated connections land on <uds>_<port>. The guest dials CID 2
	// because a host CONNECT is reset on some guest kernels after listen.
	guestPath := vm.rec.Vsock + "_" + strconv.Itoa(agent.DefaultVsockPort)
	_ = os.Remove(guestPath)
	var guestLn net.Listener
	if ln, err := net.Listen("unix", guestPath); err == nil {
		// The jailer runs Firecracker as an unprivileged uid. It connect(2)s
		// to this socket from inside the chroot, which requires write permission.
		if err := prepareGuestSocket(guestPath, d.opt.UID, d.opt.GID, d.opt.Jailer != ""); err != nil {
			_ = ln.Close()
			_ = os.Remove(guestPath)
			return nil, err
		}
		guestLn = ln
		defer func() {
			_ = ln.Close()
			_ = os.Remove(guestPath)
		}()
	}
	var last error
	for {
		if guestLn != nil {
			if ul, ok := guestLn.(*net.UnixListener); ok {
				_ = ul.SetDeadline(time.Now().Add(40 * time.Millisecond))
			}
			conn, err := guestLn.Accept()
			if err == nil {
				client, herr := handshakeAgent(ctx, conn, vm.rec.Token)
				if herr == nil {
					return client, nil
				}
				last = herr
			} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
				last = err
			}
		}
		conn, err := DialVsock(vm.rec.Vsock, uint32(agent.DefaultVsockPort))
		if err == nil {
			client, herr := handshakeAgent(ctx, conn, vm.rec.Token)
			if herr == nil {
				return client, nil
			}
			last = herr
		} else {
			last = err
		}
		if vm.proc != nil && pidExited(vm.proc.PID()) {
			return nil, fmt.Errorf("firecracker exited before the agent answered: %w", last)
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			if last == nil {
				last = ctx.Err()
			}
			return nil, fmt.Errorf("sbx-agent: %w", last)
		}
		if guestLn != nil {
			continue
		}
		timer := time.NewTimer(30 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// prepareGuestSocket lets the jailer uid connect to the guest-initiated socket.
// Without a jailer, Firecracker is this process, so a failed chown is harmless.
func prepareGuestSocket(path string, uid, gid int, jail bool) error {
	if err := os.Chmod(path, 0o660); err != nil {
		return fmt.Errorf("guest vsock socket mode: %w", err)
	}
	if err := os.Chown(path, uid, gid); err != nil && jail {
		return fmt.Errorf("guest vsock socket owner: %w", err)
	}
	return nil
}

func handshakeAgent(ctx context.Context, conn net.Conn, token string) (*agent.Client, error) {
	hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	client, err := agent.Handshake(hctx, conn, token)
	cancel()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return client, nil
}

func (d *Driver) lock(id string) *sync.Mutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	mu, ok := d.locks[id]
	if !ok {
		mu = &sync.Mutex{}
		d.locks[id] = mu
	}
	return mu
}

func (d *Driver) guest(ctx context.Context, id string) (*agent.Client, error) {
	mu := d.lock(id)
	mu.Lock()
	defer mu.Unlock()
	d.mu.Lock()
	c := d.sessions[id]
	vm := d.vms[id]
	d.mu.Unlock()
	if vm == nil {
		return nil, driver.ErrNotFound
	}
	if c != nil && c.Alive() {
		return c, nil
	}
	if c != nil {
		_ = c.Close()
	}
	next, err := d.dialAgent(ctx, vm)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.sessions[id] = next
	d.mu.Unlock()
	return next, nil
}

func (d *Driver) Exec(ctx context.Context, id string, req driver.ExecRequest) (driver.ExecResult, error) {
	return d.ExecStream(ctx, id, req, nil)
}

func (d *Driver) ExecStream(ctx context.Context, id string, req driver.ExecRequest, emit func(driver.ExecChunk) error) (driver.ExecResult, error) {
	c, err := d.guest(ctx, id)
	if err != nil {
		return driver.ExecResult{}, err
	}
	return execViaAgent(ctx, c, req, d.opt.MaxOutputBytes, emit)
}

func (d *Driver) ListFiles(ctx context.Context, id, path string) ([]driver.FileEntry, error) {
	c, err := d.guest(ctx, id)
	if err != nil {
		return nil, err
	}
	items, err := c.ListFiles(ctx, path)
	if err != nil {
		return nil, err
	}
	out := make([]driver.FileEntry, 0, len(items))
	for _, item := range items {
		out = append(out, toEntry(item))
	}
	return out, nil
}

func (d *Driver) ReadFile(ctx context.Context, id, path string, max int) ([]byte, error) {
	c, err := d.guest(ctx, id)
	if err != nil {
		return nil, err
	}
	return c.ReadFile(ctx, path, max)
}

func (d *Driver) WriteFile(ctx context.Context, id, path string, content []byte, mode uint32) error {
	c, err := d.guest(ctx, id)
	if err != nil {
		return err
	}
	return c.WriteFile(ctx, path, content, mode)
}

func (d *Driver) RemoveFile(ctx context.Context, id, path string) error {
	c, err := d.guest(ctx, id)
	if err != nil {
		return err
	}
	return c.RemoveFile(ctx, path)
}

func (d *Driver) StatFile(ctx context.Context, id, path string) (driver.FileEntry, error) {
	c, err := d.guest(ctx, id)
	if err != nil {
		return driver.FileEntry{}, err
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
		return err
	}
	return c.Mkdir(ctx, path, mode)
}

func (d *Driver) Watch(ctx context.Context, id, path string, recursive bool, emit func(driver.FileEvent) error) error {
	c, err := d.guest(ctx, id)
	if err != nil {
		return err
	}
	return c.Watch(ctx, path, recursive, func(ev agent.FileEvent) error {
		if emit == nil {
			return nil
		}
		return emit(driver.FileEvent{Type: ev.Type, Path: ev.Path})
	})
}

func (d *Driver) StartProcess(ctx context.Context, id string, req driver.ExecRequest, tty bool, cols, rows int) (driver.Process, error) {
	c, err := d.guest(ctx, id)
	if err != nil {
		return driver.Process{}, err
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
		return nil, err
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
		return err
	}
	return c.Signal(ctx, procID, signal)
}

func (d *Driver) WriteProcessStdin(ctx context.Context, id, procID string, data []byte) error {
	c, err := d.guest(ctx, id)
	if err != nil {
		return err
	}
	return c.WriteStdinKeep(ctx, procID, data)
}

func (d *Driver) StreamProcess(ctx context.Context, id, procID string, emit func(driver.ExecChunk) error) (driver.ExecResult, error) {
	c, err := d.guest(ctx, id)
	if err != nil {
		return driver.ExecResult{}, err
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
	return driver.ExecResult{ExitCode: out.ExitCode, Duration: time.Since(start), Truncated: out.Truncated}, err
}

func (d *Driver) ResizeProcess(ctx context.Context, id, procID string, cols, rows int) error {
	c, err := d.guest(ctx, id)
	if err != nil {
		return err
	}
	return c.Resize(ctx, procID, cols, rows)
}

func (d *Driver) DialPort(ctx context.Context, id string, port int) (io.ReadWriteCloser, error) {
	c, err := d.guest(ctx, id)
	if err != nil {
		return nil, err
	}
	return c.Dial(ctx, port)
}

func execViaAgent(ctx context.Context, c *agent.Client, req driver.ExecRequest, limit int, emit func(driver.ExecChunk) error) (driver.ExecResult, error) {
	start := time.Now()
	out, err := c.Exec(ctx, agent.ExecSpec{
		Cmd: req.Cmd, Cwd: req.Cwd, Env: req.Env, TimeoutSeconds: req.TimeoutSeconds, MaxOutput: limit,
	}, req.Stdin, func(kind uint8, data []byte) error {
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

var (
	_ driver.Driver   = (*Driver)(nil)
	_ driver.Guest    = (*Driver)(nil)
	_ driver.Reporter = (*Driver)(nil)
)
