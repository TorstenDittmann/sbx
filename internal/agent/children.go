package agent

import (
	"os/exec"
	"sync"
)

// children holds the pids this agent started and will Wait for. The PID 1
// reaper skips them so os/exec still collects their exit status.
var children = struct {
	sync.Mutex
	pids map[int]struct{}
}{pids: map[int]struct{}{}}

// startChild starts cmd and registers its pid. The lock is held across Start
// so the reaper cannot see the child as an untracked zombie in between.
func startChild(cmd *exec.Cmd) error {
	children.Lock()
	defer children.Unlock()
	if err := cmd.Start(); err != nil {
		return err
	}
	children.pids[cmd.Process.Pid] = struct{}{}
	return nil
}

// waitChild waits for a child started with startChild and unregisters it.
func waitChild(cmd *exec.Cmd) error {
	err := cmd.Wait()
	children.Lock()
	delete(children.pids, cmd.Process.Pid)
	children.Unlock()
	return err
}
