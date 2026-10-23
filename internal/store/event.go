package store

import (
	"context"
	"encoding/json"
	"time"
)

// Event is one sandbox lifecycle event. Seq is assigned by the database and
// only grows, across restarts too, so a client can resume after the last Seq
// it saw.
type Event struct {
	Seq        int64
	Type       string
	SandboxID  string
	OwnerKeyID string
	Time       time.Time
	Data       map[string]any
}

// EventFilter selects events after a cursor. Empty fields are ignored.
type EventFilter struct {
	After     int64
	OwnerID   string
	SandboxID string
	Limit     int
}

// InsertEvent stores ev and returns it with Seq set.
func (s *Store) InsertEvent(ctx context.Context, ev Event) (Event, error) {
	if ev.Data == nil {
		ev.Data = map[string]any{}
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO events (type, sandbox_id, owner_key_id, created_at, data_json)
		VALUES (?, ?, ?, ?, ?)`,
		ev.Type, ev.SandboxID, ev.OwnerKeyID, millis(ev.Time), mustJSON(ev.Data))
	if err != nil {
		return Event{}, err
	}
	ev.Seq, err = res.LastInsertId()
	return ev, err
}

// ListEvents returns events after f.After in Seq order.
func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]Event, error) {
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	q := `SELECT seq, type, sandbox_id, owner_key_id, created_at, data_json FROM events WHERE seq > ?`
	args := []any{f.After}
	if f.OwnerID != "" {
		q += ` AND owner_key_id = ?`
		args = append(args, f.OwnerID)
	}
	if f.SandboxID != "" {
		q += ` AND sandbox_id = ?`
		args = append(args, f.SandboxID)
	}
	q += ` ORDER BY seq ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var ev Event
		var at int64
		var data string
		if err := rows.Scan(&ev.Seq, &ev.Type, &ev.SandboxID, &ev.OwnerKeyID, &at, &data); err != nil {
			return nil, err
		}
		ev.Time = fromMillis(at)
		ev.Data = map[string]any{}
		_ = json.Unmarshal([]byte(data), &ev.Data)
		if ev.Data == nil {
			ev.Data = map[string]any{}
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// LastEventSeq is the newest Seq, or 0 when there are no events.
func (s *Store) LastEventSeq(ctx context.Context) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&seq)
	return seq, err
}

// PruneEvents deletes events older than cutoff.
func (s *Store) PruneEvents(ctx context.Context, cutoff time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE created_at <= ?`, millis(cutoff))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
