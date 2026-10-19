// Package firecracker runs sandboxes as Firecracker microVMs.
//
// Each sandbox gets an ext4 rootfs built from an OCI image, a guest kernel,
// and sbx-agent as init. The host talks to the agent through Firecracker's
// vsock socket. network=none attaches no NIC. network=full and network=allow
// use a tap device, NAT, and the same blocked metadata ranges as the Docker
// backend. Snapshot and restore are not implemented.
package firecracker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/TorstenDittmann/sbx/internal/agent"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/probe"
)

const bootArgs = "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw rootwait init=/opt/sbx/sbx-agent"

var idRE = regexp.MustCompile(`^[A-Za-z0-9_]{4,64}$`)

// Options configures the microVM backend.
type Options struct {
	Bin            string
	Kernel         string
	Jailer         string
	DataDir        string
	DockerHost     string
	AgentBin       string
	UID            int
	GID            int
	MaxOutputBytes int
	// SkipKVM is for tests. Production create fails without /dev/kvm.
	SkipKVM bool
	// Build replaces OCI-to-ext4 conversion. Tests set it.
	Build func(context.Context, RootSpec) error
	// Spawn replaces the firecracker process. Tests set it.
	Spawn func(SpawnSpec) (Process, error)
	// SetupNet and TeardownNet replace the host tap script.
	SetupNet    func(context.Context, NetSpec) error
	TeardownNet func(context.Context, NetSpec) error
	// Lookup resolves allow-list names. Nil uses the system resolver.
	Lookup func(context.Context, string) ([]netip.Addr, error)
}

// Driver implements driver.Driver for Firecracker.
type Driver struct {
	opt Options

	mu       sync.Mutex
	vms      map[string]*vm
	nextCID  uint32
	nextNet  int
	sessions map[string]*agent.Client
	locks    map[string]*sync.Mutex
}

type vm struct {
	rec  record
	proc Process
}

type record struct {
	ID        string     `json:"id"`
	Image     string     `json:"image"`
	Token     string     `json:"token"`
	APISocket string     `json:"api_socket"`
	Vsock     string     `json:"vsock"`
	PID       int        `json:"pid"`
	CID       uint32     `json:"cid"`
	Kernel    string     `json:"kernel"`
	Rootfs    string     `json:"rootfs"`
	Log       string     `json:"log"`
	State     string     `json:"state"`
	Net       *netRecord `json:"net,omitempty"`
	JailRoot  string     `json:"jail_root,omitempty"`
	NetIndex  int        `json:"net_index,omitempty"`
}

type netRecord struct {
	Tap       string   `json:"tap"`
	HostCIDR  string   `json:"host_cidr"`
	GuestIP   string   `json:"guest_ip"`
	GuestCIDR string   `json:"guest_cidr"`
	Gateway   string   `json:"gateway"`
	Mode      string   `json:"mode"`
	Allow     []string `json:"allow,omitempty"`
}

// New prepares the driver and reloads VMs left on disk.
func New(opt Options) (*Driver, error) {
	if opt.DataDir == "" {
		return nil, fmt.Errorf("data dir is required")
	}
	if opt.UID == 0 {
		opt.UID = 123
	}
	if opt.GID == 0 {
		opt.GID = 123
	}
	if opt.MaxOutputBytes == 0 {
		opt.MaxOutputBytes = 8 << 20
	}
	d := &Driver{
		opt:      opt,
		vms:      map[string]*vm{},
		nextCID:  3,
		sessions: map[string]*agent.Client{},
		locks:    map[string]*sync.Mutex{},
	}
	if err := d.load(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Driver) Name() string { return "firecracker" }

func (d *Driver) Capabilities() driver.Caps {
	return driver.Caps{
		Pause: true, NetPolicy: true, IsolationLevel: "firecracker",
		Exec: true, Files: true, PTY: true, Watch: true, Preview: true,
		Networks:     []driver.NetMode{driver.NetNone, driver.NetFull},
		Requirements: []string{"kvm", "firecracker", "kernel", "sbx-agent"},
	}
}

func (d *Driver) GuestEnabled() bool { return d.opt.AgentBin != "" }

// Check reports whether this host can boot a microVM.
func (d *Driver) Check(context.Context) error {
	if !d.opt.SkipKVM {
		if ok, detail := probe.KVM(); !ok {
			return fmt.Errorf("%w: %s", driver.ErrRuntimeUnavailable, detail)
		}
	}
	if _, err := d.resolveBin(); err != nil {
		return err
	}
	if _, err := d.resolveKernel(); err != nil {
		return err
	}
	if d.opt.AgentBin == "" || !probe.File(d.opt.AgentBin) {
		return fmt.Errorf("%w: sbx-agent binary is required (set SBX_AGENT_BIN or place it next to sbx)", driver.ErrRuntimeUnavailable)
	}
	if d.opt.Jailer != "" && !probe.File(d.opt.Jailer) {
		return fmt.Errorf("%w: jailer %s is missing", driver.ErrRuntimeUnavailable, d.opt.Jailer)
	}
	return nil
}

func (d *Driver) Ping(ctx context.Context) error { return d.Check(ctx) }

func (d *Driver) Report(ctx context.Context) driver.Info {
	err := d.Check(ctx)
	detail := "kernel and firecracker binary are ready"
	if err != nil {
		detail = err.Error()
	}
	return driver.Describe("firecracker", d.Capabilities(), err == nil, detail)
}

func (d *Driver) EnsureImage(ctx context.Context, ref string) error {
	if strings.HasPrefix(ref, "ext4:") {
		if !probe.File(strings.TrimPrefix(ref, "ext4:")) {
			return fmt.Errorf("%w: %s", driver.ErrImage, ref)
		}
		return nil
	}
	if d.opt.Build != nil {
		return nil
	}
	if strings.TrimSpace(ref) == "" {
		return fmt.Errorf("%w: image is required", driver.ErrImage)
	}
	return nil
}

func (d *Driver) Create(ctx context.Context, spec driver.SandboxSpec) (driver.Handle, error) {
	if err := d.Check(ctx); err != nil {
		return driver.Handle{}, err
	}
	if !idRE.MatchString(spec.ID) {
		return driver.Handle{}, fmt.Errorf("%w: sandbox id %q", driver.ErrConfig, spec.ID)
	}
	if spec.MemoryMB < 16 || spec.DiskMB < 16 {
		return driver.Handle{}, fmt.Errorf("%w: memory_mb and disk_mb must be at least 16", driver.ErrConfig)
	}
	netSpec, err := d.planNet(ctx, spec)
	if err != nil {
		return driver.Handle{}, err
	}
	bin, err := d.resolveBin()
	if err != nil {
		return driver.Handle{}, err
	}
	kernel, err := d.resolveKernel()
	if err != nil {
		return driver.Handle{}, err
	}
	dir := filepath.Join(d.opt.DataDir, "firecracker", "vms", spec.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return driver.Handle{}, err
	}
	token, err := newToken()
	if err != nil {
		return driver.Handle{}, err
	}
	guest := agent.GuestConfig{Token: token, VsockPort: agent.DefaultVsockPort, Env: spec.Env, DNS: []string{"1.1.1.1", "8.8.8.8"}}
	if netSpec != nil {
		guest.IP = netSpec.GuestCIDR
		guest.Gateway = netSpec.Gateway
	} else {
		guest.DNS = nil
	}
	rawGuest, err := json.Marshal(guest)
	if err != nil {
		return driver.Handle{}, err
	}
	disk := filepath.Join(dir, "rootfs.ext4")
	if err := d.buildRoot(ctx, RootSpec{Image: spec.Image, Agent: d.opt.AgentBin, Dest: disk, DiskMB: spec.DiskMB, Guest: rawGuest}); err != nil {
		_ = os.RemoveAll(dir)
		return driver.Handle{}, err
	}
	rec := record{
		ID: spec.ID, Image: spec.Image, Token: token, Kernel: kernel, Rootfs: disk,
		APISocket: filepath.Join(dir, "api.sock"), Vsock: filepath.Join(dir, "vsock.sock"),
		Log: filepath.Join(dir, "console.log"), State: "running",
	}
	if len(rec.APISocket) > 100 {
		_ = os.RemoveAll(dir)
		return driver.Handle{}, fmt.Errorf("%w: api socket path is longer than 100 bytes; use a shorter data_dir", driver.ErrConfig)
	}
	d.mu.Lock()
	rec.CID = d.nextCID
	d.nextCID++
	d.mu.Unlock()
	kernelPath, diskPath, vsockPath := kernel, disk, rec.Vsock
	if d.opt.Jailer != "" {
		base := filepath.Join(d.opt.DataDir, "firecracker", "jails")
		rec.JailRoot = jailRoot(base, spec.ID)
		rec.APISocket = filepath.Join(rec.JailRoot, "run", "fc.sock")
		rec.Vsock = filepath.Join(rec.JailRoot, "vsock.sock")
		kernelPath, diskPath, vsockPath = "/kernel", "/rootfs.ext4", "/vsock.sock"
	}
	if netSpec != nil {
		rec.Net = &netRecord{
			Tap: netSpec.Tap, HostCIDR: netSpec.HostCIDR, GuestIP: netSpec.GuestIP,
			GuestCIDR: netSpec.GuestCIDR, Gateway: netSpec.Gateway, Mode: string(netSpec.Mode), Allow: netSpec.Allow,
		}
		rec.NetIndex = netSpec.Index
	}
	spawn := d.opt.Spawn
	if spawn == nil {
		spawn = spawnFirecracker
	}
	proc, err := spawn(SpawnSpec{
		Bin: bin, Jailer: d.opt.Jailer, ID: spec.ID, UID: d.opt.UID, GID: d.opt.GID,
		ChrootBase: filepath.Join(d.opt.DataDir, "firecracker", "jails"),
		APISocket:  rec.APISocket, Log: rec.Log,
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		return driver.Handle{}, err
	}
	cleanup := func(cause error) (driver.Handle, error) {
		_ = proc.Stop()
		if netSpec != nil {
			_ = d.teardown(ctx, *netSpec)
		}
		_ = os.RemoveAll(dir)
		if rec.JailRoot != "" {
			_ = os.RemoveAll(filepath.Dir(rec.JailRoot))
		}
		return driver.Handle{}, cause
	}
	if err := waitUnix(ctx, rec.APISocket); err != nil {
		return cleanup(fmt.Errorf("%w: %v", driver.ErrRuntimeUnavailable, err))
	}
	if d.opt.Jailer != "" {
		if err := stageJail(rec.JailRoot, kernel, disk, d.opt.UID, d.opt.GID); err != nil {
			return cleanup(err)
		}
	}
	api := newAPI(rec.APISocket)
	var nic *nicBody
	if netSpec != nil {
		nic = &nicBody{ID: "eth0", MAC: guestMAC(spec.ID), Tap: netSpec.Tap}
	}
	if err := api.configure(ctx, machineBody{VCPU: vcpus(spec.CPU), Mem: spec.MemoryMB}, bootBody{Kernel: kernelPath, Args: bootArgs}, driveBody{ID: "rootfs", Path: diskPath, Root: true}, nic, vsockBody{CID: rec.CID, Path: vsockPath}); err != nil {
		return cleanup(err)
	}
	if netSpec != nil {
		if err := d.applyNet(ctx, *netSpec); err != nil {
			return cleanup(fmt.Errorf("%w: %v", driver.ErrConfig, err))
		}
	}
	if err := api.start(ctx); err != nil {
		return cleanup(err)
	}
	rec.PID = proc.PID()
	vm := &vm{rec: rec, proc: proc}
	client, err := d.dialAgent(ctx, vm)
	if err != nil {
		if raw, readErr := os.ReadFile(rec.Log); readErr == nil && len(raw) > 0 {
			err = fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(raw)))
		}
		return cleanup(err)
	}
	if err := d.save(rec); err != nil {
		_ = client.Close()
		return cleanup(err)
	}
	d.mu.Lock()
	d.vms[spec.ID] = vm
	d.sessions[spec.ID] = client
	d.mu.Unlock()
	return driver.Handle{ID: spec.ID, RuntimeName: "firecracker"}, nil
}

func (d *Driver) Delete(ctx context.Context, id string) error {
	d.mu.Lock()
	current, ok := d.vms[id]
	if ok {
		delete(d.vms, id)
	}
	sess := d.sessions[id]
	delete(d.sessions, id)
	d.mu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
	if !ok {
		rec, err := d.read(id)
		if err != nil {
			return driver.ErrNotFound
		}
		current = &vm{rec: rec}
	}
	if current.proc != nil {
		_ = current.proc.Stop()
	} else if pidAlive(current.rec.PID) {
		proc, _ := os.FindProcess(current.rec.PID)
		if proc != nil {
			_ = proc.Signal(os.Kill)
		}
	}
	if current.rec.Net != nil {
		_ = d.teardown(ctx, netFrom(*current.rec.Net))
	}
	_ = os.RemoveAll(filepath.Dir(current.rec.Rootfs))
	if current.rec.JailRoot != "" {
		_ = os.RemoveAll(filepath.Dir(current.rec.JailRoot))
	}
	return nil
}

func (d *Driver) Inspect(_ context.Context, id string) (driver.Status, error) {
	vm, err := d.get(id)
	if err != nil {
		return driver.Status{}, err
	}
	state := driver.StateStopped
	if pidAlive(vm.rec.PID) || (vm.proc != nil && pidAlive(vm.proc.PID())) {
		state = driver.StateRunning
		if vm.rec.State == string(driver.StatePaused) {
			state = driver.StatePaused
		}
	}
	return driver.Status{ID: id, SandboxID: id, State: state, Image: vm.rec.Image}, nil
}

func (d *Driver) List(context.Context) ([]driver.Status, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]driver.Status, 0, len(d.vms))
	for id, vm := range d.vms {
		state := driver.StateStopped
		if pidAlive(vm.rec.PID) || (vm.proc != nil && pidAlive(vm.proc.PID())) {
			state = driver.StateRunning
			if vm.rec.State == string(driver.StatePaused) {
				state = driver.StatePaused
			}
		}
		out = append(out, driver.Status{ID: id, SandboxID: id, State: state, Image: vm.rec.Image})
	}
	return out, nil
}

func (d *Driver) Pause(ctx context.Context, id string) error {
	return d.setState(ctx, id, "Paused", driver.StatePaused)
}

func (d *Driver) Resume(ctx context.Context, id string) error {
	return d.setState(ctx, id, "Resumed", driver.StateRunning)
}

func (d *Driver) setState(ctx context.Context, id, apiState string, state driver.State) error {
	vm, err := d.get(id)
	if err != nil {
		return err
	}
	if err := newAPI(vm.rec.APISocket).setState(ctx, apiState); err != nil {
		return err
	}
	d.mu.Lock()
	vm.rec.State = string(state)
	rec := vm.rec
	d.mu.Unlock()
	return d.save(rec)
}

func (d *Driver) get(id string) (*vm, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	vm, ok := d.vms[id]
	if !ok {
		return nil, driver.ErrNotFound
	}
	return vm, nil
}

func (d *Driver) planNet(ctx context.Context, spec driver.SandboxSpec) (*NetSpec, error) {
	pol := spec.Policy
	if spec.Network == "" || spec.Network == driver.NetNone {
		if len(pol.Allow) > 0 || len(pol.Deny) > 0 || pol.EgressKbps > 0 || pol.MaxConns > 0 {
			return nil, fmt.Errorf("%w: network rules require network full", driver.ErrConfig)
		}
		return nil, nil
	}
	if spec.Network != driver.NetFull {
		return nil, fmt.Errorf("%w: network %q", driver.ErrConfig, spec.Network)
	}
	if pol.EgressKbps > 0 {
		return nil, fmt.Errorf("%w: firecracker does not enforce egress_kbps", driver.ErrConfig)
	}
	d.mu.Lock()
	d.nextNet++
	n := d.nextNet
	d.mu.Unlock()
	host, guestCIDR, guestIP, gw, err := subnet(n)
	if err != nil {
		return nil, err
	}
	tap := tapName(spec.ID)
	out := &NetSpec{
		Tap: tap, HostCIDR: host, GuestIP: guestIP, GuestCIDR: guestCIDR, Gateway: gw,
		Mode: driver.NetFull, Index: n, MaxConns: pol.MaxConns,
		Blocked: ipv4Only(blockedPrefixes(pol)), PrivateAllow: privateAllowV4(pol.PrivateAllow),
	}
	if len(pol.Deny) > 0 {
		deny, err := resolveAllow(ctx, pol.Deny, pol, false, d.opt.Lookup)
		if err != nil {
			return nil, err
		}
		out.Deny = ipv4Only(deny)
	}
	if len(pol.Allow) > 0 {
		allow, err := resolveAllow(ctx, pol.Allow, pol, true, d.opt.Lookup)
		if err != nil {
			return nil, err
		}
		dns := pol.DNS
		if len(dns) == 0 {
			dns = fallbackDNS
		}
		resolvedDNS, err := resolveAllow(ctx, dns, pol, false, nil)
		if err != nil {
			return nil, err
		}
		out.Allow = ipv4Only(append(allow, resolvedDNS...))
		out.AllowList = true
	}
	return out, nil
}

func (d *Driver) applyNet(ctx context.Context, spec NetSpec) error {
	script, err := netScript(spec)
	if err != nil {
		return err
	}
	spec.Script = script
	if d.opt.SetupNet != nil {
		return d.opt.SetupNet(ctx, spec)
	}
	return runScript(ctx, script)
}

func (d *Driver) teardown(ctx context.Context, spec NetSpec) error {
	if d.opt.TeardownNet != nil {
		return d.opt.TeardownNet(ctx, spec)
	}
	return runScript(ctx, teardownScript(spec))
}

func (d *Driver) resolveBin() (string, error) {
	if d.opt.Bin != "" {
		if !probe.File(d.opt.Bin) {
			return "", fmt.Errorf("%w: firecracker binary %s is missing", driver.ErrRuntimeUnavailable, d.opt.Bin)
		}
		return d.opt.Bin, nil
	}
	candidate := filepath.Join(d.opt.DataDir, "firecracker", "firecracker")
	if probe.File(candidate) {
		return candidate, nil
	}
	if path, ok := probe.Which("firecracker"); ok {
		return path, nil
	}
	return "", fmt.Errorf("%w: firecracker binary not found (sbx setup firecracker --apply)", driver.ErrRuntimeUnavailable)
}

func (d *Driver) resolveKernel() (string, error) {
	if d.opt.Kernel != "" {
		if !probe.File(d.opt.Kernel) {
			return "", fmt.Errorf("%w: kernel %s is missing", driver.ErrRuntimeUnavailable, d.opt.Kernel)
		}
		return d.opt.Kernel, nil
	}
	candidate := filepath.Join(d.opt.DataDir, "firecracker", "vmlinux")
	if probe.File(candidate) {
		return candidate, nil
	}
	return "", fmt.Errorf("%w: guest kernel not found (sbx setup firecracker --apply)", driver.ErrRuntimeUnavailable)
}

func (d *Driver) load() error {
	dir := filepath.Join(d.opt.DataDir, "firecracker", "vms")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		rec, err := d.read(entry.Name())
		if err != nil {
			continue
		}
		d.vms[rec.ID] = &vm{rec: rec}
		if rec.CID >= d.nextCID {
			d.nextCID = rec.CID + 1
		}
		if rec.NetIndex > d.nextNet {
			d.nextNet = rec.NetIndex
		}
	}
	return nil
}

func (d *Driver) read(id string) (record, error) {
	raw, err := os.ReadFile(filepath.Join(d.opt.DataDir, "firecracker", "vms", id, "vm.json"))
	if err != nil {
		return record{}, err
	}
	var rec record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return record{}, err
	}
	if rec.ID == "" {
		rec.ID = id
	}
	return rec, nil
}

func (d *Driver) save(rec record) error {
	dir := filepath.Join(d.opt.DataDir, "firecracker", "vms", rec.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "vm.json"), append(raw, '\n'), 0o600)
}

func netFrom(rec netRecord) NetSpec {
	return NetSpec{
		Tap: rec.Tap, HostCIDR: rec.HostCIDR, GuestIP: rec.GuestIP, GuestCIDR: rec.GuestCIDR,
		Gateway: rec.Gateway, Mode: driver.NetMode(rec.Mode), Allow: rec.Allow,
	}
}

func tapName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "fc" + hex.EncodeToString(sum[:])[:12]
}

func guestMAC(id string) string {
	sum := sha256.Sum256([]byte("mac:" + id))
	return fmt.Sprintf("02:%02x:%02x:%02x:%02x:%02x", sum[0], sum[1], sum[2], sum[3], sum[4])
}

func vcpus(cpu float64) int {
	n := int(math.Ceil(cpu))
	if n < 1 {
		n = 1
	}
	if n > 32 {
		n = 32
	}
	return n
}

func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
