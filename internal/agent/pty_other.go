//go:build !linux

package agent

import (
	"os"
	"os/exec"
)

func startPTY(*exec.Cmd, int, int) (*os.File, error) {
	return nil, errUnsupported("pty is only supported on linux")
}

func resizePTY(*os.File, int, int) error {
	return errUnsupported("pty is only supported on linux")
}
