package store

import (
	"context"
	"time"
)

// CountWarm counts warm sandboxes for one template and backend.
func (s *Store) CountWarm(ctx context.Context, template, backend string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sandboxes
		WHERE template = ? AND backend = ? AND status = 'warm'`, template, backend).Scan(&n)
	return n, err
}

// CountPooling counts warm and in-progress pool sandboxes owned by the system.
func (s *Store) CountPooling(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sandboxes
		WHERE owner_key_id = ? AND status IN ('warm', 'creating')`, SystemOwner).Scan(&n)
	return n, err
}

// ClaimWarm assigns the oldest warm sandbox for a template to owner.
// ErrNotFound means the pool was empty.
func (s *Store) ClaimWarm(ctx context.Context, template, backend, owner string, labels map[string]string, ttl, idle int, now time.Time) (Sandbox, error) {
	expires := now.Add(time.Duration(ttl) * time.Second)
	row := s.db.QueryRowContext(ctx, `
		UPDATE sandboxes SET
			owner_key_id = ?,
			status = 'running',
			expires_at = ?,
			idle_timeout_seconds = ?,
			last_activity_at = ?,
			created_at = ?,
			labels_json = ?,
			error = ''
		WHERE id = (
			SELECT id FROM sandboxes
			WHERE template = ? AND backend = ? AND status = 'warm'
			ORDER BY created_at ASC
			LIMIT 1
		)
		RETURNING id, template, image, backend, status, cpu, memory_mb, disk_mb, pids,
		          env_json, network, allow_json, deny_json, egress_kbps, max_conns,
		          labels_json, owner_key_id, created_at, expires_at,
		          idle_timeout_seconds, last_activity_at, finished_at, container_id, error, mounts_json, secrets_json`,
		owner, millis(expires), idle, millis(now), millis(now), mustJSON(labels), template, backend)
	return scanSandbox(row)
}

// ListPool returns system-owned warm or creating sandboxes for a template.
func (s *Store) ListPool(ctx context.Context, template string) ([]Sandbox, error) {
	rows, err := s.db.QueryContext(ctx, sandboxSelect+`
		WHERE template = ? AND owner_key_id = ? AND status IN ('warm', 'creating')
		ORDER BY created_at ASC`, template, SystemOwner)
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
