package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
	"github.com/TorstenDittmann/sbx/internal/store"
)

func TestRenderStepsCarriesEnvAndWorkdir(t *testing.T) {
	scripts := renderSteps([]store.BuildStep{
		{Run: "echo first"},
		{Env: map[string]string{"B": "it's", "A": "1"}},
		{Workdir: "/app/"},
		{Packages: []string{"git", "ffmpeg"}},
		{Run: "npm install"},
		{Env: map[string]string{"A": "2"}},
	})
	if len(scripts) != 3 {
		t.Fatalf("%d scripts: %q", len(scripts), scripts)
	}
	if scripts[0] != "echo first" {
		t.Fatalf("first %q", scripts[0])
	}
	prefix := "export A='1'\nexport B='it'\\''s'\nmkdir -p '/app' && cd '/app' || exit 1\n"
	if !strings.HasPrefix(scripts[1], prefix) || !strings.Contains(scripts[1], "apt-get install -y --no-install-recommends git ffmpeg") || !strings.Contains(scripts[1], "apk add --no-cache git ffmpeg") {
		t.Fatalf("packages %q", scripts[1])
	}
	if scripts[2] != prefix+"npm install" {
		t.Fatalf("run %q", scripts[2])
	}
}

func TestBuildTagIsContentAddressed(t *testing.T) {
	a := buildTag("node:22", []string{"npm install"})
	if a != buildTag("node:22", []string{"npm install"}) {
		t.Fatal("same input, different tag")
	}
	if !strings.HasPrefix(a, "sbx-template:") || len(a) != len("sbx-template:")+32 {
		t.Fatal(a)
	}
	for _, other := range []string{
		buildTag("node:20", []string{"npm install"}),
		buildTag("node:22", []string{"npm ci"}),
		buildTag("node:22", []string{"npm", "install"}),
	} {
		if other == a {
			t.Fatal("different input, same tag")
		}
	}
}

func TestValidSteps(t *testing.T) {
	bad := [][]store.BuildStep{
		{{}},
		{{Run: "a", Workdir: "/app"}},
		{{Packages: []string{}}},
		{{Packages: []string{"git; rm -rf /"}}},
		{{Env: map[string]string{}}},
		{{Env: map[string]string{"1X": "y"}}},
		{{Workdir: "relative"}},
		{{Workdir: "/workspace/app"}},
		{{Run: "a\x00b"}},
	}
	for i, steps := range bad {
		if err := validSteps(steps); err == nil {
			t.Errorf("case %d: expected error for %+v", i, steps)
		}
	}
	ok := []store.BuildStep{
		{Packages: []string{"python3-pip", "libc6=2.36-9", "g++"}},
		{Env: map[string]string{"NODE_ENV": "production"}},
		{Workdir: "/app"},
		{Run: "npm install"},
	}
	if err := validSteps(ok); err != nil {
		t.Fatal(err)
	}
}

func TestCreateBuildsOnceAndReusesImage(t *testing.T) {
	cfg := config.Default()
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()
	admin := Actor{KeyID: coreActor(t, st).KeyID, Admin: true}
	steps := []store.BuildStep{{Packages: []string{"git"}}, {Run: "git --version"}}

	first, err := mgr.Create(ctx, admin, CreateInput{Image: "alpine:3.20", Steps: steps})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.Image, "sbx-template:") {
		t.Fatalf("image %q", first.Image)
	}
	second, err := mgr.Create(ctx, admin, CreateInput{Image: "alpine:3.20", Steps: steps})
	if err != nil {
		t.Fatal(err)
	}
	if second.Image != first.Image {
		t.Fatalf("%q != %q", second.Image, first.Image)
	}
	builds := fd.Builds()
	if len(builds) != 1 || builds[0].Base != "alpine:3.20" || builds[0].Tag != first.Image || len(builds[0].Run) != 2 {
		t.Fatalf("builds %+v", builds)
	}
	if mgr.Counts.Builds.Load() != 1 {
		t.Fatal(mgr.Counts.Builds.Load())
	}

	// Env alone renders no command, so the base image is used as is.
	plain, err := mgr.Create(ctx, admin, CreateInput{Image: "alpine:3.20", Steps: []store.BuildStep{{Env: map[string]string{"A": "b"}}}})
	if err != nil || plain.Image != "alpine:3.20" {
		t.Fatalf("%q %v", plain.Image, err)
	}
}

func TestInlineStepsNeedAdminUnlessAllowed(t *testing.T) {
	cfg := config.Default()
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()
	user := coreActor(t, st)
	steps := []store.BuildStep{{Run: "true"}}

	_, err := mgr.Create(ctx, user, CreateInput{Image: "alpine:3.20", Steps: steps})
	var op *OpError
	if !errors.As(err, &op) || op.Status != 403 {
		t.Fatalf("err = %v", err)
	}
	if _, err := mgr.Build(ctx, user, BuildInput{Image: "alpine:3.20", Steps: steps}); !errors.As(err, &op) || op.Status != 403 {
		t.Fatalf("build err = %v", err)
	}

	cfg.Builds.AllowSandboxKeys = true
	mgr = New(cfg, st, map[string]driver.Driver{"docker": fd}, discard())
	if _, err := mgr.Create(ctx, user, CreateInput{Image: "alpine:3.20", Steps: steps}); err != nil {
		t.Fatal(err)
	}
}

func TestTemplateStepsBuildAtCreateAndExtend(t *testing.T) {
	cfg := config.Default()
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()
	user := coreActor(t, st)

	fd.BuildErr = errors.New("exit 127")
	if _, err := mgr.CreateTemplate(ctx, TemplateInput{Name: "broken", Image: "node:22", Steps: []store.BuildStep{{Run: "nope"}}}); err == nil {
		t.Fatal("expected build failure")
	}
	if _, err := st.GetTemplate(ctx, "broken"); err != store.ErrNotFound {
		t.Fatal(err)
	}
	fd.BuildErr = nil

	tpl, err := mgr.CreateTemplate(ctx, TemplateInput{
		Name: "app", Image: "node:22", Warm: 1,
		Steps: []store.BuildStep{{Workdir: "/app"}, {Run: "npm install"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetTemplate(ctx, "app")
	if err != nil || len(got.Steps) != 2 || got.Steps[0].Workdir != "/app" {
		t.Fatalf("%+v %v", got, err)
	}
	pool, err := st.ListPool(ctx, "app")
	if err != nil || len(pool) != 1 || !strings.HasPrefix(pool[0].Image, "sbx-template:") {
		t.Fatalf("pool %+v %v", pool, err)
	}
	// One failed build for "broken", then one for "app" shared by the pool.
	if len(fd.Builds()) != 2 {
		t.Fatalf("builds %d", len(fd.Builds()))
	}

	// A sandbox key may use a template with steps; the image is already built.
	sb, err := mgr.Create(ctx, user, CreateInput{Template: tpl.Name})
	if err != nil || sb.ID != pool[0].ID {
		t.Fatalf("%+v %v", sb, err)
	}

	// Overriding the base of a template with steps is a new build.
	var op *OpError
	if _, err := mgr.Create(ctx, user, CreateInput{Template: "app", Image: "evil/image:1"}); !errors.As(err, &op) || op.Status != 403 {
		t.Fatalf("image override err = %v", err)
	}
	if _, err := mgr.Create(ctx, user, CreateInput{Template: "app", Image: "node:22"}); err != nil {
		t.Fatal(err)
	}

	// Request steps never take a pooled sandbox built without them.
	if err := mgr.fillTemplate(ctx, tpl); err != nil {
		t.Fatal(err)
	}
	pool, err = st.ListPool(ctx, "app")
	if err != nil || len(pool) == 0 {
		t.Fatalf("pool %+v %v", pool, err)
	}
	cold, err := mgr.Create(ctx, Actor{KeyID: user.KeyID, Admin: true}, CreateInput{Template: "app", Steps: []store.BuildStep{{Run: "npm run build"}}})
	if err != nil || cold.Image == pool[0].Image {
		t.Fatalf("%+v %v", cold, err)
	}
	for _, p := range pool {
		if cold.ID == p.ID {
			t.Fatalf("create with steps claimed pooled sandbox %s", p.ID)
		}
	}

	res, err := mgr.Build(ctx, Actor{Admin: true}, BuildInput{Template: "app", Steps: []store.BuildStep{{Run: "npm test"}}})
	if err != nil || res.Cached || res.Base != "node:22" || len(res.Steps) != 3 || res.Image != "sbx-template:"+res.ID {
		t.Fatalf("%+v %v", res, err)
	}
	again, err := mgr.Build(ctx, Actor{Admin: true}, BuildInput{Template: "app", Steps: []store.BuildStep{{Run: "npm test"}}})
	if err != nil || !again.Cached || again.Image != res.Image {
		t.Fatalf("%+v %v", again, err)
	}
	last := fd.Builds()[len(fd.Builds())-1]
	if last.Run[1] != "mkdir -p '/app' && cd '/app' || exit 1\nnpm test" {
		t.Fatalf("%q", last.Run)
	}
}
