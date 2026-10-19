// Package api is the /v1 HTTP server.
package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TorstenDittmann/sbx/internal/core"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/id"
	"github.com/TorstenDittmann/sbx/internal/store"
	"github.com/TorstenDittmann/sbx/internal/version"
)

type contextKey int

const actorKey contextKey = 1

type principal struct {
	actor core.Actor
	key   store.APIKey
}

// Server serves the sbx HTTP API.
type Server struct {
	mgr  *core.Manager
	log  *slog.Logger
	spec []byte
	lim  *limiter
}

// New returns a server. spec is the OpenAPI document served at /openapi.yaml.
func New(mgr *core.Manager, log *slog.Logger, spec []byte) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{mgr: mgr, log: log, spec: spec, lim: newLimiter(mgr.Config().Limits.RequestsPerMinute)}
}

// Handler is the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.root)
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("GET /openapi.yaml", s.openapi)

	mux.HandleFunc("POST /v1/keys", s.auth("admin", s.createKey))
	mux.HandleFunc("GET /v1/keys", s.auth("admin", s.listKeys))

	mux.HandleFunc("GET /v1/templates", s.auth("sandbox", s.listTemplates))
	mux.HandleFunc("POST /v1/templates", s.auth("admin", s.createTemplate))
	mux.HandleFunc("DELETE /v1/templates/{name}", s.auth("admin", s.deleteTemplate))

	mux.HandleFunc("POST /v1/sandboxes", s.auth("sandbox", s.createSandbox))
	mux.HandleFunc("GET /v1/sandboxes", s.auth("sandbox", s.listSandboxes))
	mux.HandleFunc("GET /v1/sandboxes/{id}", s.auth("sandbox", s.getSandbox))
	mux.HandleFunc("DELETE /v1/sandboxes/{id}", s.auth("sandbox", s.deleteSandbox))
	mux.HandleFunc("POST /v1/sandboxes/{id}/timeout", s.auth("sandbox", s.timeoutSandbox))
	mux.HandleFunc("POST /v1/sandboxes/{id}/pause", s.auth("sandbox", s.pauseSandbox))
	mux.HandleFunc("POST /v1/sandboxes/{id}/resume", s.auth("sandbox", s.resumeSandbox))
	mux.HandleFunc("POST /v1/sandboxes/{id}/exec", s.auth("sandbox", s.execSandbox))

	mux.HandleFunc("POST /v1/sandboxes/{id}/processes", s.auth("sandbox", s.createProcess))
	mux.HandleFunc("GET /v1/sandboxes/{id}/processes", s.auth("sandbox", s.listProcesses))
	mux.HandleFunc("GET /v1/sandboxes/{id}/processes/{procId}", s.auth("sandbox", s.getProcess))
	mux.HandleFunc("DELETE /v1/sandboxes/{id}/processes/{procId}", s.auth("sandbox", s.deleteProcess))
	mux.HandleFunc("POST /v1/sandboxes/{id}/processes/{procId}/signal", s.auth("sandbox", s.signalProcess))
	mux.HandleFunc("POST /v1/sandboxes/{id}/processes/{procId}/stdin", s.auth("sandbox", s.processStdin))
	mux.HandleFunc("GET /v1/sandboxes/{id}/processes/{procId}/stream", s.auth("sandbox", s.streamProcess))

	mux.HandleFunc("POST /v1/sandboxes/{id}/terminals", s.auth("sandbox", s.createTerminal))
	mux.HandleFunc("DELETE /v1/sandboxes/{id}/terminals/{termId}", s.auth("sandbox", s.deleteTerminal))
	mux.HandleFunc("POST /v1/sandboxes/{id}/terminals/{termId}/resize", s.auth("sandbox", s.resizeTerminal))
	mux.HandleFunc("GET /v1/sandboxes/{id}/terminals/{termId}/ws", s.auth("sandbox", s.terminalSocket))

	mux.HandleFunc("GET /v1/sandboxes/{id}/files", s.auth("sandbox", s.listFiles))
	mux.HandleFunc("DELETE /v1/sandboxes/{id}/files", s.auth("sandbox", s.deleteFile))
	mux.HandleFunc("GET /v1/sandboxes/{id}/files/content", s.auth("sandbox", s.downloadFile))
	mux.HandleFunc("PUT /v1/sandboxes/{id}/files/content", s.auth("sandbox", s.uploadFile))
	mux.HandleFunc("GET /v1/sandboxes/{id}/files/archive", s.auth("sandbox", s.downloadArchive))
	mux.HandleFunc("POST /v1/sandboxes/{id}/files/archive", s.auth("sandbox", s.uploadArchive))
	mux.HandleFunc("GET /v1/sandboxes/{id}/files/stat", s.auth("sandbox", s.statFile))
	mux.HandleFunc("POST /v1/sandboxes/{id}/files/mkdir", s.auth("sandbox", s.mkdir))
	mux.HandleFunc("GET /v1/sandboxes/{id}/files/watch", s.auth("sandbox", s.watchFiles))

	mux.HandleFunc("POST /v1/sandboxes/{id}/previews", s.auth("sandbox", s.createPreview))
	mux.HandleFunc("GET /v1/sandboxes/{id}/previews", s.auth("sandbox", s.listPreviews))
	mux.HandleFunc("DELETE /v1/sandboxes/{id}/previews/{previewId}", s.auth("sandbox", s.deletePreview))
	mux.HandleFunc("/v1/sandboxes/{id}/previews/{previewId}/{$}", s.auth("sandbox", s.proxyPreview))
	mux.HandleFunc("/v1/sandboxes/{id}/previews/{previewId}/{path...}", s.auth("sandbox", s.proxyPreview))
	mux.HandleFunc("/p/{sandboxId}/{port}/{$}", s.proxyPublicPath)
	mux.HandleFunc("/p/{sandboxId}/{port}/{path...}", s.proxyPublicPath)

	return s.recover(s.logRequests(s.limitBody(s.routePreviewHost(mux))))
}

func (s *Server) root(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"name":    "sbx",
		"version": version.Version,
		"health":  "/healthz",
		"openapi": "/openapi.yaml",
		"docs":    "https://github.com/TorstenDittmann/sbx",
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	d, ok := s.mgr.Driver(s.mgr.Config().Backend)
	body := map[string]string{
		"status":  "ok",
		"version": version.Version,
		"backend": s.mgr.Config().Backend,
		"docker":  "ok",
	}
	if !ok {
		body["status"] = "degraded"
		body["docker"] = "backend not configured"
		writeJSON(w, http.StatusServiceUnavailable, body)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := d.Ping(ctx); err != nil {
		body["status"] = "degraded"
		body["docker"] = "unreachable"
		writeJSON(w, http.StatusServiceUnavailable, body)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if !s.mgr.Ready() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status":  "starting",
			"version": version.Version,
			"backend": s.mgr.Config().Backend,
		})
		return
	}
	s.health(w, r)
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	counts, err := s.mgr.Store().CountByStatus(r.Context())
	if err != nil {
		http.Error(w, "metrics unavailable", http.StatusInternalServerError)
		return
	}
	var b strings.Builder
	b.WriteString("# HELP sbx_info Build information.\n# TYPE sbx_info gauge\n")
	fmt.Fprintf(&b, "sbx_info{version=%q} 1\n", version.Version)
	b.WriteString("# HELP sbx_sandboxes Sandboxes by status.\n# TYPE sbx_sandboxes gauge\n")
	for _, status := range []string{"creating", "running", "paused", "stopped", "error"} {
		fmt.Fprintf(&b, "sbx_sandboxes{status=%q} %d\n", status, counts[status])
	}
	c := &s.mgr.Counts
	b.WriteString("# HELP sbx_sandbox_creates_total Sandboxes created.\n# TYPE sbx_sandbox_creates_total counter\n")
	fmt.Fprintf(&b, "sbx_sandbox_creates_total %d\n", c.Creates.Load())
	b.WriteString("# HELP sbx_sandbox_deletes_total Sandboxes deleted by API.\n# TYPE sbx_sandbox_deletes_total counter\n")
	fmt.Fprintf(&b, "sbx_sandbox_deletes_total %d\n", c.Deletes.Load())
	b.WriteString("# HELP sbx_sandbox_reaped_total Sandboxes stopped by the reaper.\n# TYPE sbx_sandbox_reaped_total counter\n")
	fmt.Fprintf(&b, "sbx_sandbox_reaped_total %d\n", c.Reaped.Load())
	b.WriteString("# HELP sbx_execs_total Completed exec calls.\n# TYPE sbx_execs_total counter\n")
	fmt.Fprintf(&b, "sbx_execs_total %d\n", c.Execs.Load())
	b.WriteString("# HELP sbx_exec_errors_total Failed exec calls.\n# TYPE sbx_exec_errors_total counter\n")
	fmt.Fprintf(&b, "sbx_exec_errors_total %d\n", c.ExecErrors.Load())
	b.WriteString("# HELP sbx_file_ops_total File operations.\n# TYPE sbx_file_ops_total counter\n")
	fmt.Fprintf(&b, "sbx_file_ops_total %d\n", c.FileOps.Load())
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, b.String())
}

func (s *Server) openapi(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.spec)
}

func (s *Server) auth(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, err := s.authenticate(r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "unauthorized", err.Error())
			return
		}
		if !key.HasScope(scope) {
			writeErr(w, http.StatusForbidden, "forbidden", "API key lacks the "+scope+" scope")
			return
		}
		if !s.lim.allow(key.ID) {
			writeErr(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		_ = s.mgr.Store().TouchKey(r.Context(), key.ID, time.Now().UTC())
		p := principal{actor: core.Actor{KeyID: key.ID, Admin: key.Admin()}, key: key}
		next(w, r.WithContext(context.WithValue(r.Context(), actorKey, p)))
	}
}

func (s *Server) authenticate(r *http.Request) (store.APIKey, error) {
	token := ""
	scheme, bearer, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if ok && strings.EqualFold(scheme, "Bearer") {
		token = bearer
	}
	if token == "" {
		token = r.URL.Query().Get("access_token")
	}
	if token == "" {
		return store.APIKey{}, errors.New("missing bearer token")
	}
	if !strings.HasPrefix(token, "sbx_") {
		return store.APIKey{}, errors.New("invalid API key")
	}
	key, err := s.mgr.Store().KeyByHash(r.Context(), id.Hash(token))
	if errors.Is(err, store.ErrNotFound) {
		return store.APIKey{}, errors.New("invalid API key")
	}
	if err != nil {
		return store.APIKey{}, errors.New("invalid API key")
	}
	return key, nil
}

func actor(r *http.Request) core.Actor {
	p, _ := r.Context().Value(actorKey).(principal)
	return p.actor
}

func (s *Server) createKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string   `json:"name"`
		Scopes  []string `json:"scopes"`
		Network *struct {
			Mode       string    `json:"mode"`
			Allow      *[]string `json:"allow"`
			Deny       *[]string `json:"deny"`
			EgressKbps int       `json:"egress_kbps"`
			MaxConns   int       `json:"max_connections"`
		} `json:"network"`
	}
	if err := decodeJSON(r, 1<<20, &body); err != nil {
		writeDecodeErr(w, err)
		return
	}
	if body.Name == "" || len(body.Name) > 64 {
		writeErr(w, http.StatusBadRequest, "invalid", "name is required")
		return
	}
	if len(body.Scopes) == 0 {
		body.Scopes = []string{"sandbox"}
	}
	for _, scope := range body.Scopes {
		if scope != "admin" && scope != "sandbox" {
			writeErr(w, http.StatusBadRequest, "invalid", "scopes must be admin or sandbox")
			return
		}
	}
	var network store.KeyNetwork
	if body.Network != nil {
		network.Mode = body.Network.Mode
		network.Allow = body.Network.Allow
		network.Deny = body.Network.Deny
		network.EgressKbps = body.Network.EgressKbps
		network.MaxConns = body.Network.MaxConns
		if network.Mode != "" && network.Mode != "none" && network.Mode != "full" && network.Mode != "allow" {
			writeErr(w, http.StatusBadRequest, "invalid", "network.mode must be none, full, or allow")
			return
		}
		if network.EgressKbps < 0 || network.MaxConns < 0 {
			writeErr(w, http.StatusBadRequest, "invalid", "bandwidth and connection limits must be >= 0")
			return
		}
	}
	plain, hash, prefix := id.Key()
	key := store.APIKey{
		ID: id.RecordID(), Name: body.Name, Hash: hash, Prefix: prefix,
		Scopes: body.Scopes, Network: network, CreatedAt: time.Now().UTC(),
	}
	if err := s.mgr.Store().CreateKey(r.Context(), key); err != nil {
		s.log.Error("create key", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal", "could not create key")
		return
	}
	writeJSON(w, http.StatusCreated, keyJSON(key, plain))
}

func keyJSON(key store.APIKey, secret string) map[string]any {
	body := map[string]any{
		"id":         key.ID,
		"name":       key.Name,
		"prefix":     key.Prefix,
		"scopes":     key.Scopes,
		"created_at": key.CreatedAt,
		"network": map[string]any{
			"mode":            key.Network.Mode,
			"allow":           stringSlice(key.Network.Allow),
			"deny":            stringSlice(key.Network.Deny),
			"egress_kbps":     key.Network.EgressKbps,
			"max_connections": key.Network.MaxConns,
		},
	}
	if secret != "" {
		body["key"] = secret
	}
	if key.LastUsedAt != nil {
		body["last_used_at"] = key.LastUsedAt
	}
	return body
}

func stringSlice(v *[]string) []string {
	if v == nil {
		return []string{}
	}
	return *v
}

func (s *Server) listKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.mgr.Store().ListKeys(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "could not list keys")
		return
	}
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, keyJSON(k, ""))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	items, err := s.mgr.ListTemplates(r.Context())
	if err != nil {
		writeManager(w, err)
		return
	}
	out := make([]any, 0, len(items))
	for _, t := range items {
		out = append(out, templateJSON(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": out})
}

func (s *Server) createTemplate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string            `json:"name"`
		Image       string            `json:"image"`
		Description string            `json:"description"`
		CPU         float64           `json:"cpu"`
		MemoryMB    int               `json:"memory_mb"`
		DiskMB      int               `json:"disk_mb"`
		Env         map[string]string `json:"env"`
	}
	if err := decodeJSON(r, 1<<20, &body); err != nil {
		writeDecodeErr(w, err)
		return
	}
	t, err := s.mgr.CreateTemplate(r.Context(), core.TemplateInput{
		Name: body.Name, Image: body.Image, Description: body.Description,
		CPU: body.CPU, MemoryMB: body.MemoryMB, DiskMB: body.DiskMB, Env: body.Env,
	})
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, templateJSON(t))
}

func (s *Server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.DeleteTemplate(r.Context(), r.PathValue("name")); err != nil {
		writeManager(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createSandbox(w http.ResponseWriter, r *http.Request) {
	var body createRequest
	if err := decodeJSON(r, 1<<20, &body); err != nil {
		writeDecodeErr(w, err)
		return
	}
	in := core.CreateInput{
		Template: body.Template, Image: body.Image, CPU: body.CPU, MemoryMB: body.MemoryMB,
		DiskMB: body.DiskMB, PIDs: body.PIDs, TTLSeconds: body.TTLSeconds,
		IdleTimeoutSeconds: body.IdleTimeoutSeconds, Env: body.Env, Backend: body.Backend, Labels: body.Labels,
	}
	if body.Network != nil {
		in.Network = body.Network.Mode
		if body.Network.Allow != nil {
			in.HasAllow = true
			in.Allow = *body.Network.Allow
		}
		if body.Network.Deny != nil {
			in.HasDeny = true
			in.Deny = *body.Network.Deny
		}
		in.EgressKbps = body.Network.EgressKbps
		in.MaxConns = body.Network.MaxConns
	}
	sb, err := s.mgr.Create(r.Context(), actor(r), in)
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sandboxJSON(sb))
}

func (s *Server) listSandboxes(w http.ResponseWriter, r *http.Request) {
	labels, err := store.ParseLabelQuery(r.URL.Query()["label"])
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	items, err := s.mgr.List(r.Context(), actor(r), r.URL.Query().Get("status"), r.URL.Query().Get("backend"), labels)
	if err != nil {
		writeManager(w, err)
		return
	}
	out := make([]sandboxResponse, 0, len(items))
	for _, sb := range items {
		out = append(out, sandboxJSON(sb))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sandboxes": out})
}

func (s *Server) getSandbox(w http.ResponseWriter, r *http.Request) {
	sb, err := s.mgr.Get(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxJSON(sb))
}

func (s *Server) deleteSandbox(w http.ResponseWriter, r *http.Request) {
	sb, err := s.mgr.Delete(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxJSON(sb))
}

func (s *Server) timeoutSandbox(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TTL int `json:"ttl_seconds"`
	}
	if err := decodeJSON(r, 1<<20, &body); err != nil {
		writeDecodeErr(w, err)
		return
	}
	sb, err := s.mgr.SetTTL(r.Context(), actor(r), r.PathValue("id"), body.TTL)
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxJSON(sb))
}

func (s *Server) pauseSandbox(w http.ResponseWriter, r *http.Request) {
	sb, err := s.mgr.Pause(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxJSON(sb))
}

func (s *Server) resumeSandbox(w http.ResponseWriter, r *http.Request) {
	sb, err := s.mgr.Resume(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sandboxJSON(sb))
}

func (s *Server) execSandbox(w http.ResponseWriter, r *http.Request) {
	in, err := decodeExec(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	stream := r.URL.Query().Get("stream")
	if stream == "1" || stream == "true" {
		s.streamExec(w, r, r.PathValue("id"), in)
		return
	}
	res, err := s.mgr.Exec(r.Context(), actor(r), r.PathValue("id"), in)
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusOK, execJSON(res))
}

func (s *Server) streamExec(w http.ResponseWriter, r *http.Request, sandboxID string, in core.ExecInput) {
	flusher, _ := w.(http.Flusher)
	headerSent := false
	send := func(event string, payload any) {
		if !headerSent {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			headerSent = true
		}
		b, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	res, err := s.mgr.ExecStream(r.Context(), actor(r), sandboxID, in, func(chunk driver.ExecChunk) error {
		send(string(chunk.Kind), map[string]string{"data": string(chunk.Data)})
		return nil
	})
	if err != nil && !headerSent {
		writeManager(w, err)
		return
	}
	if err != nil {
		var op *core.OpError
		code, msg := "driver", err.Error()
		if errors.As(err, &op) {
			code, msg = op.Code, op.Msg
		}
		send("error", map[string]string{"code": code, "message": msg})
		return
	}
	send("exit", map[string]any{
		"exit_code":   res.ExitCode,
		"duration_ms": res.Duration.Milliseconds(),
		"truncated":   res.Truncated,
	})
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	entries, err := s.mgr.ListFiles(r.Context(), actor(r), r.PathValue("id"), p)
	if err != nil {
		writeManager(w, err)
		return
	}
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{
			"name": e.Name, "path": e.Path, "type": e.Type, "size": e.Size,
			"mode": fmt.Sprintf("%o", e.Mode), "mtime": e.ModTime,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "entries": out})
}

func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	b, err := s.mgr.ReadFile(r.Context(), actor(r), r.PathValue("id"), p)
	if err != nil {
		writeManager(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileBase(p)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	mode, err := parseMode(r.URL.Query().Get("mode"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	body, err := readBody(w, r, int64(s.mgr.Config().Limits.MaxUploadBytes))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if err := s.mgr.WriteFile(r.Context(), actor(r), r.PathValue("id"), p, body, mode); err != nil {
		writeManager(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.RemoveFile(r.Context(), actor(r), r.PathValue("id"), r.URL.Query().Get("path")); err != nil {
		writeManager(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) uploadArchive(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(w, r, int64(s.mgr.Config().Limits.MaxUploadBytes))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if err := s.mgr.WriteArchive(r.Context(), actor(r), r.PathValue("id"), r.URL.Query().Get("path"), body); err != nil {
		writeManager(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) downloadArchive(w http.ResponseWriter, r *http.Request) {
	b, err := s.mgr.ReadArchive(r.Context(), actor(r), r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		writeManager(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", "attachment; filename=\"archive.tar.gz\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
	})
}

func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic", "err", rec, "path", r.URL.Path)
				writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijack is not supported")
	}
	return h.Hijack()
}

type createRequest struct {
	Template           string            `json:"template"`
	Image              string            `json:"image"`
	CPU                *float64          `json:"cpu"`
	MemoryMB           *int              `json:"memory_mb"`
	DiskMB             *int              `json:"disk_mb"`
	PIDs               *int              `json:"pids"`
	TTLSeconds         *int              `json:"ttl_seconds"`
	IdleTimeoutSeconds *int              `json:"idle_timeout_seconds"`
	Env                map[string]string `json:"env"`
	Network            *struct {
		Mode       string    `json:"mode"`
		Allow      *[]string `json:"allow"`
		Deny       *[]string `json:"deny"`
		EgressKbps *int      `json:"egress_kbps"`
		MaxConns   *int      `json:"max_connections"`
	} `json:"network"`
	Backend string            `json:"backend"`
	Labels  map[string]string `json:"labels"`
}

type execRequest struct {
	Cmd            []string          `json:"cmd"`
	Cwd            string            `json:"cwd"`
	Env            map[string]string `json:"env"`
	TimeoutSeconds *int              `json:"timeout_seconds"`
	Stdin          string            `json:"stdin"`
	StdinBase64    string            `json:"stdin_base64"`
}

type sandboxResponse struct {
	ID                 string            `json:"id"`
	Status             string            `json:"status"`
	Backend            string            `json:"backend"`
	Template           string            `json:"template,omitempty"`
	Image              string            `json:"image"`
	CPU                float64           `json:"cpu"`
	MemoryMB           int               `json:"memory_mb"`
	DiskMB             int               `json:"disk_mb"`
	PIDs               int               `json:"pids"`
	Network            string            `json:"network"`
	Allow              []string          `json:"egress_allow,omitempty"`
	Deny               []string          `json:"egress_deny,omitempty"`
	EgressKbps         int               `json:"egress_kbps,omitempty"`
	MaxConns           int               `json:"max_connections,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
	Labels             map[string]string `json:"labels,omitempty"`
	IdleTimeoutSeconds int               `json:"idle_timeout_seconds,omitempty"`
	ExpiresAt          time.Time         `json:"expires_at"`
	CreatedAt          time.Time         `json:"created_at"`
	Node               string            `json:"node"`
}

func sandboxJSON(sb store.Sandbox) sandboxResponse {
	return sandboxResponse{
		ID: sb.ID, Status: sb.Status, Backend: sb.Backend, Template: sb.Template, Image: sb.Image,
		CPU: sb.CPU, MemoryMB: sb.MemoryMB, DiskMB: sb.DiskMB, PIDs: sb.PIDs, Network: sb.Network,
		Allow: sb.Allow, Deny: sb.Deny, EgressKbps: sb.EgressKbps, MaxConns: sb.MaxConns,
		Env: sb.Env, Labels: sb.Labels, IdleTimeoutSeconds: sb.IdleTimeoutSeconds,
		ExpiresAt: sb.ExpiresAt, CreatedAt: sb.CreatedAt, Node: "local",
	}
}

func templateJSON(t store.Template) map[string]any {
	return map[string]any{
		"name": t.Name, "image": t.Image, "description": t.Description,
		"cpu": t.CPU, "memory_mb": t.MemoryMB, "disk_mb": t.DiskMB,
		"env": t.Env, "builtin": t.Builtin, "created_at": t.CreatedAt,
	}
}

func execJSON(res driver.ExecResult) map[string]any {
	return map[string]any{
		"exit_code":   res.ExitCode,
		"stdout":      string(res.Stdout),
		"stderr":      string(res.Stderr),
		"duration_ms": res.Duration.Milliseconds(),
		"truncated":   res.Truncated,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func writeManager(w http.ResponseWriter, err error) {
	var op *core.OpError
	if errors.As(err, &op) {
		writeErr(w, op.Status, op.Code, op.Msg)
		return
	}
	writeErr(w, http.StatusInternalServerError, "internal", "internal error")
}

func writeDecodeErr(w http.ResponseWriter, err error) {
	writeErr(w, http.StatusBadRequest, "invalid", "invalid JSON: "+err.Error())
}

func decodeJSON(r *http.Request, max int64, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, max))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

func readBody(w http.ResponseWriter, r *http.Request, max int64) ([]byte, error) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, max+1))
	if err != nil {
		return nil, errors.New("body exceeds the size limit")
	}
	if int64(len(b)) > max {
		return nil, errors.New("body exceeds the size limit")
	}
	return b, nil
}

func parseMode(s string) (uint32, error) {
	if s == "" {
		return 0o644, nil
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil || n > 0o777 {
		return 0, errors.New("mode must be an octal permission like 644")
	}
	return uint32(n), nil
}

func fileBase(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
