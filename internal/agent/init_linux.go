//go:build linux

package agent

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// preparePID1 mounts the virtual filesystems a guest needs when sbx-agent is init.
// Failures are ignored unless this process is actually PID 1.
func preparePID1() error {
	if os.Getpid() != 1 {
		return nil
	}
	// The Firecracker CI kernel mounts devtmpfs before init. Remounting it
	// returns EBUSY, which must not kill PID 1.
	if err := mountfs("devtmpfs", "/dev", "devtmpfs"); err != nil {
		return err
	}
	if err := mountfs("proc", "/proc", "proc"); err != nil {
		return err
	}
	_ = mountfs("sysfs", "/sys", "sysfs")
	return nil
}

func mountfs(source, target, fstype string) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	err := unix.Mount(source, target, fstype, 0, "")
	if err == nil || errors.Is(err, unix.EBUSY) {
		return nil
	}
	return err
}

func reapZombies() {
	if os.Getpid() != 1 {
		return
	}
	go func() {
		for {
			var ws unix.WaitStatus
			_, err := unix.Wait4(-1, &ws, 0, nil)
			if err != nil {
				return
			}
		}
	}()
}
