package core

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/store"
)

// imageGCInterval is how often the reaper looks for unused built images.
const imageGCInterval = time.Hour

// CollectImages removes built images that no template or live sandbox
// references and that no create or build has used for
// builds.retain_unused_seconds. An image with no recorded use counts from
// when it was built. It returns how many images it removed.
func (m *Manager) CollectImages(ctx context.Context) (int, error) {
	retain := time.Duration(m.cfg.Builds.RetainUnusedSeconds) * time.Second
	if retain <= 0 {
		return 0, nil
	}
	keep, err := m.referencedImages(ctx)
	if err != nil {
		return 0, err
	}
	names := make([]string, 0, len(m.drivers))
	for name := range m.drivers {
		names = append(names, name)
	}
	sort.Strings(names)
	seen := map[string]bool{}
	n := 0
	var first error
	for _, name := range names {
		c, ok := m.drivers[name].(driver.ImageCollector)
		if !ok {
			continue
		}
		list, err := c.ListBuiltImages(ctx, buildImageRepo)
		if err != nil {
			m.log.Warn("list built images failed", "backend", name, "err", err)
			if first == nil {
				first = err
			}
			continue
		}
		for _, img := range list {
			if keep[img.Ref] || seen[img.Ref] {
				continue
			}
			seen[img.Ref] = true
			removed, err := m.collectImage(ctx, c, img, m.now().Add(-retain))
			if err != nil {
				m.log.Warn("remove built image failed", "image", img.Ref, "err", err)
				if first == nil {
					first = err
				}
				continue
			}
			if removed {
				n++
				m.Counts.ImagesGCed.Add(1)
				m.log.Info("removed unused template image", "image", img.Ref, "bytes", img.Size)
			}
		}
	}
	return n, first
}

// collectImage removes img if its last use is before cutoff. It holds the
// tag's build lock and rereads the last use, so a create that just picked
// the image keeps it.
func (m *Manager) collectImage(ctx context.Context, c driver.ImageCollector, img driver.BuiltImage, cutoff time.Time) (bool, error) {
	mu := m.buildLock(img.Ref)
	mu.Lock()
	defer mu.Unlock()
	last, ok, err := m.store.ImageLastUsed(ctx, img.Ref)
	if err != nil {
		return false, err
	}
	if !ok {
		last = img.Created
	}
	if last.After(cutoff) {
		return false, nil
	}
	if err := c.RemoveImage(ctx, img.Ref); err != nil {
		if errors.Is(err, driver.ErrImageInUse) {
			return false, nil
		}
		return false, err
	}
	return true, m.store.ForgetImage(ctx, img.Ref)
}

// referencedImages is every built image a template or a live sandbox needs.
func (m *Manager) referencedImages(ctx context.Context) (map[string]bool, error) {
	keep := map[string]bool{}
	tpls, err := m.store.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	for _, tpl := range tpls {
		// A template saved from a snapshot names its image directly, and
		// a snapshot cannot be rebuilt.
		if strings.HasPrefix(tpl.Image, buildImageRepo+":") {
			keep[tpl.Image] = true
		}
		if scripts := renderSteps(tpl.Steps); len(scripts) > 0 {
			keep[buildTag(tpl.Image, scripts)] = true
		}
	}
	rows, err := m.store.ListSandboxes(ctx, store.SandboxFilter{})
	if err != nil {
		return nil, err
	}
	for _, sb := range rows {
		if sb.Status == "creating" || sb.Status == "running" || sb.Status == "paused" {
			keep[sb.Image] = true
		}
	}
	return keep, nil
}
