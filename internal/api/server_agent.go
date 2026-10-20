package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/TorstenDittmann/sbx/internal/core"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/ws"
)

func (s *Server) statFile(w http.ResponseWriter, r *http.Request) {
	entry, err := s.mgr.StatFile(r.Context(), actor(r), r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusOK, fileJSON(entry))
}

func (s *Server) mkdir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
	}
	if err := decodeJSON(r, 1<<20, &body); err != nil {
		writeDecodeErr(w, err)
		return
	}
	mode, err := parseMode(body.Mode)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if err := s.mgr.Mkdir(r.Context(), actor(r), r.PathValue("id"), body.Path, mode); err != nil {
		writeManager(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) watchFiles(w http.ResponseWriter, r *http.Request) {
	recursive := r.URL.Query().Get("recursive") == "1" || r.URL.Query().Get("recursive") == "true"
	flusher, _ := w.(http.Flusher)
	headerSent := false
	send := func(event string, payload any) {
		if !headerSent {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			headerSent = true
			if flusher != nil {
				flusher.Flush()
			}
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
	send("ready", map[string]string{"path": r.URL.Query().Get("path")})
	err := s.mgr.Watch(r.Context(), actor(r), r.PathValue("id"), r.URL.Query().Get("path"), recursive, func(ev driver.FileEvent) error {
		send("change", map[string]string{"type": ev.Type, "path": ev.Path})
		return nil
	})
	if err != nil && !headerSent {
		writeManager(w, err)
		return
	}
	if err != nil && !errors.Is(err, r.Context().Err()) {
		var op *core.OpError
		code, msg := "driver", err.Error()
		if errors.As(err, &op) {
			code, msg = op.Code, op.Msg
		}
		send("error", map[string]string{"code": code, "message": msg})
	}
}

func (s *Server) createProcess(w http.ResponseWriter, r *http.Request) {
	in, err := decodeExec(r)
	if err != nil {
		writeDecodeErr(w, err)
		return
	}
	proc, err := s.mgr.StartProcess(r.Context(), actor(r), r.PathValue("id"), in, false, 0, 0)
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, processJSON(proc))
}

func (s *Server) listProcesses(w http.ResponseWriter, r *http.Request) {
	items, err := s.mgr.ListProcesses(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		writeManager(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, processJSON(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"processes": out})
}

func (s *Server) getProcess(w http.ResponseWriter, r *http.Request) {
	proc, err := s.mgr.GetProcess(r.Context(), actor(r), r.PathValue("id"), r.PathValue("procId"))
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusOK, processJSON(proc))
}

func (s *Server) deleteProcess(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.SignalProcess(r.Context(), actor(r), r.PathValue("id"), r.PathValue("procId"), "KILL"); err != nil {
		writeManager(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) signalProcess(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Signal string `json:"signal"`
	}
	if err := decodeJSON(r, 1<<20, &body); err != nil {
		writeDecodeErr(w, err)
		return
	}
	if err := s.mgr.SignalProcess(r.Context(), actor(r), r.PathValue("id"), r.PathValue("procId"), body.Signal); err != nil {
		writeManager(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) processStdin(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(w, r, int64(s.mgr.Config().Limits.MaxUploadBytes))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if err := s.mgr.WriteProcessStdin(r.Context(), actor(r), r.PathValue("id"), r.PathValue("procId"), body); err != nil {
		writeManager(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) streamProcess(w http.ResponseWriter, r *http.Request) {
	s.streamChunks(w, r, func(emit func(driver.ExecChunk) error) (driver.ExecResult, error) {
		return s.mgr.StreamProcess(r.Context(), actor(r), r.PathValue("id"), r.PathValue("procId"), emit)
	})
}

func (s *Server) createTerminal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Cmd  []string          `json:"cmd"`
		Cwd  string            `json:"cwd"`
		Env  map[string]string `json:"env"`
		Cols int               `json:"cols"`
		Rows int               `json:"rows"`
	}
	if err := decodeJSON(r, 1<<20, &body); err != nil {
		writeDecodeErr(w, err)
		return
	}
	proc, err := s.mgr.StartProcess(r.Context(), actor(r), r.PathValue("id"), core.ExecInput{
		Cmd: body.Cmd, Cwd: body.Cwd, Env: body.Env, Background: true,
	}, true, body.Cols, body.Rows)
	if err != nil {
		writeManager(w, err)
		return
	}
	cols, rows := body.Cols, body.Rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": proc.ID, "pid": proc.PID, "cols": cols, "rows": rows,
	})
}

func (s *Server) deleteTerminal(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.SignalProcess(r.Context(), actor(r), r.PathValue("id"), r.PathValue("termId"), "KILL"); err != nil {
		writeManager(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resizeTerminal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	if err := decodeJSON(r, 1<<20, &body); err != nil {
		writeDecodeErr(w, err)
		return
	}
	if err := s.mgr.ResizeProcess(r.Context(), actor(r), r.PathValue("id"), r.PathValue("termId"), body.Cols, body.Rows); err != nil {
		writeManager(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) terminalSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := ws.Accept(w, r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err := s.mgr.StreamProcess(ctx, actor(r), r.PathValue("id"), r.PathValue("termId"), func(chunk driver.ExecChunk) error {
			return conn.Write(ws.OpBinary, chunk.Data)
		})
		if err == nil {
			b, _ := json.Marshal(map[string]any{"type": "exit", "exit_code": res.ExitCode, "truncated": res.Truncated})
			_ = conn.Write(ws.OpText, b)
		}
	}()
	for {
		op, data, err := conn.Read()
		if err != nil {
			cancel()
			<-done
			return
		}
		switch op {
		case ws.OpBinary:
			if err := s.mgr.WriteProcessStdin(ctx, actor(r), r.PathValue("id"), r.PathValue("termId"), data); err != nil {
				cancel()
				<-done
				return
			}
		case ws.OpText:
			var msg struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
				_ = s.mgr.ResizeProcess(ctx, actor(r), r.PathValue("id"), r.PathValue("termId"), msg.Cols, msg.Rows)
			}
		case ws.OpClose:
			cancel()
			<-done
			return
		}
	}
}

func (s *Server) createPreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Port   int  `json:"port"`
		Public bool `json:"public"`
	}
	if err := decodeJSON(r, 1<<20, &body); err != nil {
		writeDecodeErr(w, err)
		return
	}
	pv, err := s.mgr.CreatePreview(r.Context(), actor(r), r.PathValue("id"), body.Port, body.Public)
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.previewJSON(r, pv, true))
}

func (s *Server) listPreviews(w http.ResponseWriter, r *http.Request) {
	items, err := s.mgr.ListPreviews(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		writeManager(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, s.previewJSON(r, item, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"previews": out})
}

func (s *Server) deletePreview(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.DeletePreview(r.Context(), actor(r), r.PathValue("id"), r.PathValue("previewId")); err != nil {
		writeManager(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) proxyPreview(w http.ResponseWriter, r *http.Request) {
	pv, ok := s.mgr.PreviewByID(r.PathValue("id"), r.PathValue("previewId"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "preview not found")
		return
	}
	prefix := "/v1/sandboxes/" + r.PathValue("id") + "/previews/" + pv.ID
	s.servePreview(w, r, pv, prefix, true, false)
}

func (s *Server) spliceUpgrade(w http.ResponseWriter, upstream io.ReadWriteCloser, resp *http.Response) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "internal", "upgrade is not supported")
		return
	}
	client, bufrw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	fmt.Fprintf(bufrw, "HTTP/1.1 %s\r\n", resp.Status)
	_ = resp.Header.Write(bufrw)
	_, _ = bufrw.WriteString("\r\n")
	_ = bufrw.Flush()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstream, client)
		_ = closeWrite(upstream)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, resp.Body)
		_ = closeWrite(client)
	}()
	wg.Wait()
}

func (s *Server) streamChunks(w http.ResponseWriter, r *http.Request, run func(func(driver.ExecChunk) error) (driver.ExecResult, error)) {
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
	res, err := run(func(chunk driver.ExecChunk) error {
		send(string(chunk.Kind), map[string]string{"data": string(chunk.Data)})
		return nil
	})
	if err != nil && !headerSent {
		writeManager(w, err)
		return
	}
	if err != nil && errors.Is(err, r.Context().Err()) {
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

func decodeExec(r *http.Request) (core.ExecInput, error) {
	var body execRequest
	if err := decodeJSON(r, 1<<20, &body); err != nil {
		return core.ExecInput{}, err
	}
	in := core.ExecInput{Cmd: body.Cmd, Cwd: body.Cwd, Env: body.Env}
	if body.TimeoutSeconds != nil {
		in.TimeoutSeconds = *body.TimeoutSeconds
	}
	raw, err := decodeStdin(body.Stdin, body.StdinBase64)
	if err != nil {
		return core.ExecInput{}, err
	}
	in.Stdin = raw
	return in, nil
}

func decodeStdin(text, encoded string) ([]byte, error) {
	if text != "" && encoded != "" {
		return nil, errors.New("send stdin or stdin_base64, not both")
	}
	if encoded != "" {
		b, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, errors.New("stdin_base64 is not valid base64")
		}
		return b, nil
	}
	if text != "" {
		return []byte(text), nil
	}
	return nil, nil
}

func processJSON(p driver.Process) map[string]any {
	return map[string]any{
		"id": p.ID, "pid": p.PID, "cmd": p.Cmd, "cwd": p.Cwd, "tty": p.TTY,
		"status": p.Status, "exit_code": p.ExitCode, "started_at": p.StartedAt,
	}
}

func fileJSON(e driver.FileEntry) map[string]any {
	return map[string]any{
		"name": e.Name, "path": e.Path, "type": e.Type, "size": e.Size,
		"mode": fmt.Sprintf("%o", e.Mode), "mtime": e.ModTime,
	}
}

func (s *Server) previewJSON(r *http.Request, pv core.Preview, withToken bool) map[string]any {
	base := s.externalBase(r)
	apiURL := base + "/v1/sandboxes/" + pv.SandboxID + "/previews/" + pv.ID + "/"
	pathURL := base + "/p/" + pv.SandboxID + "/" + strconv.Itoa(pv.Port) + "/"
	publicURL := pathURL
	body := map[string]any{
		"id": pv.ID, "sandbox_id": pv.SandboxID, "port": pv.Port,
		"public": pv.Public, "url": apiURL, "public_url": publicURL,
		"created_at": pv.CreatedAt,
	}
	if host := s.previewHost(pv); host != "" {
		body["host"] = host
		scheme := "http"
		if strings.HasPrefix(base, "https://") {
			scheme = "https"
		}
		publicURL = scheme + "://" + host + "/"
		body["public_url"] = publicURL
	}
	if withToken && pv.Token != "" {
		body["token"] = pv.Token
		if !pv.Public {
			body["public_url"] = publicURL + "?token=" + pv.Token
		}
	}
	return body
}

func (s *Server) externalBase(r *http.Request) string {
	if u := strings.TrimRight(strings.TrimSpace(s.mgr.Config().Network.PublicURL), "/"); u != "" {
		return u
	}
	return publicBase(r)
}

func (s *Server) previewHost(pv core.Preview) string {
	domain := strings.Trim(strings.ToLower(strings.TrimSpace(s.mgr.Config().Network.PreviewDomain)), ".")
	hexID := strings.TrimPrefix(pv.SandboxID, "sbx_")
	if domain == "" || len(hexID) != 16 {
		return ""
	}
	return strconv.Itoa(pv.Port) + "-sbx-" + hexID + "." + domain
}

func publicBase(r *http.Request) string {
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		if r.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	return proto + "://" + r.Host
}

func stripHop(h http.Header) {
	hop := []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailers", "Transfer-Encoding", "Upgrade"}
	for _, name := range hop {
		h.Del(name)
	}
}

func headerHasToken(h http.Header, name, part string) bool {
	for _, value := range strings.Split(h.Get(name), ",") {
		if strings.EqualFold(strings.TrimSpace(value), part) {
			return true
		}
	}
	return false
}

func closeWrite(c io.Closer) error {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}
