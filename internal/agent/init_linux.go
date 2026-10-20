//go:build linux

package agent

import (
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

// reapZombies collects orphans that get reparented to PID 1. A blanket
// wait4(-1) would race os/exec and steal exit codes of processes the agent
// is waiting on, and it returned ECHILD at boot before any child existed,
// which ended the reaper. Instead each SIGCHLD (and a slow tick, in case
// signals coalesce) reaps only zombies the agent does not track.
func reapZombies() {
	if os.Getpid() != 1 {
		return
	}
	sigs := make(chan os.Signal, 16)
	signal.Notify(sigs, unix.SIGCHLD)
	go func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-sigs:
			case <-tick.C:
			}
			reapOrphans("/proc", 1)
		}
	}()
}

// reapOrphans waits on every zombie child of parent that startChild did not
// register. It returns how many it reaped.
func reapOrphans(procRoot string, parent int) int {
	children.Lock()
	defer children.Unlock()
	n := 0
	for _, pid := range zombieChildren(procRoot, parent) {
		if _, ours := children.pids[pid]; ours {
			continue
		}
		var ws unix.WaitStatus
		if got, err := unix.Wait4(pid, &ws, unix.WNOHANG, nil); err == nil && got == pid {
			n++
		}
	}
	return n
}

// zombieChildren lists pids under procRoot in state Z whose parent is parent.
func zombieChildren(procRoot string, parent int) []int {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "stat"))
		if err != nil {
			continue
		}
		state, ppid, ok := parseStat(string(b))
		if ok && state == "Z" && ppid == parent {
			out = append(out, pid)
		}
	}
	return out
}

// parseStat reads the state and ppid from /proc/<pid>/stat. The command name
// is in parentheses and may itself contain spaces or ")".
func parseStat(s string) (string, int, bool) {
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return "", 0, false
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 2 {
		return "", 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return "", 0, false
	}
	return fields[0], ppid, true
}
