package firecracker

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// SpawnSpec is how one Firecracker process is started.
type SpawnSpec struct {
	Bin        string
	Jailer     string
	ID         string
	UID        int
	GID        int
	ChrootBase string
	APISocket  string
	Log        string
}

// Process is a running Firecracker (or a test double).
type Process interface {
	PID() int
	Stop() error
}

func spawnFirecracker(spec SpawnSpec) (Process, error) {
	var cmd *exec.Cmd
	if spec.Jailer != "" {
		cmd = exec.Command(spec.Jailer,
			"--id", spec.ID,
			"--exec-file", spec.Bin,
			"--uid", strconv.Itoa(spec.UID),
			"--gid", strconv.Itoa(spec.GID),
			"--chroot-base-dir", spec.ChrootBase,
			"--", "--api-sock", "/run/fc.sock",
		)
	} else {
		if spec.Bin == "" {
			return nil, fmt.Errorf("firecracker binary is required")
		}
		args := []string{"--api-sock", spec.APISocket}
		if spec.Log != "" {
			args = append(args, "--log-path", spec.Log, "--level", "Info")
		}
		cmd = exec.Command(spec.Bin, args...)
	}
	if spec.Log != "" {
		f, err := os.OpenFile(spec.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, err
		}
		cmd.Stdout = f
		cmd.Stderr = f
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &cmdProc{cmd: cmd}, nil
}

type cmdProc struct {
	cmd *exec.Cmd
}

func (p *cmdProc) PID() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *cmdProc) Stop() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_ = p.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
	}
	return nil
}

func pidAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil
}
