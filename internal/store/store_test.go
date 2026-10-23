package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/id"
)

func TestKeysAndTemplates(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	plain, hash, prefix := id.Key()
	key := APIKey{
		ID: id.RecordID(), Name: "boot", Hash: hash, Prefix: prefix,
		Scopes: []string{"admin"}, CreatedAt: now,
	}
	if err := st.CreateKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	got, err := st.KeyByHash(ctx, id.Hash(plain))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Admin() || got.Prefix != prefix {
		t.Fatalf("key %+v", got)
	}
	if _, err := st.KeyByHash(ctx, "deadbeef"); err != ErrNotFound {
		t.Fatalf("missing key err %v", err)
	}
	if err := st.TouchKey(ctx, key.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err = st.KeyByHash(ctx, hash)
	if err != nil || got.LastUsedAt == nil {
		t.Fatalf("last used %+v %v", got, err)
	}
	n, err := st.CountKeys(ctx)
	if err != nil || n != 1 {
		t.Fatalf("count %d %v", n, err)
	}

	tpl := Template{Name: "python", Image: "python:3.12-slim", Description: "py", Builtin: true, CreatedAt: now}
	if err := st.UpsertBuiltin(ctx, tpl); err != nil {
		t.Fatal(err)
	}
	tpl.Image = "python:3.12-alpine"
	if err := st.UpsertBuiltin(ctx, tpl); err != nil {
		t.Fatal(err)
	}
	gotT, err := st.GetTemplate(ctx, "python")
	if err != nil || gotT.Image != "python:3.12-alpine" || !gotT.Builtin {
		t.Fatalf("template %+v %v", gotT, err)
	}
	if err := st.CreateTemplate(ctx, Template{Name: "custom", Image: "alpine:3.20", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteTemplate(ctx, "python"); err == nil {
		t.Fatal("builtin delete should fail")
	}
	if err := st.DeleteTemplate(ctx, "custom"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetTemplate(ctx, "custom"); err != ErrNotFound {
		t.Fatalf("deleted template err %v", err)
	}
}

func TestSandboxesFilterAndDue(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	sb := Sandbox{
		ID: "sbx_aaaaaaaaaaaaaaaa", Template: "base", Image: "alpine:3.20", Backend: "docker",
		Status: "running", CPU: 1, MemoryMB: 128, DiskMB: 256, PIDs: 64,
		Env: map[string]string{"FOO": "bar"}, Network: "none",
		Labels:     map[string]string{"user": "42"},
		OwnerKeyID: "key_a", CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		LastActivityAt: now, ContainerID: "c1",
	}
	if err := st.InsertSandbox(ctx, sb); err != nil {
		t.Fatal(err)
	}
	other := sb
	other.ID = "sbx_bbbbbbbbbbbbbbbb"
	other.OwnerKeyID = "key_b"
	other.Status = "stopped"
	finished := now
	other.FinishedAt = &finished
	other.Labels = map[string]string{"user": "7"}
	if err := st.InsertSandbox(ctx, other); err != nil {
		t.Fatal(err)
	}

	list, err := st.ListSandboxes(ctx, SandboxFilter{OwnerID: "key_a", Labels: map[string]string{"user": "42"}})
	if err != nil || len(list) != 1 || list[0].Env["FOO"] != "bar" {
		t.Fatalf("list %+v %v", list, err)
	}
	n, err := st.CountActive(ctx, "key_a")
	if err != nil || n != 1 {
		t.Fatalf("active %d %v", n, err)
	}

	sb.ExpiresAt = now.Add(-time.Second)
	if err := st.UpdateSandbox(ctx, sb); err != nil {
		t.Fatal(err)
	}
	due, err := st.ListDue(ctx, now, now.Add(-time.Hour))
	if err != nil || len(due) != 1 || due[0].ID != sb.ID {
		t.Fatalf("due %+v %v", due, err)
	}

	idle := sb
	idle.ID = "sbx_cccccccccccccccc"
	idle.ExpiresAt = now.Add(time.Hour)
	idle.IdleTimeoutSeconds = 30
	idle.LastActivityAt = now.Add(-time.Minute)
	idle.ContainerID = "c3"
	if err := st.InsertSandbox(ctx, idle); err != nil {
		t.Fatal(err)
	}
	due, err = st.ListDue(ctx, now, now.Add(-time.Hour))
	if err != nil || len(due) != 2 {
		t.Fatalf("idle due %d %v", len(due), err)
	}

	creating := sb
	creating.ID = "sbx_dddddddddddddddd"
	creating.Status = "creating"
	creating.CreatedAt = now.Add(-time.Hour)
	creating.ExpiresAt = now.Add(time.Hour)
	creating.IdleTimeoutSeconds = 0
	creating.LastActivityAt = now
	if err := st.InsertSandbox(ctx, creating); err != nil {
		t.Fatal(err)
	}
	due, err = st.ListDue(ctx, now, now.Add(-30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range due {
		if d.ID == creating.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("stuck creating sandbox was not due")
	}

	if _, err := st.PruneFinished(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSandbox(ctx, other.ID); err != ErrNotFound {
		t.Fatal("expected finished row to be pruned")
	}
	counts, err := st.CountByStatus(ctx)
	if err != nil || counts["running"] == 0 && counts["creating"] == 0 {
		t.Fatalf("counts %+v %v", counts, err)
	}
}

func TestParseLabelQuery(t *testing.T) {
	got, err := ParseLabelQuery([]string{"user:42", "env:prod"})
	if err != nil || got["user"] != "42" || got["env"] != "prod" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := ParseLabelQuery([]string{"noseparator"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestVolumesAndMountMigration(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	vol := Volume{
		ID: "vol_0123456789abcdef", Name: "cache", OwnerKeyID: "key_a",
		Backend: "docker", DockerName: "sbx-0123456789abcdef", CreatedAt: now,
	}
	if err := st.InsertVolume(ctx, vol); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertVolume(ctx, vol); err == nil {
		t.Fatal("expected duplicate volume")
	}
	got, err := st.GetVolume(ctx, vol.ID)
	if err != nil || got.Name != "cache" || !got.CreatedAt.Equal(now) {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := st.GetVolume(ctx, "vol_missing"); err != ErrNotFound {
		t.Fatal(err)
	}
	listed, err := st.ListVolumes(ctx, "key_a")
	if err != nil || len(listed) != 1 {
		t.Fatalf("%+v %v", listed, err)
	}
	sb := Sandbox{
		ID: "sbx_aaaaaaaaaaaaaaaa", Image: "alpine:3.20", Backend: "docker", Status: "creating",
		CPU: 1, MemoryMB: 64, DiskMB: 64, PIDs: 32, Network: "none", OwnerKeyID: "key_a",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), LastActivityAt: now,
		Mounts: []Mount{{VolumeID: vol.ID, Path: "/cache", Source: vol.DockerName}},
	}
	if err := st.InsertSandbox(ctx, sb); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.GetSandbox(ctx, sb.ID)
	if err != nil || len(loaded.Mounts) != 1 || loaded.Mounts[0].Source != vol.DockerName {
		t.Fatalf("%+v %v", loaded.Mounts, err)
	}
	inUse, err := st.VolumeInUse(ctx, vol.ID)
	if err != nil || !inUse {
		t.Fatal(inUse, err)
	}
	loaded.Status = "stopped"
	if err := st.UpdateSandbox(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	inUse, err = st.VolumeInUse(ctx, vol.ID)
	if err != nil || inUse {
		t.Fatal("stopped sandbox should not hold the volume", err)
	}
	if err := st.DeleteVolume(ctx, vol.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteVolume(ctx, vol.ID); err != ErrNotFound {
		t.Fatal(err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE sandboxes (
		id TEXT PRIMARY KEY, template TEXT NOT NULL DEFAULT '', image TEXT NOT NULL,
		backend TEXT NOT NULL, status TEXT NOT NULL, cpu REAL NOT NULL,
		memory_mb INTEGER NOT NULL, disk_mb INTEGER NOT NULL, pids INTEGER NOT NULL,
		env_json TEXT NOT NULL, network TEXT NOT NULL, labels_json TEXT NOT NULL,
		owner_key_id TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL,
		idle_timeout_seconds INTEGER NOT NULL DEFAULT 0, last_activity_at INTEGER NOT NULL,
		finished_at INTEGER, container_id TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { migrated.Close() })
	plain := Sandbox{
		ID: "sbx_bbbbbbbbbbbbbbbb", Image: "alpine:3.20", Backend: "docker", Status: "running",
		CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 32, Network: "none", OwnerKeyID: "key_a",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), LastActivityAt: now,
	}
	if err := migrated.InsertSandbox(ctx, plain); err != nil {
		t.Fatal(err)
	}
	loaded, err = migrated.GetSandbox(ctx, plain.ID)
	if err != nil || loaded.Mounts != nil {
		t.Fatalf("%+v %v", loaded.Mounts, err)
	}
}

func TestTemplateColumnMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE templates (
		name TEXT PRIMARY KEY, image TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
		cpu REAL NOT NULL DEFAULT 0, memory_mb INTEGER NOT NULL DEFAULT 0,
		disk_mb INTEGER NOT NULL DEFAULT 0, env_json TEXT NOT NULL DEFAULT '{}',
		builtin INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	if err := st.CreateTemplate(context.Background(), Template{
		Name: "agent", Image: "alpine:3.20", Network: "full", Warm: 2,
		Setup: []string{"echo", "hi"}, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetTemplate(context.Background(), "agent")
	if err != nil || got.Warm != 2 || got.Network != "full" || len(got.Setup) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
}

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "sbx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestKeyRegistriesAndPrivateImages(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	key := APIKey{ID: "key_reg", Name: "reg", Hash: "h-reg", Prefix: "p", Scopes: []string{"sandbox"}, CreatedAt: now,
		Registries: []RegistryCred{{Server: "ghcr.io", Username: "bot", Password: "s3cret"}}}
	if err := st.CreateKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	got, err := st.KeyByID(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := got.Registry("ghcr.io"); !ok || r.Password != "s3cret" {
		t.Fatalf("%+v", got.Registries)
	}
	if err := st.SetKeyRegistries(ctx, key.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.KeyByID(ctx, key.ID); len(got.Registries) != 0 {
		t.Fatalf("%+v", got.Registries)
	}
	if err := st.SetKeyRegistries(ctx, "key_missing", nil); err != ErrNotFound {
		t.Fatal(err)
	}

	if has, err := st.HasPrivateImages(ctx); err != nil || has {
		t.Fatal(has, err)
	}
	if err := st.MarkPrivateImage(ctx, "sbx-template:abc", "ghcr.io/acme/app:1", now); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkPrivateImage(ctx, "sbx-template:abc", "other", now); err != nil {
		t.Fatal(err)
	}
	base, ok, err := st.PrivateImage(ctx, "sbx-template:abc")
	if err != nil || !ok || base != "ghcr.io/acme/app:1" {
		t.Fatal(base, ok, err)
	}
	if _, ok, _ := st.PrivateImage(ctx, "docker.io/library/alpine"); ok {
		t.Fatal("unmarked image reported private")
	}
	if has, _ := st.HasPrivateImages(ctx); !has {
		t.Fatal("expected private images")
	}
}
