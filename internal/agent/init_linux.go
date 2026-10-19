//go:build linux

package agent

import (
	"os"

	"golang.org/x/sys/unix"
)

// preparePID1 mounts the virtual filesystems a guest needs when sbx-agent is init.
// Failures are ignored unless this process is actually PID 1.
func preparePID1() error {
	if os.Getpid() != 1 {
		return nil
	}
	_ = os.MkdirAll("/dev", 0o755)
	_ = os.MkdirAll("/proc", 0o755)
	_ = os.MkdirAll("/sys", 0o755)
	if err := unix.Mount("devtmpfs", "/dev", "devtmpfs", 0, ""); err != nil {
		return err
	}
	if err := unix.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		return err
	}
	return unix.Mount("sysfs", "/sys", "sysfs", 0, "")
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
