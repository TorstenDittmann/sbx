package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/store"
)

// buildFormat is part of every build hash. Change it when the rendered
// scripts change shape, so old cached images are not reused.
const buildFormat = "sbx-build-v1"

const (
	maxBuildSteps  = 64
	maxRunBytes    = 64 * 1024
	maxPackages    = 64
	buildImageRepo = "sbx-template"
)

var packageRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_:=~@/-]{0,127}$`)

// BuildInput builds an image from a base and steps. Template, when set,
// supplies the base image and runs its steps before Steps.
type BuildInput struct {
	Template string
	Image    string
	Steps    []store.BuildStep
	Backend  string
}

// BuildResult is a cached image. ID is the content hash of the base and the
// rendered steps. Image is what sandboxes start from.
type BuildResult struct {
	ID       string
	Image    string
	Base     string
	Steps    []store.BuildStep
	Cached   bool
	Duration time.Duration
}

// Build builds and caches an image ahead of a create.
func (m *Manager) Build(ctx context.Context, actor Actor, in BuildInput) (BuildResult, error) {
	if err := m.canBuild(actor); err != nil {
		return BuildResult{}, err
	}
	var tpl store.Template
	if in.Template != "" {
		var err error
		if tpl, err = m.template(ctx, in.Template); err != nil {
			return BuildResult{}, err
		}
	}
	base := in.Image
	if base == "" {
		base = tpl.Image
	}
	if err := validImage(base); err != nil {
		return BuildResult{}, err
	}
	steps := joinSteps(tpl.Steps, in.Steps)
	if err := validSteps(steps); err != nil {
		return BuildResult{}, err
	}
	backend := in.Backend
	if backend == "" {
		backend = tpl.Backend
	}
	if backend == "" {
		backend = m.cfg.Backend
	}
	if _, ok := m.drivers[backend]; !ok {
		return BuildResult{}, invalid("unknown backend " + backend)
	}
	start := m.now()
	image, cached, err := m.buildImage(ctx, backend, base, steps)
	if err != nil {
		return BuildResult{}, err
	}
	id := ""
	if image != base {
		id = strings.TrimPrefix(image, buildImageRepo+":")
	}
	return BuildResult{
		ID: id, Image: image, Base: base, Steps: steps, Cached: cached, Duration: m.now().Sub(start),
	}, nil
}

func (m *Manager) template(ctx context.Context, name string) (store.Template, error) {
	if err := validTemplateName(name); err != nil {
		return store.Template{}, err
	}
	tpl, err := m.store.GetTemplate(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return store.Template{}, notFound("template not found")
	}
	return tpl, err
}

func (m *Manager) canBuild(actor Actor) error {
	if actor.Admin || m.cfg.Builds.AllowSandboxKeys {
		return nil
	}
	return forbidden("building an image needs an admin key unless builds.allow_sandbox_keys is set")
}

// buildImage returns the image for base plus steps, building it on first use.
// Steps that render no commands (only env or workdir) leave base as is.
func (m *Manager) buildImage(ctx context.Context, backend, base string, steps []store.BuildStep) (string, bool, error) {
	scripts := renderSteps(steps)
	if len(scripts) == 0 {
		return base, true, nil
	}
	tag := buildTag(base, scripts)
	b, err := m.builder(backend)
	if err != nil {
		return "", false, err
	}
	mu := m.buildLock(tag)
	mu.Lock()
	defer mu.Unlock()
	ok, err := b.HasImage(ctx, tag)
	if err != nil {
		return "", false, m.asDriver(err)
	}
	if ok {
		return tag, true, nil
	}
	m.log.Info("building template image", "image", tag, "base", base, "steps", len(scripts))
	// A client that disconnects should not cancel a build other creates wait on.
	err = b.BuildImage(context.WithoutCancel(ctx), driver.BuildSpec{
		Tag: tag, Base: base, Run: scripts, TimeoutSeconds: m.cfg.Builds.TimeoutSeconds,
	})
	if err != nil {
		return "", false, m.asDriver(err)
	}
	m.Counts.Builds.Add(1)
	return tag, false, nil
}

// builder prefers the sandbox backend. Firecracker cannot build, but it reads
// images from the same Docker daemon, so any Docker-based backend will do.
func (m *Manager) builder(backend string) (driver.Builder, error) {
	if b, ok := m.drivers[backend].(driver.Builder); ok {
		return b, nil
	}
	for _, name := range driver.Known() {
		if b, ok := m.drivers[name].(driver.Builder); ok {
			return b, nil
		}
	}
	return nil, unsupported("no backend on this server can build images")
}

func (m *Manager) buildLock(tag string) *sync.Mutex {
	v, _ := m.buildLocks.LoadOrStore(tag, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func joinSteps(a, b []store.BuildStep) []store.BuildStep {
	out := make([]store.BuildStep, 0, len(a)+len(b))
	out = append(out, a...)
	return append(out, b...)
}

func validSteps(steps []store.BuildStep) error {
	if len(steps) > maxBuildSteps {
		return fmtInvalid("a template has at most %d steps", maxBuildSteps)
	}
	for i, st := range steps {
		set := 0
		if st.Run != "" {
			set++
			if len(st.Run) > maxRunBytes || strings.ContainsRune(st.Run, 0) {
				return fmtInvalid("step %d: run must be at most %d bytes and free of NUL", i, maxRunBytes)
			}
		}
		if st.Packages != nil {
			set++
			if len(st.Packages) == 0 || len(st.Packages) > maxPackages {
				return fmtInvalid("step %d: packages must list 1 to %d names", i, maxPackages)
			}
			for _, p := range st.Packages {
				if !packageRE.MatchString(p) {
					return fmtInvalid("step %d: invalid package name %q", i, p)
				}
			}
		}
		if st.Env != nil {
			set++
			if len(st.Env) == 0 {
				return fmtInvalid("step %d: env must set at least one variable", i)
			}
			if err := validEnv(st.Env); err != nil {
				return fmtInvalid("step %d: %s", i, err.Error())
			}
		}
		if st.Workdir != "" {
			set++
			dir, err := cleanPath(st.Workdir)
			if err != nil {
				return fmtInvalid("step %d: workdir: %s", i, err.Error())
			}
			if dir == "/workspace" || strings.HasPrefix(dir, "/workspace/") {
				return fmtInvalid("step %d: /workspace is mounted over at runtime, so build into another directory such as /app", i)
			}
		}
		if set != 1 {
			return fmtInvalid("step %d must set exactly one of run, packages, env, or workdir", i)
		}
	}
	return nil
}

// renderSteps turns steps into shell scripts. env and workdir carry forward
// into every later script instead of becoming image config, so build-time
// variables are not baked into sandboxes.
func renderSteps(steps []store.BuildStep) []string {
	env := map[string]string{}
	dir := ""
	var out []string
	for _, st := range steps {
		switch {
		case st.Env != nil:
			for k, v := range st.Env {
				env[k] = v
			}
		case st.Workdir != "":
			dir, _ = cleanPath(st.Workdir)
		case st.Packages != nil:
			out = append(out, scriptPrefix(env, dir)+installScript(st.Packages))
		case st.Run != "":
			out = append(out, scriptPrefix(env, dir)+st.Run)
		}
	}
	return out
}

func scriptPrefix(env map[string]string, dir string) string {
	var b strings.Builder
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("export " + k + "=" + shellQuote(env[k]) + "\n")
	}
	if dir != "" {
		q := shellQuote(dir)
		b.WriteString("mkdir -p " + q + " && cd " + q + " || exit 1\n")
	}
	return b.String()
}

// installScript uses whichever package manager the base image has.
func installScript(pkgs []string) string {
	list := strings.Join(pkgs, " ")
	return "if command -v apt-get >/dev/null 2>&1; then\n" +
		"  apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends " + list + " && rm -rf /var/lib/apt/lists/*\n" +
		"elif command -v apk >/dev/null 2>&1; then\n" +
		"  apk add --no-cache " + list + "\n" +
		"elif command -v dnf >/dev/null 2>&1; then\n" +
		"  dnf install -y " + list + " && dnf clean all\n" +
		"elif command -v yum >/dev/null 2>&1; then\n" +
		"  yum install -y " + list + " && yum clean all\n" +
		"else\n" +
		"  echo 'sbx: the base image has no apt-get, apk, dnf, or yum' >&2\n" +
		"  exit 1\n" +
		"fi"
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func buildTag(base string, scripts []string) string {
	h := sha256.New()
	h.Write([]byte(buildFormat))
	h.Write([]byte{0})
	h.Write([]byte(base))
	for _, s := range scripts {
		h.Write([]byte{0})
		h.Write([]byte(s))
	}
	return buildImageRepo + ":" + hex.EncodeToString(h.Sum(nil))[:32]
}
