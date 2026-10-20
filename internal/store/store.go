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
	ID         string
	Name       string
	Hash       string
	Prefix     string
	Scopes     []string
	Network    KeyNetwork
	CreatedAt  time.Time
	LastUsedAt *time.Time
	// ExpiresAt, when set, is when the key stops authenticating.
	ExpiresAt *time.Time
	// RevokedAt is set once the key has been revoked. A revoked key never
	// authenticates again.
	RevokedAt *time.Time
}

// Usable reports whether the key may still authenticate at now.
func (k APIKey) Usable(now time.Time) bool {
	if k.RevokedAt != nil {
		return false
	}
	return k.ExpiresAt == nil || now.Before(*k.ExpiresAt)
}

// HasScope reports whether the key may perform an action. admin implies every scope.
func (k APIKey) HasScope(scope string) bool {
	for _, s := range k.Scopes {
		if s == "admin" || s == scope {
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
	Builtin      bool
	CreatedAt    time.Time
}

// SystemOwner is the owner of warm-pool sandboxes. They do not count toward a key's quota.
const SystemOwner = "sbx-system"

// Sandbox is one sandbox row. Status is creating, running, paused, stopped, error, or warm.
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
	LastActivityAt     time.Time
	FinishedAt         *time.Time
	ContainerID        string
	Error              string
	Mounts             []Mount
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
	Labels   map[string]string
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
		{"api_keys", "network_json", `ALTER TABLE api_keys ADD COLUMN network_json TEXT NOT NULL DEFAULT '{}'`},
		{"api_keys", "expires_at", `ALTER TABLE api_keys ADD COLUMN expires_at INTEGER`},
		{"api_keys", "revoked_at", `ALTER TABLE api_keys ADD COLUMN revoked_at INTEGER`},
		{"templates", "setup_json", `ALTER TABLE templates ADD COLUMN setup_json TEXT NOT NULL DEFAULT '[]'`},
		{"templates", "warm", `ALTER TABLE templates ADD COLUMN warm INTEGER NOT NULL DEFAULT 0`},
		{"templates", "network", `ALTER TABLE templates ADD COLUMN network TEXT NOT NULL DEFAULT 'none'`},
		{"templates", "setup_timeout_seconds", `ALTER TABLE templates ADD COLUMN setup_timeout_seconds INTEGER NOT NULL DEFAULT 0`},
		{"templates", "backend", `ALTER TABLE templates ADD COLUMN backend TEXT NOT NULL DEFAULT ''`},
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
    setup_timeout_seconds INTEGER NOT NULL DEFAULT 0
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
    secrets_json TEXT NOT NULL DEFAULT '{}'
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

CREATE INDEX IF NOT EXISTS idx_sandboxes_owner ON sandboxes(owner_key_id);
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
		INSERT INTO api_keys (id, name, key_hash, prefix, scopes, network_json, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		k.ID, k.Name, k.Hash, k.Prefix, string(scopes), string(network), millis(k.CreatedAt), nullMillis(k.ExpiresAt))
	return err
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
		SELECT id, name, key_hash, prefix, scopes, network_json, created_at, last_used_at, expires_at, revoked_at
		FROM api_keys WHERE key_hash = ?`, hash)
	return scanKey(row)
}

// KeyByID loads one key. The secret is not included.
func (s *Store) KeyByID(ctx context.Context, id string) (APIKey, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, key_hash, prefix, scopes, network_json, created_at, last_used_at, expires_at, revoked_at
		FROM api_keys WHERE id = ?`, id)
	return scanKey(row)
}

// ListKeys returns every key, newest first. Secrets are not included beyond the prefix.
func (s *Store) ListKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, key_hash, prefix, scopes, network_json, created_at, last_used_at, expires_at, revoked_at
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
		INSERT INTO templates (name, image, description, cpu, memory_mb, disk_mb, env_json, backend, builtin, created_at, setup_json, warm, network, setup_timeout_seconds)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?)`,
		t.Name, t.Image, t.Description, t.CPU, t.MemoryMB, t.DiskMB, mustJSON(t.Env), t.Backend, millis(t.CreatedAt),
		mustJSON(t.Setup), t.Warm, t.Network, t.SetupTimeout)
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
		       setup_json, warm, network, setup_timeout_seconds
		FROM templates WHERE name = ?`, name)
	return scanTemplate(row)
}

// ListTemplates returns every template, built-ins first.
func (s *Store) ListTemplates(ctx context.Context) ([]Template, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, image, description, cpu, memory_mb, disk_mb, env_json, backend, builtin, created_at,
		       setup_json, warm, network, setup_timeout_seconds
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
			idle_timeout_seconds, last_activity_at, finished_at, container_id, error, mounts_json, secrets_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sb.ID, sb.Template, sb.Image, sb.Backend, sb.Status, sb.CPU, sb.MemoryMB, sb.DiskMB, sb.PIDs,
		mustJSON(sb.Env), sb.Network, mustJSON(nonNilSlice(sb.Allow)), mustJSON(nonNilSlice(sb.Deny)), sb.EgressKbps, sb.MaxConns,
		mustJSON(sb.Labels), sb.OwnerKeyID, millis(sb.CreatedAt), millis(sb.ExpiresAt),
		sb.IdleTimeoutSeconds, millis(sb.LastActivityAt), nullMillis(sb.FinishedAt), sb.ContainerID, sb.Error, mountsJSON(sb.Mounts), mustJSON(sb.Secrets))
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
			finished_at = ?, container_id = ?, error = ?, labels_json = ?
		WHERE id = ?`
	args := []any{sb.Status, millis(sb.ExpiresAt), sb.IdleTimeoutSeconds, millis(sb.LastActivityAt),
		nullMillis(sb.FinishedAt), sb.ContainerID, sb.Error, mustJSON(sb.Labels), sb.ID}
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
		q += ` AND owner_key_id = ?`
		args = append(args, f.OwnerID)
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
		WHERE status IN ('creating', 'running', 'paused')`).Scan(&n)
	return n, err
}

// CountActive returns sandboxes that still consume the owner's quota.
func (s *Store) CountActive(ctx context.Context, ownerID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sandboxes
		WHERE owner_key_id = ? AND status IN ('creating', 'running', 'paused')`, ownerID).Scan(&n)
	return n, err
}

// ListDue returns sandboxes the reaper should stop.
func (s *Store) ListDue(ctx context.Context, now time.Time, creatingBefore time.Time) ([]Sandbox, error) {
	rows, err := s.db.QueryContext(ctx, sandboxSelect+`
		WHERE (
			status IN ('running', 'paused') AND (
				expires_at <= ? OR
				(idle_timeout_seconds > 0 AND last_activity_at + idle_timeout_seconds * 1000 <= ?)
			)
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
       idle_timeout_seconds, last_activity_at, finished_at, container_id, error, mounts_json, secrets_json
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
	var scopes, network string
	var created int64
	var last, expires, revoked sql.NullInt64
	err := row.Scan(&k.ID, &k.Name, &k.Hash, &k.Prefix, &scopes, &network, &created, &last, &expires, &revoked)
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
	var env, setup, network string
	var builtin, warm, setupTimeout int
	var created int64
	err := row.Scan(&t.Name, &t.Image, &t.Description, &t.CPU, &t.MemoryMB, &t.DiskMB, &env, &t.Backend, &builtin, &created, &setup, &warm, &network, &setupTimeout)
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
	return t, nil
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
