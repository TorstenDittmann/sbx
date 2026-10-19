package core

import (
	"context"
	"strings"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
	"github.com/TorstenDittmann/sbx/internal/store"
)

func TestVolumesPersistAcrossSandboxes(t *testing.T) {
	cfg := config.Default()
	cfg.Limits.MaxSandboxesPerKey = 4
	st := openStore(t)
	docker := fake.New("docker")
	gvisor := fake.New("gvisor")
	mgr := New(cfg, st, map[string]driver.Driver{"docker": docker, "gvisor": gvisor}, discard())
	ctx := context.Background()
	owner := coreActor(t, st)
	other := coreActor(t, st)

	vol, err := mgr.CreateVolume(ctx, owner, "project", "")
	if err != nil {
		t.Fatal(err)
	}
	if vol.Backend != "docker" || vol.DockerName != "sbx-"+strings.TrimPrefix(vol.ID, "vol_") || !docker.HasVolume(vol.DockerName) {
		t.Fatalf("volume %+v", vol)
	}
	if _, err := mgr.CreateVolume(ctx, owner, "project", "docker"); err == nil {
		t.Fatal("expected duplicate name")
	}
	if _, err := mgr.CreateVolume(ctx, owner, "-bad", ""); err == nil {
		t.Fatal("expected bad name")
	}
	if _, err := mgr.CreateVolume(ctx, owner, "ok", "nope"); err == nil {
		t.Fatal("expected unknown backend")
	}

	hidden, err := mgr.CreateVolume(ctx, other, "project", "gvisor")
	if err != nil {
		t.Fatal(err)
	}
	gv, err := mgr.CreateVolume(ctx, owner, "tools", "gvisor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.GetVolume(ctx, owner, hidden.ID); err == nil {
		t.Fatal("other owner's volume should be hidden")
	}
	all, err := mgr.ListVolumes(ctx, Actor{Admin: true})
	if err != nil || len(all) != 3 {
		t.Fatalf("admin list %d %v", len(all), err)
	}
	mine, err := mgr.ListVolumes(ctx, owner)
	if err != nil || len(mine) != 2 {
		t.Fatalf("owner list %+v %v", mine, err)
	}

	sb, err := mgr.Create(ctx, owner, CreateInput{
		Image: "alpine:3.20", TTLSeconds: intPtr(60),
		Mounts: []MountInput{{VolumeID: vol.ID, Path: "/workspace"}, {VolumeID: vol.ID, Path: "/cache"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sb.Mounts) != 2 || sb.Mounts[0].Path != "/workspace" || sb.Mounts[0].Source != vol.DockerName {
		t.Fatalf("mounts %+v", sb.Mounts)
	}
	spec, ok := docker.Spec(sb.ID)
	if !ok || len(spec.Mounts) != 2 || spec.Mounts[0].Target != "/workspace" || spec.Mounts[1].Target != "/cache" {
		t.Fatalf("spec %+v %v", spec.Mounts, ok)
	}
	if err := mgr.DeleteVolume(ctx, owner, vol.ID); err == nil {
		t.Fatal("mounted volume should conflict")
	}
	if _, err := mgr.Pause(ctx, owner, sb.ID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.DeleteVolume(ctx, owner, vol.ID); err == nil {
		t.Fatal("paused sandbox should keep the volume")
	}
	if _, err := mgr.Delete(ctx, owner, sb.ID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.DeleteVolume(ctx, owner, vol.ID); err != nil {
		t.Fatal(err)
	}
	if docker.HasVolume(vol.DockerName) {
		t.Fatal("docker volume should be removed")
	}
	if _, err := mgr.GetVolume(ctx, owner, vol.ID); err == nil {
		t.Fatal("deleted volume should be gone")
	}

	if _, err := mgr.Create(ctx, owner, CreateInput{Image: "alpine:3.20", Backend: "gvisor", Mounts: []MountInput{{VolumeID: hidden.ID, Path: "/etc/passwd"}}}); err == nil {
		t.Fatal("reserved path should fail")
	}
	if _, err := mgr.Create(ctx, owner, CreateInput{Image: "alpine:3.20", Mounts: []MountInput{{VolumeID: gv.ID, Path: "/data"}}}); err == nil {
		t.Fatal("backend mismatch should fail")
	}
	if _, err := mgr.Create(ctx, owner, CreateInput{Image: "alpine:3.20", Mounts: []MountInput{{VolumeID: hidden.ID, Path: "/data"}}}); err == nil {
		t.Fatal("another key's volume should be hidden")
	}
	if _, err := mgr.Create(ctx, owner, CreateInput{Image: "alpine:3.20", Mounts: []MountInput{{VolumeID: "vol_missing0000000", Path: "/data"}}}); err == nil {
		t.Fatal("missing volume should fail")
	}
}

func TestMountPathRules(t *testing.T) {
	ok := []string{"/workspace", "/cache", "/opt/cache", "/var/lib/sbx"}
	for _, p := range ok {
		if _, err := cleanMountPath(p); err != nil {
			t.Fatal(p, err)
		}
	}
	bad := []string{"", "cache", "/workspace/src", "/tmp/x", "/opt/sbx/agent", "../etc", "/cache/../../etc", "/a:b", "/proc", "\x00"}
	for _, p := range bad {
		if _, err := cleanMountPath(p); err == nil {
			t.Fatal("accepted", p)
		}
	}
	if err := validVolumeName(""); err == nil || validVolumeName("has space") == nil || validVolumeName("ok.name_1") != nil {
		t.Fatal("volume name rules")
	}
	many := make([]MountInput, 9)
	for i := range many {
		many[i] = MountInput{VolumeID: "vol_x", Path: "/m"}
	}
	cfg := config.Default()
	st := openStore(t)
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fake.New("docker")}, discard())
	if _, err := mgr.resolveMounts(context.Background(), Actor{}, "docker", many); err == nil {
		t.Fatal("expected mount cap")
	}
	row := store.Volume{ID: "vol_deadbeefdeadbee", Name: "gone", OwnerKeyID: "key", Backend: "missing", DockerName: "sbx-dead", CreatedAt: mgr.now()}
	if err := st.InsertVolume(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	if err := mgr.DeleteVolume(context.Background(), Actor{Admin: true}, row.ID); err == nil {
		t.Fatal("expected unknown backend")
	}
}
