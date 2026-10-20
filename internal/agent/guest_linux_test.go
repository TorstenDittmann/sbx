//go:build linux

package agent

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLinuxGuestNetAndVsock(t *testing.T) {
	if os.Getuid() != 0 {
		if err := configureGuestNet("10.0.0.2/30", "10.0.0.1"); err == nil {
			t.Fatal("expected guest net error")
		}
	}
	if err := linkUp("sbx-no-such-iface"); err == nil {
		t.Fatal("expected link error")
	}
	if err := sendNetlink([]byte{1, 0, 0, 0}); err == nil {
		t.Fatal("expected netlink error")
	}
	path := t.TempDir() + "/resolv"
	if err := writeResolv(path, []string{"1.1.1.1"}); err != nil {
		t.Fatal(err)
	}
	if err := writeResolv(path, nil); err != nil {
		t.Fatal(err)
	}
	ln, err := listenVsock(15252)
	if err != nil {
		if err := writeResolv("/no/such/dir/resolv", []string{"1.1.1.1"}); err == nil {
			t.Fatal("expected write error")
		}
		return
	}
	defer ln.Close()
	if ln.Addr().Network() != "vsock" || ln.Addr().String() == "" {
		t.Fatal(ln.Addr())
	}
	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if conn != nil {
			_ = conn.Close()
		}
		done <- err
	}()
	_ = ln.Close()
	<-done
}

func TestMountfs(t *testing.T) {
	dir := t.TempDir()
	err := mountfs("tmpfs", dir, "tmpfs")
	if os.Getuid() != 0 {
		if err == nil {
			t.Fatal("expected mount to fail")
		}
		return
	}
	if errors.Is(err, unix.EPERM) {
		t.Skip("mount is not permitted in this environment")
	}
	if err != nil {
		t.Fatal(err)
	}
	// Unmount before TempDir cleanup, which otherwise fails with EBUSY.
	if err := unix.Unmount(dir, 0); err != nil {
		t.Fatal(err)
	}
}

func TestVsockListenerCloseWakesAccept(t *testing.T) {
	ln, err := listenVsock(15253)
	if err != nil {
		t.Skip(err)
	}
	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if conn != nil {
			_ = conn.Close()
		}
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	_ = ln.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("accept after close = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not wake a blocked Accept")
	}
}

func TestReapOrphansSkipsTrackedChildren(t *testing.T) {
	orphan := exec.Command("true")
	if err := orphan.Start(); err != nil {
		t.Skip(err)
	}
	tracked := exec.Command("true")
	if err := startChild(tracked); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(zombieChildren("/proc", os.Getpid())) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := reapOrphans("/proc", os.Getpid()); n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
	if err := waitChild(tracked); err != nil {
		t.Fatalf("tracked child lost its exit status: %v", err)
	}
	if err := orphan.Wait(); err == nil {
		t.Fatal("expected the untracked child to be reaped already")
	}
}

func TestParseStat(t *testing.T) {
	state, ppid, ok := parseStat("42 (a b) c) Z 1 42 42 0")
	if !ok || state != "Z" || ppid != 1 {
		t.Fatalf("%q %d %v", state, ppid, ok)
	}
	if _, _, ok := parseStat("garbage"); ok {
		t.Fatal("expected parse failure")
	}
}
