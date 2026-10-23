package core

import (
	"context"
	"sync"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/store"
)

// statsWorkers bounds concurrent engine calls during one /metrics scrape.
const statsWorkers = 8

// statsTimeout caps one sandbox's sample during a scrape.
const statsTimeout = 2 * time.Second

// Stats samples resource usage for a running or paused sandbox. It does not
// count as activity, so polling stats never keeps an idle sandbox alive.
func (m *Manager) Stats(ctx context.Context, actor Actor, sandboxID string) (store.Sandbox, driver.Stats, error) {
	sb, err := m.alive(ctx, actor, sandboxID)
	if err != nil {
		return store.Sandbox{}, driver.Stats{}, err
	}
	d, err := m.backend(sb)
	if err != nil {
		return store.Sandbox{}, driver.Stats{}, err
	}
	sr, ok := d.(driver.StatsReader)
	if !ok {
		return store.Sandbox{}, driver.Stats{}, unsupported(sb.Backend + " does not report resource stats")
	}
	st, err := sr.Stats(ctx, sb.ContainerID, true)
	if err != nil {
		return store.Sandbox{}, driver.Stats{}, m.asDriver(err)
	}
	return sb, st, nil
}

// SandboxStats is one sandbox's sample for /metrics.
type SandboxStats struct {
	Sandbox store.Sandbox
	Stats   driver.Stats
}

// SampleStats takes a cheap sample of every running or paused sandbox whose
// backend reports stats. Sandboxes that fail to sample are left out.
func (m *Manager) SampleStats(ctx context.Context) ([]SandboxStats, error) {
	var boxes []store.Sandbox
	for _, status := range []string{"running", "paused"} {
		list, err := m.store.ListSandboxes(ctx, store.SandboxFilter{Status: status})
		if err != nil {
			return nil, err
		}
		boxes = append(boxes, list...)
	}
	out := make([]*SandboxStats, len(boxes))
	sem := make(chan struct{}, statsWorkers)
	var wg sync.WaitGroup
	for i, sb := range boxes {
		sr, ok := m.drivers[sb.Backend].(driver.StatsReader)
		if !ok || sb.ContainerID == "" {
			continue
		}
		wg.Add(1)
		go func(i int, sb store.Sandbox) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sctx, cancel := context.WithTimeout(ctx, statsTimeout)
			defer cancel()
			st, err := sr.Stats(sctx, sb.ContainerID, false)
			if err != nil {
				return
			}
			out[i] = &SandboxStats{Sandbox: sb, Stats: st}
		}(i, sb)
	}
	wg.Wait()
	res := make([]SandboxStats, 0, len(out))
	for _, s := range out {
		if s != nil {
			res = append(res, *s)
		}
	}
	return res, nil
}
