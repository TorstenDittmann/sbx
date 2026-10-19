package firecracker

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
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
	var logWriter *os.File
	if spec.Log != "" {
		f, err := os.OpenFile(spec.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, err
		}
		logWriter = f
		pr, pw, err := os.Pipe()
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		cmd.Stdout = pw
		cmd.Stderr = pw
		go func() {
			defer f.Close()
			defer pr.Close()
			buf := make([]byte, 512)
			for {
				n, err := pr.Read(buf)
				if n > 0 {
					_, _ = f.Write(buf[:n])
					_ = f.Sync()
				}
				if err != nil {
					return
				}
			}
		}()
		defer pw.Close()
	}
	if err := cmd.Start(); err != nil {
		if logWriter != nil {
			_ = logWriter.Close()
		}
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

// pidExited reports a zombie or a missing process. A child Firecracker that
// the kernel killed stays a zombie until Wait, and Kill(pid, 0) still succeeds.
func pidExited(pid int) bool {
	if pid <= 1 {
		return true
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	s := string(raw)
	i := strings.LastIndex(s, ")")
	if i < 0 || i+2 >= len(s) {
		return false
	}
	state := s[i+2]
	return state == 'Z' || state == 'X' || state == 'x'
}
