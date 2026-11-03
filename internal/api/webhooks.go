package api

import (
	"net/http"
	"time"

	"github.com/TorstenDittmann/sbx/internal/core"
	"github.com/TorstenDittmann/sbx/internal/store"
)

type webhookResponse struct {
	ID            string     `json:"id"`
	URL           string     `json:"url"`
	Description   string     `json:"description"`
	Events        []string   `json:"events"`
	Enabled       bool       `json:"enabled"`
	Secret        string     `json:"secret,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	LastStatus    int        `json:"last_status"`
	LastError     string     `json:"last_error"`
	Failures      int        `json:"failures"`
}

// webhookJSON renders a webhook. The secret is only included right after
// it was made, on create and rotate.
func webhookJSON(wh store.Webhook, withSecret bool) webhookResponse {
	events := wh.Events
	if events == nil {
		events = []string{}
	}
	out := webhookResponse{
		ID: wh.ID, URL: wh.URL, Description: wh.Description, Events: events, Enabled: wh.Enabled,
		CreatedAt: wh.CreatedAt, UpdatedAt: wh.UpdatedAt, LastAttemptAt: wh.LastAttemptAt,
		LastSuccessAt: wh.LastSuccessAt, LastStatus: wh.LastStatus, LastError: wh.LastError, Failures: wh.Failures,
	}
	if withSecret {
		out.Secret = wh.Secret
	}
	return out
}

func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL         string   `json:"url"`
		Description string   `json:"description"`
		Events      []string `json:"events"`
	}
	if err := decodeJSON(r, 64<<10, &body); err != nil {
		writeDecodeErr(w, err)
		return
	}
	wh, err := s.mgr.CreateWebhook(r.Context(), actor(r), core.WebhookInput{
		URL: body.URL, Description: body.Description, Events: body.Events,
	})
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, webhookJSON(wh, true))
}

func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request) {
	items, err := s.mgr.ListWebhooks(r.Context(), actor(r))
	if err != nil {
		writeManager(w, err)
		return
	}
	out := make([]webhookResponse, 0, len(items))
	for _, wh := range items {
		out = append(out, webhookJSON(wh, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"webhooks": out})
}

func (s *Server) getWebhook(w http.ResponseWriter, r *http.Request) {
	wh, err := s.mgr.GetWebhook(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusOK, webhookJSON(wh, false))
}

func (s *Server) updateWebhook(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL         *string   `json:"url"`
		Description *string   `json:"description"`
		Events      *[]string `json:"events"`
		Enabled     *bool     `json:"enabled"`
	}
	if err := decodeJSON(r, 64<<10, &body); err != nil {
		writeDecodeErr(w, err)
		return
	}
	wh, err := s.mgr.UpdateWebhook(r.Context(), actor(r), r.PathValue("id"), core.WebhookPatch{
		URL: body.URL, Description: body.Description, Events: body.Events, Enabled: body.Enabled,
	})
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusOK, webhookJSON(wh, false))
}

func (s *Server) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.DeleteWebhook(r.Context(), actor(r), r.PathValue("id")); err != nil {
		writeManager(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) rotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	wh, err := s.mgr.RotateWebhookSecret(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusOK, webhookJSON(wh, true))
}

func (s *Server) testWebhook(w http.ResponseWriter, r *http.Request) {
	res, err := s.mgr.TestWebhook(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": res.OK, "status_code": res.StatusCode, "error": res.Error, "duration_ms": res.Duration.Milliseconds(),
	})
}
