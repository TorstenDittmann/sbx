package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

func mountsJSON(mounts []Mount) string {
	if len(mounts) == 0 {
		return "[]"
	}
	b, err := json.Marshal(mounts)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeMounts(s string) []Mount {
	if s == "" || s == "[]" {
		return nil
	}
	var out []Mount
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// InsertVolume stores a volume row.
func (s *Store) InsertVolume(ctx context.Context, v Volume) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO volumes (id, name, owner_key_id, backend, docker_name, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		v.ID, v.Name, v.OwnerKeyID, v.Backend, v.DockerName, millis(v.CreatedAt))
	return err
}

// GetVolume loads one volume.
func (s *Store) GetVolume(ctx context.Context, id string) (Volume, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, owner_key_id, backend, docker_name, created_at FROM volumes WHERE id = ?`, id)
	return scanVolume(row)
}

// ListVolumes returns volumes, optionally limited to one owner. Newest first.
func (s *Store) ListVolumes(ctx context.Context, ownerID string) ([]Volume, error) {
	q := `SELECT id, name, owner_key_id, backend, docker_name, created_at FROM volumes`
	var args []any
	if ownerID != "" {
		q += ` WHERE owner_key_id = ?`
		args = append(args, ownerID)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Volume
	for rows.Next() {
		v, err := scanVolume(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteVolume removes a volume row.
func (s *Store) DeleteVolume(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM volumes WHERE id = ?`, id)
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

// VolumeInUse reports whether a creating, running, or paused sandbox mounts id.
func (s *Store) VolumeInUse(ctx context.Context, volumeID string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT mounts_json FROM sandboxes WHERE status IN ('creating', 'running', 'paused')`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		for _, m := range decodeMounts(raw) {
			if m.VolumeID == volumeID {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}

func scanVolume(row scanner) (Volume, error) {
	var v Volume
	var created int64
	err := row.Scan(&v.ID, &v.Name, &v.OwnerKeyID, &v.Backend, &v.DockerName, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Volume{}, ErrNotFound
	}
	if err != nil {
		return Volume{}, err
	}
	v.CreatedAt = fromMillis(created)
	return v, nil
}
