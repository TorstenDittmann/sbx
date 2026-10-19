package docker

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

func TestBuildCreateSecureDefaults(t *testing.T) {
	body := buildCreate(driver.SandboxSpec{
		ID: "sbx_abc", Image: "alpine:3.20", CPU: 0.5, MemoryMB: 128, DiskMB: 32, PIDs: 64,
		Env: map[string]string{"FOO": "bar"}, Network: driver.NetNone,
		Labels: map[string]string{"user": "42", "sbx.id": "sbx_other", "sbx.managed": "false"},
	}, Options{Name: "docker", UID: 1000, GID: 1000, ReadOnlyRootfs: true}, []string{"PATH=/usr/bin"}, "")

	if body.User != "1000:1000" {
		t.Fatalf("user %s", body.User)
	}
	if body.HostConfig.NetworkMode != "none" || body.HostConfig.Privileged || !body.HostConfig.ReadonlyRootfs {
		t.Fatalf("host %+v", body.HostConfig)
	}
	if len(body.HostConfig.CapDrop) != 1 || body.HostConfig.CapDrop[0] != "ALL" {
		t.Fatalf("caps %+v", body.HostConfig.CapDrop)
	}
	if len(body.HostConfig.CapAdd) != 0 {
		t.Fatal("expected no added caps")
	}
	joined := strings.Join(body.HostConfig.SecurityOpt, ",")
	if !strings.Contains(joined, "no-new-privileges:true") {
		t.Fatal(joined)
	}
	if body.HostConfig.Memory != 128*1024*1024 || body.HostConfig.MemorySwap != body.HostConfig.Memory {
		t.Fatalf("memory %d swap %d", body.HostConfig.Memory, body.HostConfig.MemorySwap)
	}
	if body.HostConfig.NanoCpus != 500_000_000 || body.HostConfig.PidsLimit != 64 {
		t.Fatalf("cpu %d pids %d", body.HostConfig.NanoCpus, body.HostConfig.PidsLimit)
	}
	if body.HostConfig.Runtime != "" {
		t.Fatal("docker backend should use the default runtime")
	}
	if !strings.Contains(body.HostConfig.Tmpfs["/workspace"], "size=32m") || !strings.Contains(body.HostConfig.Tmpfs["/workspace"], "uid=1000") {
		t.Fatal(body.HostConfig.Tmpfs)
	}
	if body.Labels["sbx.managed"] != "true" || body.Labels["sbx.id"] != "sbx_abc" || body.Labels["user"] != "42" {
		t.Fatal(body.Labels)
	}
	if body.Entrypoint[0] != "sleep" {
		t.Fatal(body.Entrypoint)
	}
	found := false
	for _, e := range body.Env {
		if e == "FOO=bar" || e == "HOME=/workspace" {
			found = true
		}
	}
	if !found {
		t.Fatal(body.Env)
	}
}

func TestBuildCreateGVisorAndFullNetwork(t *testing.T) {
	body := buildCreate(driver.SandboxSpec{
		ID: "sbx_g", Image: "alpine:3.20", CPU: 1, MemoryMB: 64, DiskMB: 64, PIDs: 32,
		Network: driver.NetFull,
	}, Options{Name: "gvisor", Runtime: "runsc", UID: 1000, GID: 1000, ReadOnlyRootfs: true}, nil, "tok")
	if body.HostConfig.Runtime != "runsc" {
		t.Fatal(body.HostConfig.Runtime)
	}
	if body.HostConfig.NetworkMode != sandboxNetwork {
		t.Fatal(body.HostConfig.NetworkMode)
	}
	if body.Labels["sbx.backend"] != "gvisor" {
		t.Fatal(body.Labels)
	}
	if len(body.Entrypoint) != 2 || body.Entrypoint[0] != agentBinary || body.Entrypoint[1] != "init" {
		t.Fatalf("entrypoint %v", body.Entrypoint)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"Cmd":[]`)) {
		t.Fatalf("image cmd was not cleared: %s", raw)
	}
	if len(body.HostConfig.Binds) != 1 || body.HostConfig.Binds[0] != agentVolume+":/opt/sbx:ro" {
		t.Fatal(body.HostConfig.Binds)
	}
	foundToken := false
	for _, e := range body.Env {
		if e == agentTokenEnv+"=tok" {
			foundToken = true
		}
	}
	if !foundToken {
		t.Fatal(body.Env)
	}
}

func TestWithTimeoutAndSplitRef(t *testing.T) {
	got := withTimeout([]string{"echo", "hi"}, 5)
	joined := strings.Join(got, " ")
	if !strings.HasPrefix(joined, "sh -c ") || !strings.Contains(joined, "5 echo hi") || strings.Contains(joined, "timeout") {
		t.Fatal(got)
	}
	for _, needle := range []string{"mkfifo", "setsid", "kill -KILL", "0|1"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("missing %s in %v", needle, got)
		}
	}
	if strings.Join(withTimeout([]string{"echo", "hi"}, 0), " ") != "echo hi" {
		t.Fatal(withTimeout([]string{"echo", "hi"}, 0))
	}
	from, tag := splitRef("ghcr.io/org/img:1.2")
	if from != "ghcr.io/org/img" || tag != "1.2" {
		t.Fatalf("%s %s", from, tag)
	}
	from, tag = splitRef("alpine")
	if from != "alpine" || tag != "" {
		t.Fatalf("%s %s", from, tag)
	}
	from, tag = splitRef("alpine@sha256:abc")
	if tag != "" || !strings.Contains(from, "@") {
		t.Fatalf("%s %s", from, tag)
	}
}
