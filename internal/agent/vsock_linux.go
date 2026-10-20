//go:build linux

package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func listenVsock(port uint32) (net.Listener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
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
	nfd, _, err := unix.Accept(l.fd)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(nfd), "vsock")
	conn, err := net.FileConn(f)
	_ = f.Close()
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

// dialHostVsock connects to the host (CID 2) on port. Firecracker forwards that
// to the Unix socket "<uds_path>_<port>". Host-initiated CONNECT is reset on
// some kernels even after a guest listen, so the guest also dials out.
func dialHostVsock(port uint32) (net.Conn, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	err = unix.Connect(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: port})
	if err != nil && !errors.Is(err, unix.EINPROGRESS) && !errors.Is(err, unix.EAGAIN) {
		_ = unix.Close(fd)
		return nil, err
	}
	if err != nil {
		pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
		n, perr := unix.Poll(pfd, 100)
		if perr != nil || n == 0 {
			_ = unix.Close(fd)
			if perr == nil {
				perr = unix.ETIMEDOUT
			}
			return nil, perr
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
	if err := unix.SetNonblock(fd, false); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "vsock")
	conn, ferr := net.FileConn(f)
	_ = f.Close()
	if ferr != nil {
		return nil, ferr
	}
	return conn, nil
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
