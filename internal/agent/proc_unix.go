//go:build unix

package agent

import (
	"errors"
	"os/exec"
	"syscall"
)

func prepareCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killGroup(pid, sig int) error {
	err := syscall.Kill(-pid, syscall.Signal(sig))
	if err != nil {
		err = syscall.Kill(pid, syscall.Signal(sig))
	}
	return err
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return 1
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	if ee.ExitCode() >= 0 {
		return ee.ExitCode()
	}
	return 1
}
