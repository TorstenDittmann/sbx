// Package kata runs sandboxes with Kata Containers through the Docker runtime
// class. The runtime name defaults to "kata" and the runtime type defaults to
// io.containerd.kata.v2. Exec, files, terminals, and previews are the Docker
// driver's, including sbx-agent when it is configured.
package kata

import (
	"context"
	"fmt"
	"strings"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/docker"
	"github.com/TorstenDittmann/sbx/internal/probe"
)

// Options configures the Kata runtime class.
type Options struct {
	Host           string
	Runtime        string
	RuntimeType    string
	ReadOnlyRootfs bool
	UID            int
	GID            int
	MaxOutputBytes int
	AgentBin       string
	// SkipKVM is for tests that use a fake daemon.
	SkipKVM bool
}

// Driver is a Docker driver that refuses to start unless Kata is registered.
type Driver struct {
	*docker.Driver
	runtime     string
	runtimeType string
	skipKVM     bool
}

// New connects to Docker and selects the Kata runtime.
func New(opt Options) (*Driver, error) {
	if opt.Runtime == "" {
		opt.Runtime = "kata"
	}
	if opt.RuntimeType == "" {
		opt.RuntimeType = "io.containerd.kata.v2"
	}
	inner, err := docker.New(docker.Options{
		Host:           opt.Host,
		Name:           "kata",
		Runtime:        opt.Runtime,
		ReadOnlyRootfs: opt.ReadOnlyRootfs,
		UID:            opt.UID,
		GID:            opt.GID,
		MaxOutputBytes: opt.MaxOutputBytes,
		AgentBin:       opt.AgentBin,
	})
	if err != nil {
		return nil, err
	}
	return &Driver{Driver: inner, runtime: opt.Runtime, runtimeType: opt.RuntimeType, skipKVM: opt.SkipKVM}, nil
}

// NewWithDocker wraps an existing Docker driver. Tests use it.
func NewWithDocker(inner *docker.Driver, runtime, runtimeType string, skipKVM bool) *Driver {
	if runtime == "" {
		runtime = "kata"
	}
	if runtimeType == "" {
		runtimeType = "io.containerd.kata.v2"
	}
	return &Driver{Driver: inner, runtime: runtime, runtimeType: runtimeType, skipKVM: skipKVM}
}

func (d *Driver) Name() string { return "kata" }

func (d *Driver) Capabilities() driver.Caps {
	guest := d.GuestEnabled()
	return driver.Caps{
		Pause: true, NetPolicy: true, IsolationLevel: "kata",
		Exec: true, Files: true, PTY: guest, Watch: guest, Preview: guest,
		Networks:     []driver.NetMode{driver.NetNone, driver.NetFull},
		Requirements: []string{"docker", "kvm", d.runtimeType},
	}
}

// Check pings Docker, requires a writable /dev/kvm, and checks the runtime class.
func (d *Driver) Check(ctx context.Context) error {
	if !d.skipKVM {
		if ok, detail := probe.KVM(); !ok {
			return fmt.Errorf("%w: %s", driver.ErrRuntimeUnavailable, detail)
		}
	}
	if err := d.Ping(ctx); err != nil {
		return err
	}
	info, err := d.DaemonInfo(ctx)
	if err != nil {
		return err
	}
	rt, ok := info.RuntimeInfo[d.runtime]
	if !ok {
		return fmt.Errorf("%w: Docker has no %q runtime (sbx setup kata registers runtimeType %s; restart the daemon after editing daemon.json)", driver.ErrRuntimeUnavailable, d.runtime, d.runtimeType)
	}
	if !runtimeMatches(rt, d.runtimeType) {
		got := rt.RuntimeType
		if got == "" {
			got = rt.Path
		}
		return fmt.Errorf("%w: Docker runtime %q is %q, want runtimeType %s or a kata binary", driver.ErrRuntimeUnavailable, d.runtime, got, d.runtimeType)
	}
	return nil
}

func runtimeMatches(rt docker.Runtime, want string) bool {
	if rt.RuntimeType == want {
		return true
	}
	// The configured name is not proof. A runtime called kata can still point at runc.
	blob := strings.ToLower(rt.RuntimeType + " " + rt.Path)
	return strings.Contains(blob, "kata")
}

// Create rejects a missing Kata runtime before the container is started.
func (d *Driver) Create(ctx context.Context, spec driver.SandboxSpec) (driver.Handle, error) {
	if err := d.Check(ctx); err != nil {
		return driver.Handle{}, err
	}
	return d.Driver.Create(ctx, spec)
}

// Report describes whether Kata can start a sandbox on this host.
func (d *Driver) Report(ctx context.Context) driver.Info {
	err := d.Check(ctx)
	detail := d.runtime + " (" + d.runtimeType + ") is registered"
	if err != nil {
		detail = err.Error()
	}
	return driver.Describe("kata", d.Capabilities(), err == nil, detail)
}

var (
	_ driver.Driver   = (*Driver)(nil)
	_ driver.Guest    = (*Driver)(nil)
	_ driver.Reporter = (*Driver)(nil)
)
