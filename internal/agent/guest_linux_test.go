//go:build linux

package agent

import (
	"os"
	"testing"
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
