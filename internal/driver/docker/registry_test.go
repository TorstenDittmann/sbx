package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

func TestCreatePullsWithRegistryAuth(t *testing.T) {
	fd, sock := startFakeDaemon(t)
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	d := NewWithClient(Options{Name: "docker", NoEgressFilter: true}, cli)
	ctx := context.Background()
	auth := &driver.RegistryAuth{Server: "ghcr.io", Username: "bot", Password: "s3cret"}
	fd.mu.Lock()
	fd.images["ghcr.io/acme/app:1"] = true
	fd.mu.Unlock()

	// Present locally and not forced: no pull at all.
	if _, err := d.Create(ctx, driver.SandboxSpec{ID: "sbx_reg0000000000001", Image: "ghcr.io/acme/app:1", CPU: 1, MemoryMB: 64, DiskMB: 32, PIDs: 32, Registry: auth}); err != nil {
		t.Fatal(err)
	}
	// Forced: the daemon is asked again, with the credential.
	if _, err := d.Create(ctx, driver.SandboxSpec{ID: "sbx_reg0000000000002", Image: "ghcr.io/acme/app:1", CPU: 1, MemoryMB: 64, DiskMB: 32, PIDs: 32, Registry: auth, ForcePull: true}); err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	pulls, headers := append([]string(nil), fd.pulls...), append([]string(nil), fd.pullAuth...)
	fd.mu.Unlock()
	if len(pulls) != 1 || pulls[0] != "ghcr.io/acme/app:1" {
		t.Fatalf("pulls %v", pulls)
	}
	raw, err := base64.URLEncoding.DecodeString(headers[0])
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["username"] != "bot" || got["password"] != "s3cret" || got["serveraddress"] != "ghcr.io" {
		t.Fatalf("%v", got)
	}
}

func TestBuildPullsBaseWithAuth(t *testing.T) {
	fd, sock := startFakeDaemon(t)
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	d := NewWithClient(Options{Name: "docker"}, cli)
	auth := &driver.RegistryAuth{Server: driver.DockerHub, IdentityToken: "tok"}
	// The fake daemon has no /build handler, so only the pull is checked.
	_ = d.BuildImage(context.Background(), driver.BuildSpec{Tag: "sbx-template:x", Base: "acme/private:1", Run: []string{"true"}, Registry: auth})
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.pulls) != 1 || fd.pulls[0] != "acme/private:1" {
		t.Fatalf("pulls %v", fd.pulls)
	}
	raw, _ := base64.URLEncoding.DecodeString(fd.pullAuth[0])
	var got map[string]string
	_ = json.Unmarshal(raw, &got)
	if got["identitytoken"] != "tok" || got["serveraddress"] != "https://index.docker.io/v1/" || got["password"] != "" {
		t.Fatalf("%v", got)
	}
}

func TestPullWithoutAuthSendsNoHeader(t *testing.T) {
	fd, sock := startFakeDaemon(t)
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.ImagePull(context.Background(), "alpine:3.20", nil); err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if fd.pullAuth[0] != "" {
		t.Fatalf("header %q", fd.pullAuth[0])
	}
}
