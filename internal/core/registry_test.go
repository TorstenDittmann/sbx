package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
	"github.com/TorstenDittmann/sbx/internal/id"
	"github.com/TorstenDittmann/sbx/internal/store"
)

func registryKey(t *testing.T, st *store.Store, regs ...store.RegistryCred) Actor {
	t.Helper()
	_, hash, prefix := id.Key()
	key := store.APIKey{ID: id.RecordID(), Name: "reg", Hash: hash, Prefix: prefix, Scopes: []string{"admin"}, Registries: regs, CreatedAt: time.Now().UTC()}
	if err := st.CreateKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	return Actor{KeyID: key.ID, Admin: true}
}

// privateRegistry lets a pull of ghcr.io/acme/* through only with bot's password.
func privateRegistry(ref string, auth *driver.RegistryAuth) error {
	if !strings.HasPrefix(strings.ToLower(ref), "ghcr.io/acme/") {
		return nil
	}
	if auth == nil || auth.Username != "bot" || auth.Password != "s3cret" {
		return errors.New("unauthorized")
	}
	return nil
}

func TestKeyRegistryCredentialGatesSharedCache(t *testing.T) {
	st := openStore(t)
	fd := fake.New("docker")
	fd.Registry = privateRegistry
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()
	owner := registryKey(t, st, store.RegistryCred{Server: "ghcr.io", Username: "bot", Password: "s3cret"})
	other := registryKey(t, st)

	sb, err := mgr.Create(ctx, owner, CreateInput{Image: "ghcr.io/acme/app:1"})
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := fd.Spec(sb.ContainerID)
	if spec.Registry == nil || spec.Registry.Username != "bot" || !spec.ForcePull {
		t.Fatalf("%+v", spec)
	}
	// The daemon now has the image, but another key must still ask the
	// registry, by tag or by digest.
	for _, ref := range []string{"ghcr.io/acme/app:1", "ghcr.io/acme/app@sha256:0123", "GHCR.io/acme/app:latest"} {
		if _, err := mgr.Create(ctx, other, CreateInput{Image: ref}); err == nil {
			t.Fatalf("%s: created without registry access", ref)
		}
	}
	// The owner still can, again with its credential.
	if _, err := mgr.Create(ctx, owner, CreateInput{Image: "ghcr.io/acme/app:1"}); err != nil {
		t.Fatal(err)
	}
	// An image ID would skip the registry, so it is refused now.
	if _, err := mgr.Create(ctx, other, CreateInput{Image: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}); err == nil {
		t.Fatal("image id accepted")
	}
	// Public images are untouched.
	pub, err := mgr.Create(ctx, other, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	if spec, _ := fd.Spec(pub.ContainerID); spec.Registry != nil || spec.ForcePull {
		t.Fatalf("%+v", spec)
	}
}

func TestServerRegistryCredential(t *testing.T) {
	st := openStore(t)
	fd := fake.New("docker")
	fd.Registry = privateRegistry
	cfg := config.Default()
	cfg.Registries = []config.Registry{{Server: "ghcr.io", Username: "bot", Password: "s3cret"}}
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()
	anyKey := registryKey(t, st)

	sb, err := mgr.Create(ctx, anyKey, CreateInput{Image: "ghcr.io/acme/app:1"})
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := fd.Spec(sb.ContainerID)
	if spec.Registry == nil || spec.Registry.Username != "bot" || spec.ForcePull {
		t.Fatalf("%+v", spec)
	}

	// A key's own credential wins over the server's.
	own := registryKey(t, st, store.RegistryCred{Server: "ghcr.io", Username: "mine", Password: "x"})
	if _, err := mgr.Create(ctx, own, CreateInput{Image: "ghcr.io/acme/other:1"}); err == nil {
		t.Fatal("expected the key's wrong password to be used")
	}
	pulls := fd.Pulls()
	if last := pulls[len(pulls)-1]; last.Auth == nil || last.Auth.Username != "mine" {
		t.Fatalf("%+v", last)
	}
}

func TestBuildFromPrivateBase(t *testing.T) {
	st := openStore(t)
	fd := fake.New("docker")
	fd.Registry = privateRegistry
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()
	owner := registryKey(t, st, store.RegistryCred{Server: "ghcr.io", Username: "bot", Password: "s3cret"})
	other := registryKey(t, st)
	steps := []store.BuildStep{{Run: "make"}}

	built, err := mgr.Build(ctx, owner, BuildInput{Image: "ghcr.io/acme/base:1", Steps: steps})
	if err != nil {
		t.Fatal(err)
	}
	b := fd.Builds()
	if len(b) != 1 || b[0].Registry == nil || b[0].Registry.Username != "bot" || !b[0].ForcePull {
		t.Fatalf("%+v", b)
	}
	// Same base and steps hit the cache, but the base is checked first.
	if _, err := mgr.Create(ctx, other, CreateInput{Image: "ghcr.io/acme/base:1", Steps: steps}); err == nil {
		t.Fatal("cached build used without registry access")
	}
	// Naming the built image directly is checked the same way.
	if _, err := mgr.Create(ctx, other, CreateInput{Image: built.Image}); err == nil {
		t.Fatal("built image used without registry access")
	}
	if _, err := mgr.Create(ctx, other, CreateInput{Image: "docker.io/library/" + built.Image}); err == nil {
		t.Fatal("built image used through another spelling")
	}
	if _, err := mgr.Create(ctx, owner, CreateInput{Image: built.Image}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create(ctx, owner, CreateInput{Image: "ghcr.io/acme/base:1", Steps: steps}); err != nil {
		t.Fatal(err)
	}
	if len(fd.Builds()) != 1 {
		t.Fatalf("rebuilt: %+v", fd.Builds())
	}
}

func TestWarmPoolUsesServerCredentialOnly(t *testing.T) {
	st := openStore(t)
	fd := fake.New("docker")
	fd.Registry = privateRegistry
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()
	owner := registryKey(t, st, store.RegistryCred{Server: "ghcr.io", Username: "bot", Password: "s3cret"})
	if _, err := mgr.Create(ctx, owner, CreateInput{Image: "ghcr.io/acme/app:1"}); err != nil {
		t.Fatal(err)
	}
	// The pool belongs to no key, so it cannot use the owner's credential.
	if _, err := mgr.CreateTemplate(ctx, owner, TemplateInput{Name: "priv", Image: "ghcr.io/acme/app:1", Warm: 1}); err == nil {
		t.Fatal("warm pool filled from a key-only image")
	}
}

func TestNormalizeRegistries(t *testing.T) {
	out, err := NormalizeRegistries([]store.RegistryCred{
		{Server: "https://index.docker.io/v1/", Username: "u", Password: "p"},
		{Server: "GHCR.io", IdentityToken: "t"},
	})
	if err != nil || out[0].Server != "docker.io" || out[1].Server != "ghcr.io" {
		t.Fatalf("%+v %v", out, err)
	}
	for _, bad := range [][]store.RegistryCred{
		{{Username: "u", Password: "p"}},
		{{Server: "ghcr.io", Username: "u"}},
		{{Server: "ghcr.io", IdentityToken: "t"}, {Server: "https://ghcr.io", IdentityToken: "t"}},
	} {
		if _, err := NormalizeRegistries(bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
