package store

import (
	"context"
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

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "sbx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}
