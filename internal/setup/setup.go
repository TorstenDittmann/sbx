// Package setup prepares optional sandbox backends on a host.
//
// sbx setup prints the plan. --apply downloads pinned assets into the data
// directory and, when the process can write it, registers a Docker runtime.
// It does not restart the Docker daemon.
package setup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/TorstenDittmann/sbx/internal/probe"
)

const (
	// FirecrackerVersion is the release sbx setup downloads.
	FirecrackerVersion = "v1.17.0"
)

// Options controls one setup invocation.
type Options struct {
	DataDir string
	Prefix  string
	Arch    string
	Apply   bool
	// DockerConfig is the daemon.json path. Empty uses /etc/docker/daemon.json.
	DockerConfig string
	// GVisorURL, FirecrackerURL, and KernelURL override the pinned downloads. Tests set them.
	GVisorURL      string
	FirecrackerURL string
	KernelURL      string
	Client         *http.Client
}

// Result is what setup did or would do.
type Result struct {
	Backend string
	Lines   []string
	Applied bool
}

func (r Result) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", r.Backend)
	for _, line := range r.Lines {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	if !r.Applied {
		b.WriteString("  rerun with --apply to download and write files\n")
	}
	return b.String()
}

// Arch is the Firecracker asset architecture for this process.
func Arch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "aarch64"
	default:
		return "x86_64"
	}
}

func (o Options) arch() string {
	if o.Arch != "" {
		return o.Arch
	}
	return Arch()
}

func (o Options) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (o Options) prefix() string {
	if o.Prefix != "" {
		return o.Prefix
	}
	return filepath.Join(o.DataDir, "bin")
}

// GVisor plans a runsc install and a Docker runtime named runsc.
func GVisor(ctx context.Context, opt Options) (Result, error) {
	return runtimeSetup(ctx, opt, runtimePlan{
		name:    "gvisor",
		binary:  "runsc",
		url:     opt.gvisorURL(),
		runtime: "runsc",
		entry: map[string]any{
			"path": filepath.Join(opt.prefix(), "runsc"),
		},
		notes: []string{
			"gVisor is the runsc Docker runtime. No KVM required.",
			"restart docker after the runtime is registered (systemctl restart docker, or restart the engine)",
		},
	})
}

// Kata plans the Kata Containers runtime class io.containerd.kata.v2.
func Kata(ctx context.Context, opt Options) (Result, error) {
	res, err := runtimeSetup(ctx, opt, runtimePlan{
		name:    "kata",
		binary:  "",
		runtime: "kata",
		entry: map[string]any{
			"runtimeType": "io.containerd.kata.v2",
		},
		notes: []string{
			"Kata needs /dev/kvm and a containerd shim (containerd-shim-kata-v2).",
			"Install the distro kata-containers package, or the upstream kata release, before --apply.",
			"The Docker runtime name is kata and the runtimeType is io.containerd.kata.v2.",
			"restart docker after the runtime is registered",
		},
	})
	if err != nil {
		return res, err
	}
	if ok, detail := kvmLine(); !ok {
		res.Lines = append([]string{"kvm: " + detail}, res.Lines...)
	} else {
		res.Lines = append([]string{"kvm: " + detail}, res.Lines...)
	}
	return res, nil
}

// Firecracker downloads the pinned firecracker and jailer binaries and a guest kernel.
func Firecracker(ctx context.Context, opt Options) (Result, error) {
	if opt.DataDir == "" {
		return Result{}, errors.New("data dir is required")
	}
	arch := opt.arch()
	dir := filepath.Join(opt.DataDir, "firecracker")
	res := Result{Backend: "firecracker", Lines: []string{
		"version " + FirecrackerVersion + " (" + arch + ")",
		"binaries and kernel go in " + dir,
		"the guest kernel is Firecracker CI vmlinux-6.1.155 (v1.15 artifacts, supported by " + FirecrackerVersion + ")",
		"rootfs images are built later from OCI images; this step does not need Docker",
	}}
	ok, detail := kvmLine()
	res.Lines = append(res.Lines, "kvm: "+detail)
	if !ok {
		res.Lines = append(res.Lines, "sandboxes will not start until /dev/kvm is writable")
	}
	if !opt.Apply {
		res.Lines = append(res.Lines,
			"binary: "+opt.firecrackerURL(),
			"kernel: "+opt.kernelURL(),
		)
		return res, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, err
	}
	archive, err := download(ctx, opt.client(), opt.firecrackerURL(), 64<<20)
	if err != nil {
		return res, fmt.Errorf("download firecracker: %w", err)
	}
	bins, err := untarBinaries(archive)
	if err != nil {
		return res, err
	}
	fc, ok := pickBinary(bins, "firecracker")
	if !ok {
		return res, errors.New("archive has no firecracker binary")
	}
	jail, _ := pickBinary(bins, "jailer")
	if err := writeExec(filepath.Join(dir, "firecracker"), fc); err != nil {
		return res, err
	}
	if jail != nil {
		if err := writeExec(filepath.Join(dir, "jailer"), jail); err != nil {
			return res, err
		}
	}
	kernel, err := download(ctx, opt.client(), opt.kernelURL(), 128<<20)
	if err != nil {
		return res, fmt.Errorf("download kernel: %w", err)
	}
	if len(kernel) < 1024 {
		return res, errors.New("kernel download was empty")
	}
	kpath := filepath.Join(dir, "vmlinux")
	if err := os.WriteFile(kpath, kernel, 0o644); err != nil {
		return res, err
	}
	manifest := map[string]string{
		"version":     FirecrackerVersion,
		"arch":        arch,
		"firecracker": filepath.Join(dir, "firecracker"),
		"jailer":      filepath.Join(dir, "jailer"),
		"kernel":      kpath,
		"kernel_sha":  sha(kernel),
	}
	raw, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), append(raw, '\n'), 0o644); err != nil {
		return res, err
	}
	res.Applied = true
	res.Lines = append(res.Lines, "wrote "+filepath.Join(dir, "manifest.json"))
	return res, nil
}

func (o Options) gvisorURL() string {
	if o.GVisorURL != "" {
		return o.GVisorURL
	}
	return "https://storage.googleapis.com/gvisor/releases/release/latest/" + o.arch() + "/runsc"
}

func (o Options) firecrackerURL() string {
	if o.FirecrackerURL != "" {
		return o.FirecrackerURL
	}
	return "https://github.com/firecracker-microvm/firecracker/releases/download/" +
		FirecrackerVersion + "/firecracker-" + FirecrackerVersion + "-" + o.arch() + ".tgz"
}

func (o Options) kernelURL() string {
	if o.KernelURL != "" {
		return o.KernelURL
	}
	// Firecracker 1.17 still supports the 6.1 guest kernel. The newest CI build
	// published for that kernel is from the v1.15 artifact set.
	switch o.arch() {
	case "aarch64":
		return "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/aarch64/vmlinux-6.1.155"
	default:
		return "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64/vmlinux-6.1.155"
	}
}

type runtimePlan struct {
	name    string
	binary  string
	url     string
	runtime string
	entry   map[string]any
	notes   []string
}

func runtimeSetup(ctx context.Context, opt Options, plan runtimePlan) (Result, error) {
	if opt.DataDir == "" && opt.Prefix == "" {
		return Result{}, errors.New("data dir is required")
	}
	res := Result{Backend: plan.name}
	res.Lines = append(res.Lines, plan.notes...)
	if plan.binary != "" {
		res.Lines = append(res.Lines, "binary: "+filepath.Join(opt.prefix(), plan.binary))
		res.Lines = append(res.Lines, "url: "+plan.url)
	}
	cfgPath := opt.DockerConfig
	if cfgPath == "" {
		cfgPath = "/etc/docker/daemon.json"
	}
	snippet, err := runtimeSnippet(plan.runtime, plan.entry)
	if err != nil {
		return res, err
	}
	res.Lines = append(res.Lines, "docker runtime "+plan.runtime+" in "+cfgPath+":")
	for _, line := range strings.Split(strings.TrimRight(snippet, "\n"), "\n") {
		res.Lines = append(res.Lines, "  "+line)
	}
	if !opt.Apply {
		return res, nil
	}
	if plan.binary != "" {
		if err := os.MkdirAll(opt.prefix(), 0o755); err != nil {
			return res, err
		}
		body, err := download(ctx, opt.client(), plan.url, 64<<20)
		if err != nil {
			return res, fmt.Errorf("download %s: %w", plan.binary, err)
		}
		if err := writeExec(filepath.Join(opt.prefix(), plan.binary), body); err != nil {
			return res, err
		}
		res.Lines = append(res.Lines, "wrote "+filepath.Join(opt.prefix(), plan.binary))
	}
	if err := mergeDockerRuntime(cfgPath, plan.runtime, plan.entry); err != nil {
		res.Lines = append(res.Lines, "daemon.json was not updated: "+err.Error())
		res.Lines = append(res.Lines, "add the runtime snippet above and restart docker")
	} else {
		res.Lines = append(res.Lines, "updated "+cfgPath)
	}
	res.Applied = true
	return res, nil
}

func runtimeSnippet(name string, entry map[string]any) (string, error) {
	body := map[string]any{"runtimes": map[string]any{name: entry}}
	raw, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw) + "\n", nil
}

func mergeDockerRuntime(path, name string, entry map[string]any) error {
	existing := map[string]any{}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &existing); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	}
	runtimes, _ := existing["runtimes"].(map[string]any)
	if runtimes == nil {
		runtimes = map[string]any{}
	}
	runtimes[name] = entry
	existing["runtimes"] = runtimes
	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func download(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("GET %s exceeded %d bytes", url, limit)
	}
	return body, nil
}

func untarBinaries(archive []byte) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("firecracker archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		if hdr.Size > 64<<20 {
			return nil, fmt.Errorf("%s is too large", hdr.Name)
		}
		body, err := io.ReadAll(io.LimitReader(tr, hdr.Size))
		if err != nil {
			return nil, err
		}
		out[filepath.Base(hdr.Name)] = body
	}
}

func pickBinary(bins map[string][]byte, kind string) ([]byte, bool) {
	for name, body := range bins {
		if strings.Contains(name, kind) && !strings.Contains(name, ".txt") {
			return body, true
		}
	}
	return nil, false
}

func writeExec(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o755)
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func kvmLine() (bool, string) { return probe.KVM() }
