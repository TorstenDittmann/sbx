//go:build linux

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

func listenVsock(port uint32) (net.Listener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("vsock bind %d: %w", port, err)
	}
	if err := unix.Listen(fd, 16); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("vsock listen: %w", err)
	}
	return &vsockListener{fd: fd, port: port}, nil
}

type vsockListener struct {
	fd   int
	port uint32
}

func (l *vsockListener) Accept() (net.Conn, error) {
	nfd, _, err := unix.Accept4(l.fd, unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
	if err != nil {
		return nil, err
	}
	conn, err := newVsockConn(nfd, 0)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (l *vsockListener) Close() error { return unix.Close(l.fd) }

func (l *vsockListener) Addr() net.Addr { return vsockAddr{port: l.port} }

type vsockAddr struct{ port uint32 }

func (a vsockAddr) Network() string { return "vsock" }
func (a vsockAddr) String() string  { return fmt.Sprintf("vsock:%d", a.port) }

// vsockConn is a net.Conn over an AF_VSOCK fd. net.FileConn cannot wrap one:
// the syscall package has no SockaddrVM, so Getsockname returns EAFNOSUPPORT
// and FileConn turns that into EPROTONOSUPPORT.
type vsockConn struct {
	mu     sync.Mutex
	fd     int
	remote uint32
	rd     time.Time
	wd     time.Time
}

func newVsockConn(fd int, remote uint32) (*vsockConn, error) {
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return &vsockConn{fd: fd, remote: remote}, nil
}

func (c *vsockConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		fd, err := c.raw()
		if err != nil {
			return 0, err
		}
		n, err := unix.Read(fd, p)
		if err == unix.EINTR {
			continue
		}
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			if err := pollFD(fd, unix.POLLIN, c.readDeadline()); err != nil {
				return 0, err
			}
			continue
		}
		if err != nil {
			return n, err
		}
		if n == 0 {
			return 0, io.EOF
		}
		return n, nil
	}
}

func (c *vsockConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	written := 0
	for written < len(p) {
		fd, err := c.raw()
		if err != nil {
			if written > 0 {
				return written, err
			}
			return 0, err
		}
		n, err := unix.Write(fd, p[written:])
		if n > 0 {
			written += n
		}
		if err == nil || err == unix.EINTR {
			continue
		}
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			if werr := pollFD(fd, unix.POLLOUT, c.writeDeadline()); werr != nil {
				if written > 0 {
					return written, werr
				}
				return 0, werr
			}
			continue
		}
		if written > 0 {
			return written, err
		}
		return 0, err
	}
	return written, nil
}

func (c *vsockConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fd < 0 {
		return net.ErrClosed
	}
	fd := c.fd
	c.fd = -1
	return unix.Close(fd)
}

func (c *vsockConn) LocalAddr() net.Addr {
	fd, err := c.raw()
	if err != nil {
		return vsockAddr{}
	}
	sa, err := unix.Getsockname(fd)
	if err != nil {
		return vsockAddr{}
	}
	vm, ok := sa.(*unix.SockaddrVM)
	if !ok {
		return vsockAddr{}
	}
	return vsockAddr{port: vm.Port}
}

func (c *vsockConn) RemoteAddr() net.Addr { return vsockAddr{port: c.remote} }

func (c *vsockConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.rd, c.wd = t, t
	c.mu.Unlock()
	return nil
}

func (c *vsockConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.rd = t
	c.mu.Unlock()
	return nil
}

func (c *vsockConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.wd = t
	c.mu.Unlock()
	return nil
}

func (c *vsockConn) raw() (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fd < 0 {
		return -1, net.ErrClosed
	}
	return c.fd, nil
}

func (c *vsockConn) readDeadline() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rd
}

func (c *vsockConn) writeDeadline() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wd
}

func pollFD(fd int, events int16, deadline time.Time) error {
	for {
		timeout, err := pollTimeout(deadline)
		if err != nil {
			return err
		}
		pfd := []unix.PollFd{{Fd: int32(fd), Events: events}}
		n, err := unix.Poll(pfd, timeout)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return os.ErrDeadlineExceeded
		}
		re := pfd[0].Revents
		if re&events != 0 || re&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return nil
		}
	}
}

func pollTimeout(deadline time.Time) (int, error) {
	if deadline.IsZero() {
		return -1, nil
	}
	remain := time.Until(deadline)
	if remain <= 0 {
		return 0, os.ErrDeadlineExceeded
	}
	ms := int(remain / time.Millisecond)
	if ms < 1 {
		ms = 1
	}
	return ms, nil
}

// dialVsock connects to cid:port. Firecracker forwards a guest connection to
// CID 2 into the host Unix socket "<uds_path>_<port>".
func dialVsock(cid, port uint32) (net.Conn, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	err = unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: port})
	if err != nil && !errors.Is(err, unix.EINPROGRESS) && !errors.Is(err, unix.EAGAIN) {
		_ = unix.Close(fd)
		return nil, err
	}
	if err != nil {
		if werr := pollFD(fd, unix.POLLOUT, time.Now().Add(2*time.Second)); werr != nil {
			_ = unix.Close(fd)
			return nil, werr
		}
		soerr, gerr := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
		if gerr != nil || soerr != 0 {
			_ = unix.Close(fd)
			if gerr != nil {
				return nil, gerr
			}
			return nil, unix.Errno(soerr)
		}
	}
	return newVsockConn(fd, port)
}

// dialHostVsock connects to the host (CID 2) on port.
func dialHostVsock(port uint32) (net.Conn, error) {
	return dialVsock(unix.VMADDR_CID_HOST, port)
}

// dialHostUntil keeps a guest-initiated session up until ctx is cancelled.
func dialHostUntil(ctx context.Context, port uint32, token string) {
	a := New(token)
	delay := 20 * time.Millisecond
	for ctx.Err() == nil {
		conn, err := dialHostVsock(port)
		if err != nil {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if delay < time.Second {
				delay *= 2
			}
			continue
		}
		delay = 20 * time.Millisecond
		a.ServeConn(conn)
	}
}
