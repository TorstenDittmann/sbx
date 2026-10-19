package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestMigratesTemplateBackend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE templates (
			name TEXT PRIMARY KEY,
			image TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			cpu REAL NOT NULL DEFAULT 0,
			memory_mb INTEGER NOT NULL DEFAULT 0,
			disk_mb INTEGER NOT NULL DEFAULT 0,
			env_json TEXT NOT NULL DEFAULT '{}',
			builtin INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO templates (name, image, description, cpu, memory_mb, disk_mb, env_json, builtin, created_at) VALUES ('base', 'alpine:3.20', '', 0.5, 128, 256, '{}', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tpl, err := st.GetTemplate(context.Background(), "base")
	if err != nil || tpl.Backend != "" || tpl.Image != "alpine:3.20" {
		t.Fatalf("%+v %v", tpl, err)
	}
	if err := st.CreateTemplate(context.Background(), Template{
		Name: "custom", Image: "debian:12", Backend: "gvisor", CreatedAt: time.UnixMilli(2).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetTemplate(context.Background(), "custom")
	if err != nil || got.Backend != "gvisor" {
		t.Fatalf("%+v %v", got, err)
	}
}
