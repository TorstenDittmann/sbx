//go:build !linux

package agent

import "context"

func (a *Agent) watchFiles(ctx context.Context, root string, recursive bool, ready func() error, emit func(FileEvent) error) error {
	return a.pollWatch(ctx, root, recursive, ready, emit)
}
