// Package store is the single-node SQLite state. The same Go types are what a
// later Postgres implementation would persist; cluster mode is not in v0.1.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// KeyNetwork is the default egress policy for sandboxes created with a key.
// Nil slices inherit the server policy. An empty non-nil slice clears it.
type KeyNetwork struct {
	Mode string `json:"mode,omitempty"`
	// MaxMode, when set, is a ceiling: sandboxes created with the key cannot
	// ask for a looser mode, and under "allow" cannot list destinations
	// outside Allow. Without it the key network is only a default.
	MaxMode    string    `json:"max_mode,omitempty"`
	Allow      *[]string `json:"allow,omitempty"`
	Deny       *[]string `json:"deny,omitempty"`
	EgressKbps int       `json:"egress_kbps,omitempty"`
	MaxConns   int       `json:"max_connections,omitempty"`
}

// APIKey is a stored credential. Hash is the sha256 hex of the secret.
type APIKey struct {
	ID      string
	Name    string
	Hash    string
	Prefix  string
	Scopes  []string
	Network KeyNetwork
	// Registries are image registry credentials for sandboxes and builds
	// made with this key. They are stored as given and never returned.
	Registries []RegistryCred
	CreatedAt  time.Time
	LastUsedAt *time.Time
	// ExpiresAt, when set, is when the key stops authenticating.
	ExpiresAt *time.Time
	// RevokedAt is set once the key has been revoked. A revoked key never
	// authenticates again.
	RevokedAt *time.Time
	// Project, when set, groups the key with every other key of the same
	// project: each sees and, with the sandbox scope, uses the sandboxes and
	// volumes the others created. It is fixed when the key is created.
	Project string
	// Quota limits what the key's live sandboxes may hold at once.
	Quota KeyQuota
}

// KeyQuota caps the sandboxes one key may hold at once. Zero fields fall back
// to the server: MaxSandboxes to limits.max_sandboxes_per_key, MaxCPU and
// MaxMemoryMB to no total limit (each sandbox is still capped by the server).
type KeyQuota struct {
	MaxSandboxes int     `json:"max_sandboxes,omitempty"`
	MaxCPU       float64 `json:"max_cpu,omitempty"`
	MaxMemoryMB  int     `json:"max_memory_mb,omitempty"`
}

// Usage is what one key's live sandboxes hold: those creating, running,
// paused, or hibernated, since each keeps its reservation.
type Usage struct {
	Sandboxes int
	CPU       float64
	MemoryMB  int
}

// RegistryCred is one registry credential. Server is a normalized host.
type RegistryCred struct {
	Server        string `json:"server"`
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	IdentityToken string `json:"identity_token,omitempty"`
}

// Registry returns the key's credential for host, if it has one.
func (k APIKey) Registry(host string) (RegistryCred, bool) {
	for _, r := range k.Registries {
		if r.Server == host {
			return r, true
		}
	}
	return RegistryCred{}, false
}

// Usable reports whether the key may still authenticate at now.
func (k APIKey) Usable(now time.Time) bool {
	if k.RevokedAt != nil {
		return false
	}
	return k.ExpiresAt == nil || now.Before(*k.ExpiresAt)
}

// Scopes from the most to the least privileged. Each implies the ones after it.
const (
	ScopeAdmin   = "admin"
	ScopeSandbox = "sandbox"
	ScopeRead    = "read"
)

var scopeRank = map[string]int{ScopeRead: 1, ScopeSandbox: 2, ScopeAdmin: 3}

// ValidScope reports whether s is a known scope.
func ValidScope(s string) bool {
	return scopeRank[s] > 0
}

// ValidProject accepts 1 to 64 of [A-Za-z0-9._-], starting with a letter or digit.
func ValidProject(p string) bool {
	if p == "" || len(p) > 64 {
		return false
	}
	for i, c := range p {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || (c != '.' && c != '_' && c != '-')) {
			return false
		}
	}
	return true
}

// HasScope reports whether the key may perform an action. admin implies
// sandbox, and sandbox implies read.
func (k APIKey) HasScope(scope string) bool {
	want := scopeRank[scope]
	if want == 0 {
		return false
	}
	for _, s := range k.Scopes {
		if scopeRank[s] >= want {
			return true
		}
	}
	return false
}

// Admin reports whether the key has the admin scope.
func (k APIKey) Admin() bool {
	return k.HasScope("admin")
}

// Template is a named image plus optional resource defaults.
// Backend, when set, is the default driver for sandboxes created from it.
type Template struct {
	Name         string
	Image        string
	Description  string
	CPU          float64
	MemoryMB     int
	DiskMB       int
	Env          map[string]string
	Backend      string
	Network      string
	Setup        []string
	SetupTimeout int
	Warm         int
	Steps        []BuildStep
	Builtin      bool
	CreatedAt    time.Time
}

// BuildStep is one template build step. Exactly one field is set.
type BuildStep struct {
	Run      string            `json:"run,omitempty"`
	Packages []string          `json:"packages,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Workdir  string            `json:"workdir,omitempty"`
}

// SystemOwner is the owner of warm-pool sandboxes. They do not count toward a key's quota.
const SystemOwner = "sbx-system"

// Sandbox is one sandbox row. Status is creating, running, paused,
// hibernated, stopped, error, or warm. A hibernated sandbox has no container:
// its state is in SnapshotImage until it is woken.
type Sandbox struct {
	ID                 string
	Template           string
	Image              string
	Backend            string
	Status             string
	CPU                float64
	MemoryMB           int
	DiskMB             int
	PIDs               int
	Env                map[string]string
	Network            string
	Allow              []string
	Deny               []string
	EgressKbps         int
	MaxConns           int
	Labels             map[string]string
	Secrets            map[string]string
	OwnerKeyID         string
	CreatedAt          time.Time
	ExpiresAt          time.Time
	IdleTimeoutSeconds int
	// IdleAction is what the reaper does once IdleTimeoutSeconds pass
	// without activity: stop (the default), pause, or hibernate.
	IdleAction     string
	LastActivityAt time.Time
	FinishedAt     *time.Time
	ContainerID    string
	Error          string
	Mounts         []Mount
	// SnapshotImage is the hibernation snapshot the sandbox was saved to.
	// While hibernated there is no container and a wake starts one from
	// it; after the wake the container keeps running from it.
	SnapshotImage string
}

// Mount records a volume attached to a sandbox.
type Mount struct {
	VolumeID string `json:"volume_id"`
	Path     string `json:"path"`
	Source   string `json:"source"`
}

// Volume is a named Docker volume that outlives any one sandbox.
type Volume struct {
	ID         string
	Name       string
	OwnerKeyID string
	Backend    string
	DockerName string
	CreatedAt  time.Time
}

// SandboxFilter selects rows. Empty fields are ignored.
type SandboxFilter struct {
	Status   string
	Backend  string
	Template string
	Query    string
	OwnerID  string
	// Project, with OwnerID, also selects sandboxes of every key in the project.
	Project string
	Labels  map[string]string
}

// Store is a process-local SQLite database.
type Store struct {
	db *sql.DB
}

// Open creates or migrates the database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	alters := []struct{ table, column, stmt string }{
		{"sandboxes", "allow_json", `ALTER TABLE sandboxes ADD COLUMN allow_json TEXT NOT NULL DEFAULT '[]'`},
		{"sandboxes", "deny_json", `ALTER TABLE sandboxes ADD COLUMN deny_json TEXT NOT NULL DEFAULT '[]'`},
		{"sandboxes", "egress_kbps", `ALTER TABLE sandboxes ADD COLUMN egress_kbps INTEGER NOT NULL DEFAULT 0`},
		{"sandboxes", "max_conns", `ALTER TABLE sandboxes ADD COLUMN max_conns INTEGER NOT NULL DEFAULT 0`},
		{"sandboxes", "mounts_json", `ALTER TABLE sandboxes ADD COLUMN mounts_json TEXT NOT NULL DEFAULT '[]'`},
		{"sandboxes", "secrets_json", `ALTER TABLE sandboxes ADD COLUMN secrets_json TEXT NOT NULL DEFAULT '{}'`},
		{"sandboxes", "idle_action", `ALTER TABLE sandboxes ADD COLUMN idle_action TEXT NOT NULL DEFAULT 'stop'`},
		{"sandboxes", "snapshot_image", `ALTER TABLE sandboxes ADD COLUMN snapshot_image TEXT NOT NULL DEFAULT ''`},
		{"api_keys", "network_json", `ALTER TABLE api_keys ADD COLUMN network_json TEXT NOT NULL DEFAULT '{}'`},
		{"api_keys", "expires_at", `ALTER TABLE api_keys ADD COLUMN expires_at INTEGER`},
		{"api_keys", "revoked_at", `ALTER TABLE api_keys ADD COLUMN revoked_at INTEGER`},
		{"api_keys", "registries_json", `ALTER TABLE api_keys ADD COLUMN registries_json TEXT NOT NULL DEFAULT '[]'`},
		{"api_keys", "project", `ALTER TABLE api_keys ADD COLUMN project TEXT NOT NULL DEFAULT ''`},
		{"api_keys", "quota_json", `ALTER TABLE api_keys ADD COLUMN quota_json TEXT NOT NULL DEFAULT '{}'`},
		{"templates", "setup_json", `ALTER TABLE templates ADD COLUMN setup_json TEXT NOT NULL DEFAULT '[]'`},
		{"templates", "warm", `ALTER TABLE templates ADD COLUMN warm INTEGER NOT NULL DEFAULT 0`},
		{"templates", "network", `ALTER TABLE templates ADD COLUMN network TEXT NOT NULL DEFAULT 'none'`},
		{"templates", "setup_timeout_seconds", `ALTER TABLE templates ADD COLUMN setup_timeout_seconds INTEGER NOT NULL DEFAULT 0`},
		{"templates", "backend", `ALTER TABLE templates ADD COLUMN backend TEXT NOT NULL DEFAULT ''`},
		{"templates", "steps_json", `ALTER TABLE templates ADD COLUMN steps_json TEXT NOT NULL DEFAULT '[]'`},
	}
	for _, alt := range alters {
		have, err := hasColumn(db, alt.table, alt.column)
		if err != nil {
			return err
		}
		if have {
			continue
		}
		if _, err := db.Exec(alt.stmt); err != nil {
			return err
		}
	}
	return nil
}

func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

const schema = `
CREATE TABLE IF NOT EXISTS api_keys (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    key_hash TEXT NOT NULL UNIQUE,
    prefix TEXT NOT NULL,
    scopes TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    last_used_at INTEGER
);

CREATE TABLE IF NOT EXISTS templates (
    name TEXT PRIMARY KEY,
    image TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    cpu REAL NOT NULL DEFAULT 0,
    memory_mb INTEGER NOT NULL DEFAULT 0,
    disk_mb INTEGER NOT NULL DEFAULT 0,
    env_json TEXT NOT NULL DEFAULT '{}',
    backend TEXT NOT NULL DEFAULT '',
    builtin INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    setup_json TEXT NOT NULL DEFAULT '[]',
    warm INTEGER NOT NULL DEFAULT 0,
    network TEXT NOT NULL DEFAULT 'none',
    setup_timeout_seconds INTEGER NOT NULL DEFAULT 0,
    steps_json TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS sandboxes (
    id TEXT PRIMARY KEY,
    template TEXT NOT NULL DEFAULT '',
    image TEXT NOT NULL,
    backend TEXT NOT NULL,
    status TEXT NOT NULL,
    cpu REAL NOT NULL,
    memory_mb INTEGER NOT NULL,
    disk_mb INTEGER NOT NULL,
    pids INTEGER NOT NULL,
    env_json TEXT NOT NULL,
    network TEXT NOT NULL,
    labels_json TEXT NOT NULL,
    owner_key_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    idle_timeout_seconds INTEGER NOT NULL DEFAULT 0,
    last_activity_at INTEGER NOT NULL,
    finished_at INTEGER,
    container_id TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    mounts_json TEXT NOT NULL DEFAULT '[]',
    secrets_json TEXT NOT NULL DEFAULT '{}',
    idle_action TEXT NOT NULL DEFAULT 'stop',
    snapshot_image TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS volumes (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    owner_key_id TEXT NOT NULL,
    backend TEXT NOT NULL,
    docker_name TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    UNIQUE(owner_key_id, name)
);

CREATE TABLE IF NOT EXISTS previews (
    id TEXT PRIMARY KEY,
    sandbox_id TEXT NOT NULL,
    port INTEGER NOT NULL,
    public INTEGER NOT NULL DEFAULT 0,
    token_hash TEXT NOT NULL,
    bytes_per_sec INTEGER NOT NULL DEFAULT 0,
    max_streams INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    UNIQUE(sandbox_id, port)
);

CREATE TABLE IF NOT EXISTS built_images (
    ref TEXT PRIMARY KEY,
    last_used_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    type TEXT NOT NULL,
    sandbox_id TEXT NOT NULL,
    owner_key_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    data_json TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_events_created ON events(created_at);
CREATE INDEX IF NOT EXISTS idx_sandboxes_owner ON sandboxes(owner_key_id);
CREATE TABLE IF NOT EXISTS private_images (
    ref TEXT PRIMARY KEY,
    base TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS webhooks (
    id TEXT PRIMARY KEY,
    owner_key_id TEXT NOT NULL,
    url TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    events_json TEXT NOT NULL DEFAULT '[]',
    secret TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    cursor INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    last_attempt_at INTEGER,
    last_success_at INTEGER,
    last_status INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    failures INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_webhooks_owner ON webhooks(owner_key_id);
CREATE INDEX IF NOT EXISTS idx_sandboxes_status ON sandboxes(status);
CREATE INDEX IF NOT EXISTS idx_sandboxes_expires ON sandboxes(expires_at);
`

// CreateKey inserts a key. The plaintext secret is not stored.
func (s *Store) CreateKey(ctx context.Context, k APIKey) error {
	scopes, err := json.Marshal(k.Scopes)
	if err != nil {
		return err
	}
	network, err := json.Marshal(k.Network)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO api_keys (id, name, key_hash, prefix, scopes, network_json, registries_json, created_at, expires_at, project, quota_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		k.ID, k.Name, k.Hash, k.Prefix, string(scopes), string(network), registriesJSON(k.Registries), millis(k.CreatedAt), nullMillis(k.ExpiresAt),
		k.Project, mustJSON(k.Quota))
	return err
}

// SetKeyQuota replaces a key's quota.
func (s *Store) SetKeyQuota(ctx context.Context, id string, q KeyQuota) error {
	res, err := s.db.ExecContext(ctx, `UPDATE api_keys SET quota_json = ? WHERE id = ?`, mustJSON(q), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// KeyProject returns the project of key id, or "" when it has none or does
// not exist.
func (s *Store) KeyProject(ctx context.Context, id string) (string, error) {
	var p string
	err := s.db.QueryRowContext(ctx, `SELECT project FROM api_keys WHERE id = ?`, id).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return p, err
}

// SetKeyRegistries replaces a key's registry credentials.
func (s *Store) SetKeyRegistries(ctx context.Context, id string, regs []RegistryCred) error {
	res, err := s.db.ExecContext(ctx, `UPDATE api_keys SET registries_json = ? WHERE id = ?`, registriesJSON(regs), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func registriesJSON(regs []RegistryCred) string {
	if regs == nil {
		regs = []RegistryCred{}
	}
	return mustJSON(regs)
}

// MarkPrivateImage records that ref was fetched with a key's own registry
// credential, so later uses must ask the registry again. ref is a repository
// (driver.Repository) or a built tag; base is the built tag's base image.
func (s *Store) MarkPrivateImage(ctx context.Context, ref, base string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO private_images (ref, base, created_at) VALUES (?, ?, ?)
		ON CONFLICT(ref) DO NOTHING`, ref, base, millis(at))
	return err
}

// PrivateImage reports whether ref was marked, and the base it was built from.
func (s *Store) PrivateImage(ctx context.Context, ref string) (base string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT base FROM private_images WHERE ref = ?`, ref).Scan(&base)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return base, true, nil
}

// HasPrivateImages reports whether any image was marked private.
func (s *Store) HasPrivateImages(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM private_images LIMIT 1)`).Scan(&n)
	return n > 0, err
}

// CountKeys returns how many keys exist.
func (s *Store) CountKeys(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_keys`).Scan(&n)
	return n, err
}

// KeyByHash looks up a key by the digest of its secret.
func (s *Store) KeyByHash(ctx context.Context, hash string) (APIKey, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, key_hash, prefix, scopes, network_json, registries_json, created_at, last_used_at, expires_at, revoked_at, project, quota_json
		FROM api_keys WHERE key_hash = ?`, hash)
	return scanKey(row)
}

// KeyByID loads one key. The secret is not included.
func (s *Store) KeyByID(ctx context.Context, id string) (APIKey, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, key_hash, prefix, scopes, network_json, registries_json, created_at, last_used_at, expires_at, revoked_at, project, quota_json
		FROM api_keys WHERE id = ?`, id)
	return scanKey(row)
}

// ListKeys returns every key, newest first. Secrets are not included beyond the prefix.
func (s *Store) ListKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, key_hash, prefix, scopes, network_json, registries_json, created_at, last_used_at, expires_at, revoked_at, project, quota_json
		FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeKey marks a key revoked. Revoking a revoked key keeps the first time.
func (s *Store) RevokeKey(ctx context.Context, id string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE api_keys SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?`, millis(at), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchKey records that a key was just used.
func (s *Store) TouchKey(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = ? WHERE id = ?`, millis(at), id)
	return err
}

// UpsertBuiltin inserts or replaces a built-in template.
func (s *Store) UpsertBuiltin(ctx context.Context, t Template) error {
	env := mustJSON(t.Env)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO templates (name, image, description, cpu, memory_mb, disk_mb, env_json, backend, builtin, created_at, setup_json, warm, network, setup_timeout_seconds)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, '[]', 0, 'none', 0)
		ON CONFLICT(name) DO UPDATE SET
			image = excluded.image,
			description = excluded.description,
			cpu = excluded.cpu,
			memory_mb = excluded.memory_mb,
			disk_mb = excluded.disk_mb,
			env_json = excluded.env_json,
			builtin = 1`,
		t.Name, t.Image, t.Description, t.CPU, t.MemoryMB, t.DiskMB, env, t.Backend, millis(t.CreatedAt))
	return err
}

// CreateTemplate inserts a user template. Names that already exist fail.
func (s *Store) CreateTemplate(ctx context.Context, t Template) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO templates (name, image, description, cpu, memory_mb, disk_mb, env_json, backend, builtin, created_at, setup_json, warm, network, setup_timeout_seconds, steps_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?)`,
		t.Name, t.Image, t.Description, t.CPU, t.MemoryMB, t.DiskMB, mustJSON(t.Env), t.Backend, millis(t.CreatedAt),
		mustJSON(t.Setup), t.Warm, t.Network, t.SetupTimeout, mustJSON(stepsOrEmpty(t.Steps)))
	return err
}

// DeleteTemplate removes a non-built-in template.
func (s *Store) DeleteTemplate(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM templates WHERE name = ? AND builtin = 0`, name)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var builtin int
		err := s.db.QueryRowContext(ctx, `SELECT builtin FROM templates WHERE name = ?`, name).Scan(&builtin)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("built-in template")
	}
	return nil
}

// GetTemplate loads one template.
func (s *Store) GetTemplate(ctx context.Context, name string) (Template, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT name, image, description, cpu, memory_mb, disk_mb, env_json, backend, builtin, created_at,
		       setup_json, warm, network, setup_timeout_seconds, steps_json
		FROM templates WHERE name = ?`, name)
	return scanTemplate(row)
}

// ListTemplates returns every template, built-ins first.
func (s *Store) ListTemplates(ctx context.Context) ([]Template, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, image, description, cpu, memory_mb, disk_mb, env_json, backend, builtin, created_at,
		       setup_json, warm, network, setup_timeout_seconds, steps_json
		FROM templates ORDER BY builtin DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Template
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// InsertSandbox inserts a new sandbox row.
func (s *Store) InsertSandbox(ctx context.Context, sb Sandbox) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sandboxes (
			id, template, image, backend, status, cpu, memory_mb, disk_mb, pids,
			env_json, network, allow_json, deny_json, egress_kbps, max_conns,
			labels_json, owner_key_id, created_at, expires_at,
			idle_timeout_seconds, last_activity_at, finished_at, container_id, error, mounts_json, secrets_json,
			idle_action, snapshot_image
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sb.ID, sb.Template, sb.Image, sb.Backend, sb.Status, sb.CPU, sb.MemoryMB, sb.DiskMB, sb.PIDs,
		mustJSON(sb.Env), sb.Network, mustJSON(nonNilSlice(sb.Allow)), mustJSON(nonNilSlice(sb.Deny)), sb.EgressKbps, sb.MaxConns,
		mustJSON(sb.Labels), sb.OwnerKeyID, millis(sb.CreatedAt), millis(sb.ExpiresAt),
		sb.IdleTimeoutSeconds, millis(sb.LastActivityAt), nullMillis(sb.FinishedAt), sb.ContainerID, sb.Error, mountsJSON(sb.Mounts), mustJSON(sb.Secrets),
		idleAction(sb.IdleAction), sb.SnapshotImage)
	return err
}

// UpdateSandbox writes mutable fields of an existing row.
func (s *Store) UpdateSandbox(ctx context.Context, sb Sandbox) error {
	return s.UpdateSandboxFrom(ctx, sb)
}

// UpdateSandboxFrom writes mutable fields only while the stored status is one
// of from. No from statuses means any status. A row in another status (for
// example one a concurrent delete stopped) is left alone and ErrNotFound is
// returned, so a stale copy cannot overwrite a newer state.
func (s *Store) UpdateSandboxFrom(ctx context.Context, sb Sandbox, from ...string) error {
	q := `
		UPDATE sandboxes SET
			status = ?, expires_at = ?, idle_timeout_seconds = ?, last_activity_at = ?,
			finished_at = ?, container_id = ?, error = ?, labels_json = ?, snapshot_image = ?
		WHERE id = ?`
	args := []any{sb.Status, millis(sb.ExpiresAt), sb.IdleTimeoutSeconds, millis(sb.LastActivityAt),
		nullMillis(sb.FinishedAt), sb.ContainerID, sb.Error, mustJSON(sb.Labels), sb.SnapshotImage, sb.ID}
	if len(from) > 0 {
		q += ` AND status IN (?` + strings.Repeat(`, ?`, len(from)-1) + `)`
		for _, st := range from {
			args = append(args, st)
		}
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchSandbox records activity on a live sandbox. It only writes
// last_activity_at, and only while the row is running or paused.
func (s *Store) TouchSandbox(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE sandboxes SET last_activity_at = ?
		WHERE id = ? AND status IN ('running', 'paused')`, millis(at), id)
	return err
}

// GetSandbox loads one sandbox.
func (s *Store) GetSandbox(ctx context.Context, id string) (Sandbox, error) {
	row := s.db.QueryRowContext(ctx, sandboxSelect+` WHERE id = ?`, id)
	return scanSandbox(row)
}

// ListSandboxes returns sandboxes matching the filter, newest first.
// Label matches are applied in process; v0.1 state is small.
func (s *Store) ListSandboxes(ctx context.Context, f SandboxFilter) ([]Sandbox, error) {
	q := sandboxSelect + ` WHERE 1 = 1`
	var args []any
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	} else {
		q += ` AND status != 'warm'`
	}
	if f.Backend != "" {
		q += ` AND backend = ?`
		args = append(args, f.Backend)
	}
	if f.Template != "" {
		q += ` AND template = ?`
		args = append(args, f.Template)
	}
	if f.OwnerID != "" {
		c, a := ownerClause(f.OwnerID, f.Project)
		q += ` AND ` + c
		args = append(args, a...)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sandbox
	for rows.Next() {
		sb, err := scanSandbox(rows)
		if err != nil {
			return nil, err
		}
		if !labelsMatch(sb.Labels, f.Labels) || !matchesQuery(sb, f.Query) {
			continue
		}
		out = append(out, sb)
	}
	return out, rows.Err()
}

// CountAllActive returns sandboxes that still consume the node-wide cap.
func (s *Store) CountAllActive(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sandboxes
		WHERE status IN ('creating', 'running', 'paused', 'warm')`).Scan(&n)
	return n, err
}

// CountActive returns sandboxes that still consume the owner's quota.
func (s *Store) CountActive(ctx context.Context, ownerID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sandboxes
		WHERE owner_key_id = ? AND status IN ('creating', 'running', 'paused', 'hibernated')`, ownerID).Scan(&n)
	return n, err
}

// UsageOf sums what an owner's live sandboxes hold. Hibernated sandboxes
// count too: a wake brings their CPU and memory back without a quota check.
func (s *Store) UsageOf(ctx context.Context, ownerID string) (Usage, error) {
	var u Usage
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(cpu), 0), COALESCE(SUM(memory_mb), 0) FROM sandboxes
		WHERE owner_key_id = ? AND status IN ('creating', 'running', 'paused', 'hibernated')`, ownerID).Scan(&u.Sandboxes, &u.CPU, &u.MemoryMB)
	return u, err
}

// ownerClause selects rows owned by owner, or by any key of project when
// project is set.
func ownerClause(owner, project string) (string, []any) {
	if project == "" {
		return `owner_key_id = ?`, []any{owner}
	}
	return `(owner_key_id = ? OR owner_key_id IN (SELECT id FROM api_keys WHERE project = ?))`, []any{owner, project}
}

// ListDue returns sandboxes the reaper should act on: past their TTL, idle,
// or stuck creating. An idle sandbox whose idle action is pause or hibernate
// is due only while it is running; once paused or hibernated only its TTL
// applies.
func (s *Store) ListDue(ctx context.Context, now time.Time, creatingBefore time.Time) ([]Sandbox, error) {
	rows, err := s.db.QueryContext(ctx, sandboxSelect+`
		WHERE (
			status IN ('running', 'paused', 'hibernated') AND expires_at <= ?
		) OR (
			status IN ('running', 'paused') AND
			idle_timeout_seconds > 0 AND last_activity_at + idle_timeout_seconds * 1000 <= ? AND
			(idle_action = 'stop' OR status = 'running')
		) OR (
			status = 'creating' AND created_at <= ?
		)`, millis(now), millis(now), millis(creatingBefore))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sandbox
	for rows.Next() {
		sb, err := scanSandbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sb)
	}
	return out, rows.Err()
}

// PruneFinished deletes stopped and error rows finished before cutoff.
func (s *Store) PruneFinished(ctx context.Context, cutoff time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM sandboxes
		WHERE status IN ('stopped', 'error') AND finished_at IS NOT NULL AND finished_at <= ?`, millis(cutoff))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// CountByStatus counts sandboxes grouped by status.
func (s *Store) CountByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM sandboxes GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
	}
	return out, rows.Err()
}

const sandboxSelect = `
SELECT id, template, image, backend, status, cpu, memory_mb, disk_mb, pids,
       env_json, network, allow_json, deny_json, egress_kbps, max_conns,
       labels_json, owner_key_id, created_at, expires_at,
       idle_timeout_seconds, last_activity_at, finished_at, container_id, error, mounts_json, secrets_json,
       idle_action, snapshot_image
FROM sandboxes`

// UpdateSecrets replaces the secret map for one sandbox.
func (s *Store) UpdateSecrets(ctx context.Context, id string, secrets map[string]string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE sandboxes SET secrets_json = ? WHERE id = ?`, mustJSON(secrets), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanKey(row scanner) (APIKey, error) {
	var k APIKey
	var scopes, network, registries, quota string
	var created int64
	var last, expires, revoked sql.NullInt64
	err := row.Scan(&k.ID, &k.Name, &k.Hash, &k.Prefix, &scopes, &network, &registries, &created, &last, &expires, &revoked, &k.Project, &quota)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, err
	}
	if err := json.Unmarshal([]byte(scopes), &k.Scopes); err != nil {
		return APIKey{}, err
	}
	if network != "" && network != "{}" {
		if err := json.Unmarshal([]byte(network), &k.Network); err != nil {
			return APIKey{}, err
		}
	}
	if registries != "" && registries != "[]" {
		if err := json.Unmarshal([]byte(registries), &k.Registries); err != nil {
			return APIKey{}, err
		}
	}
	if quota != "" && quota != "{}" {
		if err := json.Unmarshal([]byte(quota), &k.Quota); err != nil {
			return APIKey{}, err
		}
	}
	k.CreatedAt = fromMillis(created)
	if last.Valid {
		t := fromMillis(last.Int64)
		k.LastUsedAt = &t
	}
	if expires.Valid {
		t := fromMillis(expires.Int64)
		k.ExpiresAt = &t
	}
	if revoked.Valid {
		t := fromMillis(revoked.Int64)
		k.RevokedAt = &t
	}
	return k, nil
}

func scanTemplate(row scanner) (Template, error) {
	var t Template
	var env, setup, network, steps string
	var builtin, warm, setupTimeout int
	var created int64
	err := row.Scan(&t.Name, &t.Image, &t.Description, &t.CPU, &t.MemoryMB, &t.DiskMB, &env, &t.Backend, &builtin, &created, &setup, &warm, &network, &setupTimeout, &steps)
	if errors.Is(err, sql.ErrNoRows) {
		return Template{}, ErrNotFound
	}
	if err != nil {
		return Template{}, err
	}
	t.Env = map[string]string{}
	if env != "" {
		if err := json.Unmarshal([]byte(env), &t.Env); err != nil {
			return Template{}, err
		}
	}
	t.Builtin = builtin == 1
	t.CreatedAt = fromMillis(created)
	t.Setup = decodeStrings(setup)
	t.Warm = warm
	t.Network = network
	if t.Network == "" {
		t.Network = "none"
	}
	t.SetupTimeout = setupTimeout
	if steps != "" && steps != "[]" {
		if err := json.Unmarshal([]byte(steps), &t.Steps); err != nil {
			return Template{}, err
		}
	}
	return t, nil
}

func stepsOrEmpty(steps []BuildStep) []BuildStep {
	if steps == nil {
		return []BuildStep{}
	}
	return steps
}

func decodeStrings(s string) []string {
	if s == "" || s == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

func scanSandbox(row scanner) (Sandbox, error) {
	var sb Sandbox
	var env, labels, allow, deny, mounts, secrets string
	var created, expires, activity int64
	var finished sql.NullInt64
	err := row.Scan(
		&sb.ID, &sb.Template, &sb.Image, &sb.Backend, &sb.Status, &sb.CPU, &sb.MemoryMB, &sb.DiskMB, &sb.PIDs,
		&env, &sb.Network, &allow, &deny, &sb.EgressKbps, &sb.MaxConns, &labels, &sb.OwnerKeyID, &created, &expires,
		&sb.IdleTimeoutSeconds, &activity, &finished, &sb.ContainerID, &sb.Error, &mounts, &secrets,
		&sb.IdleAction, &sb.SnapshotImage,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Sandbox{}, ErrNotFound
	}
	if err != nil {
		return Sandbox{}, err
	}
	sb.Env = decodeMap(env)
	sb.Labels = decodeMap(labels)
	sb.Allow = decodeSlice(allow)
	sb.Deny = decodeSlice(deny)
	sb.Mounts = decodeMounts(mounts)
	sb.Secrets = decodeMap(secrets)
	sb.CreatedAt = fromMillis(created)
	sb.ExpiresAt = fromMillis(expires)
	sb.LastActivityAt = fromMillis(activity)
	if finished.Valid {
		t := fromMillis(finished.Int64)
		sb.FinishedAt = &t
	}
	return sb, nil
}

func decodeMap(s string) map[string]string {
	out := map[string]string{}
	if s == "" {
		return out
	}
	_ = json.Unmarshal([]byte(s), &out)
	if out == nil {
		return map[string]string{}
	}
	return out
}

func decodeSlice(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

func nonNilSlice(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func mustJSON(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func labelsMatch(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func matchesQuery(sb Sandbox, q string) bool {
	if q == "" {
		return true
	}
	q = strings.ToLower(q)
	fields := []string{sb.ID, sb.Template, sb.Image, sb.Status}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), q) {
			return true
		}
	}
	for k, v := range sb.Labels {
		if strings.Contains(strings.ToLower(k), q) || strings.Contains(strings.ToLower(v), q) {
			return true
		}
	}
	return false
}

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixMilli()
}

func nullMillis(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return millis(*t)
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// ParseLabelQuery parses repeated "k:v" label filters.
func ParseLabelQuery(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, raw := range values {
		k, v, ok := strings.Cut(raw, ":")
		if !ok || k == "" {
			return nil, fmt.Errorf("label %q must be key:value", raw)
		}
		out[k] = v
	}
	return out, nil
}

// idleAction stores the default for an unset idle action.
func idleAction(a string) string {
	if a == "" {
		return "stop"
	}
	return a
}

// HibernateSandbox records that a running sandbox was saved to image and its
// container is about to be removed. It only applies while the row is still
// running with the activity time the caller saw, so a request that touched
// the sandbox during the snapshot keeps it running.
func (s *Store) HibernateSandbox(ctx context.Context, id, image string, activity time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE sandboxes SET status = 'hibernated', container_id = '', snapshot_image = ?
		WHERE id = ? AND status = 'running' AND last_activity_at = ?`, image, id, millis(activity))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateNetwork replaces the egress policy of a sandbox that is still running
// or paused. A sandbox that stopped meanwhile returns ErrNotFound.
func (s *Store) UpdateNetwork(ctx context.Context, sb Sandbox) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE sandboxes SET network = ?, allow_json = ?, deny_json = ?, egress_kbps = ?, max_conns = ?
		WHERE id = ? AND status IN ('running', 'paused')`,
		sb.Network, mustJSON(nonNilSlice(sb.Allow)), mustJSON(nonNilSlice(sb.Deny)), sb.EgressKbps, sb.MaxConns, sb.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
