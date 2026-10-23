package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// TouchImage records that a create or build used a built image.
func (s *Store) TouchImage(ctx context.Context, ref string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO built_images (ref, last_used_at) VALUES (?, ?)
		ON CONFLICT(ref) DO UPDATE SET last_used_at = MAX(last_used_at, excluded.last_used_at)`,
		ref, millis(at))
	return err
}

// ImageLastUsed returns when ref was last used. ok is false when no create
// or build has recorded it.
func (s *Store) ImageLastUsed(ctx context.Context, ref string) (at time.Time, ok bool, err error) {
	var ms int64
	err = s.db.QueryRowContext(ctx, `SELECT last_used_at FROM built_images WHERE ref = ?`, ref).Scan(&ms)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return fromMillis(ms), true, nil
}

// ForgetImage drops the usage row of a removed image.
func (s *Store) ForgetImage(ctx context.Context, ref string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM built_images WHERE ref = ?`, ref)
	return err
}
