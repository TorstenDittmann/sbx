package store

import (
	"context"
	"time"
)

// Preview is a persisted loopback port publication. Only the token hash is
// stored, so a preview survives a restart without keeping its secret.
type Preview struct {
	ID          string
	SandboxID   string
	Port        int
	Public      bool
	TokenHash   string
	BytesPerSec int
	MaxStreams  int
	CreatedAt   time.Time
}

// InsertPreview stores a preview row.
func (s *Store) InsertPreview(ctx context.Context, p Preview) error {
	public := 0
	if p.Public {
		public = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO previews (id, sandbox_id, port, public, token_hash, bytes_per_sec, max_streams, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.SandboxID, p.Port, public, p.TokenHash, p.BytesPerSec, p.MaxStreams, millis(p.CreatedAt))
	return err
}

// DeletePreview removes one preview row. A missing row is not an error.
func (s *Store) DeletePreview(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM previews WHERE id = ?`, id)
	return err
}

// DeleteSandboxPreviews removes every preview of a sandbox.
func (s *Store) DeleteSandboxPreviews(ctx context.Context, sandboxID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM previews WHERE sandbox_id = ?`, sandboxID)
	return err
}

// LivePreviews deletes previews whose sandbox is gone or no longer running or
// paused, then returns the rest.
func (s *Store) LivePreviews(ctx context.Context) ([]Preview, error) {
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM previews WHERE sandbox_id NOT IN (
			SELECT id FROM sandboxes WHERE status IN ('running', 'paused'))`); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, sandbox_id, port, public, token_hash, bytes_per_sec, max_streams, created_at
		FROM previews ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Preview
	for rows.Next() {
		var p Preview
		var public int
		var created int64
		if err := rows.Scan(&p.ID, &p.SandboxID, &p.Port, &public, &p.TokenHash, &p.BytesPerSec, &p.MaxStreams, &created); err != nil {
			return nil, err
		}
		p.Public = public != 0
		p.CreatedAt = fromMillis(created)
		out = append(out, p)
	}
	return out, rows.Err()
}
