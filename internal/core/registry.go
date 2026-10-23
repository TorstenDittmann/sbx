package core

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/store"
)

const maxKeyRegistries = 16

// builtTagRE matches a build's content hash or a snapshot id.
var builtTagRE = regexp.MustCompile(`^` + buildImageRepo + `:([0-9a-f]{32}|snap_[0-9a-f]{16})$`)

// imageAccess is how a backend fetches one image: with which credential,
// and whether it must ask the registry even when the daemon has a copy.
type imageAccess struct {
	auth  *driver.RegistryAuth
	force bool
}

// resolveImage picks the credential for ref, the caller's key before the
// server's. The Docker daemon shares pulled images between every caller, so
// an image first fetched with a key's own credential is marked private, and
// every later use pulls it again with the caller's credential. A caller
// without access then fails at the registry instead of reading the cache.
func (m *Manager) resolveImage(ctx context.Context, actor Actor, backend, ref string) (imageAccess, error) {
	if strings.HasPrefix(ref, "ext4:") {
		return imageAccess{}, nil
	}
	if driver.IsImageID(ref) {
		has, err := m.store.HasPrivateImages(ctx)
		if err != nil {
			return imageAccess{}, err
		}
		if has {
			return imageAccess{}, invalid("name the image by repository: an image ID cannot be checked against registry credentials")
		}
		return imageAccess{}, nil
	}
	repo := driver.Repository(ref)
	if repo == driver.Repository(buildImageRepo) {
		return imageAccess{}, m.checkBuilt(ctx, actor, backend, ref)
	}
	host := driver.RegistryHost(ref)
	_, private, err := m.store.PrivateImage(ctx, repo)
	if err != nil {
		return imageAccess{}, err
	}
	var acc imageAccess
	if cred, ok, err := m.keyRegistry(ctx, actor, host); err != nil {
		return imageAccess{}, err
	} else if ok {
		acc.auth = &cred
		if !private && !m.haveImage(ctx, backend, ref) {
			// Mark before the pull, so a concurrent create by another key
			// already has to ask the registry.
			if err := m.store.MarkPrivateImage(ctx, repo, "", m.now()); err != nil {
				return imageAccess{}, err
			}
			private = true
		}
	} else if r, ok := m.cfg.Registry(host); ok {
		acc.auth = &driver.RegistryAuth{Server: r.Server, Username: r.Username, Password: r.Password, IdentityToken: r.IdentityToken}
	}
	acc.force = private
	return acc, nil
}

// checkBuilt lets a caller name a built image directly only if it may still
// pull the private base the image was built from.
func (m *Manager) checkBuilt(ctx context.Context, actor Actor, backend, ref string) error {
	if !builtTagRE.MatchString(ref) {
		return invalid("name a built image by the image value a build returned")
	}
	base, private, err := m.store.PrivateImage(ctx, ref)
	if err != nil || !private {
		return err
	}
	acc, err := m.resolveImage(ctx, actor, backend, base)
	if err != nil {
		return err
	}
	return m.verifyImage(ctx, backend, base, acc)
}

// verifyImage asks the registry for ref when acc says the cache is not enough.
func (m *Manager) verifyImage(ctx context.Context, backend, ref string, acc imageAccess) error {
	if !acc.force {
		return nil
	}
	b, err := m.builder(backend)
	if err != nil {
		return err
	}
	if err := b.PullImage(ctx, ref, acc.auth); err != nil {
		return m.asDriver(err)
	}
	return nil
}

func (m *Manager) haveImage(ctx context.Context, backend, ref string) bool {
	b, err := m.builder(backend)
	if err != nil {
		return false
	}
	ok, err := b.HasImage(ctx, ref)
	return err == nil && ok
}

func (m *Manager) keyRegistry(ctx context.Context, actor Actor, host string) (driver.RegistryAuth, bool, error) {
	if actor.KeyID == "" {
		return driver.RegistryAuth{}, false, nil
	}
	key, err := m.store.KeyByID(ctx, actor.KeyID)
	if errors.Is(err, store.ErrNotFound) {
		return driver.RegistryAuth{}, false, nil
	}
	if err != nil {
		return driver.RegistryAuth{}, false, err
	}
	r, ok := key.Registry(host)
	if !ok {
		return driver.RegistryAuth{}, false, nil
	}
	return driver.RegistryAuth{Server: r.Server, Username: r.Username, Password: r.Password, IdentityToken: r.IdentityToken}, true, nil
}

// NormalizeRegistries validates key registry credentials and normalizes
// their servers.
func NormalizeRegistries(in []store.RegistryCred) ([]store.RegistryCred, error) {
	if len(in) > maxKeyRegistries {
		return nil, fmtInvalid("a key has at most %d registries", maxKeyRegistries)
	}
	out := make([]store.RegistryCred, 0, len(in))
	seen := map[string]bool{}
	for i, r := range in {
		r.Server = driver.NormalizeRegistry(r.Server)
		if r.Server == "" || strings.ContainsAny(r.Server, " \t\r\n") {
			return nil, fmtInvalid("registries[%d]: server is required", i)
		}
		if seen[r.Server] {
			return nil, fmtInvalid("registries[%d]: %s is listed twice", i, r.Server)
		}
		seen[r.Server] = true
		if r.IdentityToken == "" && (r.Username == "" || r.Password == "") {
			return nil, fmtInvalid("registries[%d]: %s needs username and password, or identity_token", i, r.Server)
		}
		if len(r.Username)+len(r.Password)+len(r.IdentityToken) > 8192 {
			return nil, fmtInvalid("registries[%d]: credential is too long", i)
		}
		out = append(out, r)
	}
	return out, nil
}
