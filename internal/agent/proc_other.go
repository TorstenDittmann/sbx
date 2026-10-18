//go:build !unix

package agent

import "os/exec"

func prepareCmd(*exec.Cmd) {}

func killGroup(int, int) error { return errUnsupported("signals are not supported on this OS") }

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	return 1
}
