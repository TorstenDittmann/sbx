//go:build linux

package agent

import (
	"errors"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// exerciseVsockConn checks the raw fd net.Conn. AF_VSOCK has no host loopback
// on a machine that is not itself a guest, so the byte path uses a socketpair.
func exerciseVsockConn() error {
	return exerciseVsockSocketpair()
}

func exerciseVsockSocketpair() error {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return err
	}
	left, err := newVsockConn(fds[0], 1)
	if err != nil {
		_ = unix.Close(fds[1])
		return err
	}
	right, err := newVsockConn(fds[1], 2)
	if err != nil {
		_ = left.Close()
		return err
	}
	defer right.Close()
	defer left.Close()
	if left.LocalAddr().Network() != "vsock" || left.RemoteAddr().String() != "vsock:1" {
		return fmt.Errorf("addrs local=%s remote=%s", left.LocalAddr(), left.RemoteAddr())
	}
	if _, err := left.Write(nil); err != nil {
		return err
	}
	if n, err := left.Read(nil); n != 0 || err != nil {
		return fmt.Errorf("empty read %d %v", n, err)
	}
	if err := left.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	if _, err := left.Write([]byte("ping")); err != nil {
		return fmt.Errorf("socketpair write: %w", err)
	}
	buf := make([]byte, 4)
	if err := right.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	n, err := right.Read(buf)
	if err != nil || string(buf[:n]) != "ping" {
		return fmt.Errorf("socketpair read %q %v", buf[:n], err)
	}
	if err := right.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		return err
	}
	if _, err := right.Read(buf); !errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Errorf("deadline = %v", err)
	}
	if err := left.SetDeadline(time.Time{}); err != nil {
		return err
	}
	if err := left.Close(); err != nil {
		return err
	}
	if err := left.Close(); !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("second close %v", err)
	}
	if _, err := left.Write([]byte("x")); !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("write after close %v", err)
	}
	return nil
}

func TestVsockReadErrorCount(t *testing.T) {
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Close(fd); err != nil {
		t.Fatal(err)
	}
	conn := &vsockConn{fd: fd, remote: 1}
	n, err := conn.Read(make([]byte, 8))
	if n != 0 || err == nil {
		t.Fatalf("read on a closed fd = %d, %v", n, err)
	}
}
