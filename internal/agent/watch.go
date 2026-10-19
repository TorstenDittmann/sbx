package agent

import (
	"context"
	"time"
)

// pollWatch diffs directory snapshots until ctx ends. It is the fallback when
// inotify is unavailable (some gVisor builds) and the implementation on
// non-Linux hosts.
func (a *Agent) pollWatch(ctx context.Context, root string, recursive bool, ready func() error, emit func(FileEvent) error) error {
	prev, err := snapshot(root, recursive)
	if err != nil {
		return err
	}
	if err := ready(); err != nil {
		return err
	}
	every := a.watchEvery
	if every <= 0 {
		every = 200 * time.Millisecond
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			cur, err := snapshot(root, recursive)
			if err != nil {
				return err
			}
			for _, ev := range diffSnapshot(prev, cur) {
				if err := emit(ev); err != nil {
					return err
				}
			}
			prev = cur
		}
	}
}
