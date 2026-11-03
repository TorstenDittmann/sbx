package sbx

import (
	"errors"
	"maps"
)

// Template is an immutable list of build steps on a base image or template.
// Every method returns a new Template, so one base can branch into variants.
// sbx builds the steps once, caches the image by content hash, and starts
// later sandboxes from it. Env and Workdir apply to later steps only; they
// are not set in sandboxes. /workspace is mounted over at runtime, so build
// into another directory such as /app.
//
// Methods that get an invalid argument return a Template whose Err is set;
// CreateSandbox and Build then fail with that error.
type Template struct {
	image    string
	template string
	steps    []BuildStep
	err      error
}

// FromImage starts a template from a base image such as node:22.
func FromImage(image string) *Template { return &Template{image: image} }

// FromTemplate starts a template from a named template. An empty name
// means the built-in base template.
func FromTemplate(name string) *Template {
	if name == "" {
		name = "base"
	}
	return &Template{template: name}
}

// Image is the base image, or empty when the template starts from a
// named template.
func (t *Template) Image() string { return t.image }

// BaseTemplate is the named template this one starts from, if any.
func (t *Template) BaseTemplate() string { return t.template }

// Steps returns a copy of the build steps.
func (t *Template) Steps() []BuildStep {
	out := make([]BuildStep, len(t.steps))
	copy(out, t.steps)
	return out
}

// Err reports the first invalid argument passed to a builder method.
func (t *Template) Err() error { return t.err }

func (t *Template) with(step BuildStep, err error) *Template {
	next := &Template{image: t.image, template: t.template, err: t.err}
	next.steps = append(t.Steps(), step)
	if next.err == nil {
		next.err = err
	}
	if err != nil {
		next.steps = t.Steps()
	}
	return next
}

// Run adds a shell command, run with /bin/sh -c as root.
func (t *Template) Run(command string) *Template {
	var err error
	if command == "" {
		err = errors.New("sbx: run command must not be empty")
	}
	return t.with(BuildStep{Run: command}, err)
}

// WithPackages installs system packages with apt-get, apk, dnf, or yum,
// whichever the base image has.
func (t *Template) WithPackages(names ...string) *Template {
	var err error
	if len(names) == 0 {
		err = errors.New("sbx: WithPackages needs at least one package")
	}
	return t.with(BuildStep{Packages: append([]string(nil), names...)}, err)
}

// WithEnv sets build-time variables for later steps. They are part of the
// content hash, so a change rebuilds. Pass runtime variables to
// CreateSandbox.
func (t *Template) WithEnv(vars map[string]string) *Template {
	var err error
	if len(vars) == 0 {
		err = errors.New("sbx: WithEnv needs at least one variable")
	}
	return t.with(BuildStep{Env: maps.Clone(vars)}, err)
}

// Workdir sets the working directory for later steps.
func (t *Template) Workdir(dir string) *Template {
	var err error
	if dir == "" {
		err = errors.New("sbx: workdir must not be empty")
	}
	return t.with(BuildStep{Workdir: dir}, err)
}
