package api

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
		Port int `json:"port"`
	}
	if err := decodeJSON(r, 1<<20, &body); err != nil {
		writeDecodeErr(w, err)
		return
	}
	pv, err := s.mgr.CreatePreview(r.Context(), actor(r), r.PathValue("id"), body.Port)
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, previewJSON(r, pv))
}

func (s *Server) listPreviews(w http.ResponseWriter, r *http.Request) {
	items, err := s.mgr.ListPreviews(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		writeManager(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, previewJSON(r, item))
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
	conn, pv, err := s.mgr.DialPreview(r.Context(), actor(r), r.PathValue("id"), r.PathValue("previewId"))
	if err != nil {
		writeManager(w, err)
		return
	}
	defer conn.Close()
	prefix := "/v1/sandboxes/" + r.PathValue("id") + "/previews/" + pv.ID
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	if rest == "" {
		rest = "/"
	}
	if !strings.HasPrefix(rest, "/") {
		rest = "/" + rest
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.URL.Scheme = "http"
	out.URL.Host = fmt.Sprintf("127.0.0.1:%d", pv.Port)
	out.URL.Path = rest
	q := out.URL.Query()
	q.Del("access_token")
	out.URL.RawQuery = q.Encode()
	out.Host = out.URL.Host
	out.Header.Del("Authorization")
	out.Header.Del("Cookie")
	if !headerHasToken(r.Header, "Connection", "upgrade") {
		stripHop(out.Header)
	}
	if err := out.Write(conn); err != nil {
		writeErr(w, http.StatusBadGateway, "driver", "preview connection failed")
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), out)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "driver", "preview response failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusSwitchingProtocols {
		s.spliceUpgrade(w, conn, resp)
		return
	}
	stripHop(resp.Header)
	for k, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
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

func previewJSON(r *http.Request, pv core.Preview) map[string]any {
	return map[string]any{
		"id": pv.ID, "sandbox_id": pv.SandboxID, "port": pv.Port,
		"url":        publicBase(r) + "/v1/sandboxes/" + pv.SandboxID + "/previews/" + pv.ID + "/",
		"created_at": pv.CreatedAt,
	}
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
