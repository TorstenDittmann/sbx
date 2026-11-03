package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Webhook is an external URL that receives lifecycle events. Events lists
// the event types it wants; empty means every type. Cursor is the last event
// Seq handled, delivered or skipped, so delivery resumes after a restart.
type Webhook struct {
	ID          string
	OwnerKeyID  string
	URL         string
	Description string
	Events      []string
	Secret      string
	Enabled     bool
	Cursor      int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// Delivery status, for the owner to see why events are not arriving.
	LastAttemptAt *time.Time
	LastSuccessAt *time.Time
	LastStatus    int
	LastError     string
	Failures      int
}

// WebhookDelivery is the outcome of one delivery attempt.
type WebhookDelivery struct {
	At     time.Time
	Status int
	Error  string
	OK     bool
}

const webhookColumns = `id, owner_key_id, url, description, events_json, secret, enabled, cursor,
	created_at, updated_at, last_attempt_at, last_success_at, last_status, last_error, failures`

// InsertWebhook stores a new webhook.
func (s *Store) InsertWebhook(ctx context.Context, w Webhook) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO webhooks (id, owner_key_id, url, description, events_json, secret, enabled, cursor, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.OwnerKeyID, w.URL, w.Description, eventsJSON(w.Events), w.Secret, boolInt(w.Enabled),
		w.Cursor, millis(w.CreatedAt), millis(w.UpdatedAt))
	return err
}

// GetWebhook loads one webhook.
func (s *Store) GetWebhook(ctx context.Context, id string) (Webhook, error) {
	return scanWebhook(s.db.QueryRowContext(ctx, `SELECT `+webhookColumns+` FROM webhooks WHERE id = ?`, id))
}

// ListWebhooks returns webhooks, optionally for one owner, oldest first.
func (s *Store) ListWebhooks(ctx context.Context, ownerID string) ([]Webhook, error) {
	q := `SELECT ` + webhookColumns + ` FROM webhooks`
	var args []any
	if ownerID != "" {
		q += ` WHERE owner_key_id = ?`
		args = append(args, ownerID)
	}
	q += ` ORDER BY created_at ASC, id ASC`
	return s.queryWebhooks(ctx, q, args...)
}

// ListEnabledWebhooks returns every enabled webhook.
func (s *Store) ListEnabledWebhooks(ctx context.Context) ([]Webhook, error) {
	return s.queryWebhooks(ctx, `SELECT `+webhookColumns+` FROM webhooks WHERE enabled = 1 ORDER BY id`)
}

// CountWebhooks counts an owner's webhooks.
func (s *Store) CountWebhooks(ctx context.Context, ownerID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhooks WHERE owner_key_id = ?`, ownerID).Scan(&n)
	return n, err
}

// UpdateWebhook saves the settings an owner can change: url, description,
// events, secret, enabled, and cursor. Enabling clears the failure count.
func (s *Store) UpdateWebhook(ctx context.Context, w Webhook) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE webhooks SET url = ?, description = ?, events_json = ?, secret = ?, enabled = ?,
			cursor = ?, updated_at = ?, failures = CASE WHEN ? = 1 AND enabled = 0 THEN 0 ELSE failures END
		WHERE id = ?`,
		w.URL, w.Description, eventsJSON(w.Events), w.Secret, boolInt(w.Enabled),
		w.Cursor, millis(w.UpdatedAt), boolInt(w.Enabled), w.ID)
	return affected(res, err)
}

// DeleteWebhook removes a webhook.
func (s *Store) DeleteWebhook(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM webhooks WHERE id = ?`, id)
	return affected(res, err)
}

// AdvanceWebhook moves a webhook's cursor forward. It never moves it back,
// so a delivery that raced an owner's update cannot replay events.
func (s *Store) AdvanceWebhook(ctx context.Context, id string, cursor int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE webhooks SET cursor = ? WHERE id = ? AND cursor < ?`, cursor, id, cursor)
	return err
}

// RecordWebhookDelivery stores the outcome of an attempt. A success resets
// the failure count; a failure increments it.
func (s *Store) RecordWebhookDelivery(ctx context.Context, id string, d WebhookDelivery) error {
	if d.OK {
		_, err := s.db.ExecContext(ctx, `
			UPDATE webhooks SET last_attempt_at = ?, last_success_at = ?, last_status = ?, last_error = '', failures = 0
			WHERE id = ?`, millis(d.At), millis(d.At), d.Status, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE webhooks SET last_attempt_at = ?, last_status = ?, last_error = ?, failures = failures + 1
		WHERE id = ?`, millis(d.At), d.Status, d.Error, id)
	return err
}

// DisableWebhook turns a webhook off and records why.
func (s *Store) DisableWebhook(ctx context.Context, id, reason string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE webhooks SET enabled = 0, last_error = ?, updated_at = ? WHERE id = ?`,
		reason, millis(at), id)
	return err
}

func (s *Store) queryWebhooks(ctx context.Context, q string, args ...any) ([]Webhook, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Webhook{}
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func scanWebhook(row scanner) (Webhook, error) {
	var w Webhook
	var events string
	var enabled int
	var created, updated int64
	var attempt, success sql.NullInt64
	err := row.Scan(&w.ID, &w.OwnerKeyID, &w.URL, &w.Description, &events, &w.Secret, &enabled, &w.Cursor,
		&created, &updated, &attempt, &success, &w.LastStatus, &w.LastError, &w.Failures)
	if errors.Is(err, sql.ErrNoRows) {
		return Webhook{}, ErrNotFound
	}
	if err != nil {
		return Webhook{}, err
	}
	w.Enabled = enabled != 0
	w.CreatedAt = fromMillis(created)
	w.UpdatedAt = fromMillis(updated)
	if attempt.Valid {
		t := fromMillis(attempt.Int64)
		w.LastAttemptAt = &t
	}
	if success.Valid {
		t := fromMillis(success.Int64)
		w.LastSuccessAt = &t
	}
	_ = json.Unmarshal([]byte(events), &w.Events)
	return w, nil
}

func eventsJSON(events []string) string {
	if len(events) == 0 {
		return "[]"
	}
	b, err := json.Marshal(events)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func affected(res sql.Result, err error) error {
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
