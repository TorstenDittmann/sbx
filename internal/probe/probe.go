// Package probe checks host prerequisites for sandbox backends.
package probe

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// KVM reports whether /dev/kvm exists and is writable by this process.
// MicroVM backends need it. A missing device is not an error on its own;
// the caller decides whether that backend is required.
func KVM() (ok bool, detail string) {
	if runtime.GOOS != "linux" {
		return false, "KVM is a Linux device"
	}
	fi, err := os.Stat("/dev/kvm")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, "/dev/kvm is missing (load kvm and kvm_intel or kvm_amd)"
		}
		return false, err.Error()
	}
	if fi.Mode()&os.ModeDevice == 0 {
		return false, "/dev/kvm is not a device"
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return false, "/dev/kvm is not writable by this user (" + err.Error() + ")"
	}
	_ = f.Close()
	return true, "/dev/kvm is writable"
}

// Which resolves name on PATH. An empty name is not found.
func Which(name string) (string, bool) {
	if name == "" || strings.ContainsAny(name, " \t\n") {
		return "", false
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", false
	}
	return path, true
}

// File reports whether path is a regular file.
func File(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}
