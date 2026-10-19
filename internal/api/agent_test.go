package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/ws"
)

func TestAgentAPI(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{"image": "alpine:3.20", "env": map[string]string{"SBX_AGENT_TOKEN": "nope"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("reserved env %s", resp.Status)
	}
	resp = h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{"image": "alpine:3.20"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %s", resp.Status)
	}
	var created struct {
		ID string `json:"id"`
	}
	decode(t, resp, &created)

	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/exec", h.key, map[string]any{"cmd": []string{"cat"}, "stdin": "hello"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("exec %s", resp.Status)
	}
	var execd struct {
		Stdout string `json:"stdout"`
	}
	decode(t, resp, &execd)
	if execd.Stdout != "hello" {
		t.Fatalf("stdout %q", execd.Stdout)
	}

	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/files/mkdir", h.key, map[string]any{"path": "/workspace/sub", "mode": "755"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("mkdir %s", resp.Status)
	}
	resp = h.do(t, http.MethodPut, "/v1/sandboxes/"+created.ID+"/files/content?path=/workspace/sub/a.txt", h.key, []byte("abc"))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("write %s", resp.Status)
	}
	resp = h.do(t, http.MethodGet, "/v1/sandboxes/"+created.ID+"/files/stat?path=/workspace/sub/a.txt", h.key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stat %s", resp.Status)
	}
	var st struct {
		Type string `json:"type"`
		Size int    `json:"size"`
	}
	decode(t, resp, &st)
	if st.Type != "file" || st.Size != 3 {
		t.Fatalf("%+v", st)
	}

	watchCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchReq, err := http.NewRequestWithContext(watchCtx, http.MethodGet, h.srv.URL+"/v1/sandboxes/"+created.ID+"/files/watch?path=/workspace&recursive=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	watchReq.Header.Set("Authorization", "Bearer "+h.key)
	watchResp, err := http.DefaultClient.Do(watchReq)
	if err != nil {
		t.Fatal(err)
	}
	defer watchResp.Body.Close()
	if watchResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(watchResp.Body)
		t.Fatalf("watch %s %s", watchResp.Status, b)
	}
	reader := bufio.NewReader(watchResp.Body)
	if _, err := readSSE(reader); err != nil {
		t.Fatal(err)
	}
	resp = h.do(t, http.MethodPut, "/v1/sandboxes/"+created.ID+"/files/content?path=/workspace/watched.txt", h.key, []byte("z"))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatal(resp.Status)
	}
	ev, err := readSSE(reader)
	if err != nil || ev.event != "change" || !strings.Contains(ev.data, "watched.txt") {
		t.Fatalf("%+v %v", ev, err)
	}
	cancel()

	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/processes", h.key, map[string]any{"cmd": []string{"sleep", "30"}, "timeout_seconds": 0})
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("process %s %s", resp.Status, body)
	}
	var proc struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	decode(t, resp, &proc)
	if !strings.HasPrefix(proc.ID, "proc_") || proc.Status != "running" {
		t.Fatalf("%+v", proc)
	}
	resp = h.doRaw(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/processes/"+proc.ID+"/stdin", h.key, "application/octet-stream", []byte("ignored"))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("stdin %s", resp.Status)
	}
	if string(h.docker.ProcStdin(created.ID, proc.ID)) != "ignored" {
		t.Fatalf("stdin bytes %q", h.docker.ProcStdin(created.ID, proc.ID))
	}

	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()
	streamReq, err := http.NewRequestWithContext(streamCtx, http.MethodGet, h.srv.URL+"/v1/sandboxes/"+created.ID+"/processes/"+proc.ID+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	streamReq.Header.Set("Authorization", "Bearer "+h.key)
	streamResp, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		t.Fatal(err)
	}
	defer streamResp.Body.Close()
	sreader := bufio.NewReader(streamResp.Body)
	first, err := readSSE(sreader)
	if err != nil || first.event != "stdout" {
		t.Fatalf("%+v %v", first, err)
	}
	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/processes/"+proc.ID+"/signal", h.key, map[string]any{"signal": "KILL"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("signal %s", resp.Status)
	}
	exitEv, err := readSSE(sreader)
	if err != nil || exitEv.event != "exit" || !strings.Contains(exitEv.data, "137") {
		t.Fatalf("%+v %v", exitEv, err)
	}

	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/terminals", h.key, map[string]any{"cmd": []string{"sh"}, "cols": 100, "rows": 40})
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("terminal %s %s", resp.Status, body)
	}
	var term struct {
		ID string `json:"id"`
	}
	decode(t, resp, &term)
	conn, err := ws.Dial(wsURL(h.srv.URL) + "/v1/sandboxes/" + created.ID + "/terminals/" + term.ID + "/ws?access_token=" + h.key)
	if err != nil {
		t.Fatal(err)
	}
	op, data, err := conn.Read()
	if err != nil || op != ws.OpBinary || string(data) != "ok\n" {
		t.Fatalf("ws %d %q %v", op, data, err)
	}
	if err := conn.Write(ws.OpText, []byte(`{"type":"resize","cols":120,"rows":50}`)); err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ws.OpBinary, []byte("echo\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	cols, rows := h.docker.ProcSize(created.ID, term.ID)
	if cols != 120 || rows != 50 {
		t.Fatalf("size %d %d", cols, rows)
	}
	if !strings.Contains(string(h.docker.ProcStdin(created.ID, term.ID)), "echo") {
		t.Fatalf("pty stdin %q", h.docker.ProcStdin(created.ID, term.ID))
	}
	conn.Close()

	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/previews", h.key, map[string]any{"port": 3000})
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("preview %s %s", resp.Status, body)
	}
	var preview struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	decode(t, resp, &preview)
	if !strings.HasPrefix(preview.ID, "prv_") || !strings.Contains(preview.URL, preview.ID) {
		t.Fatalf("%+v", preview)
	}
	resp = h.do(t, http.MethodGet, "/v1/sandboxes/"+created.ID+"/previews/"+preview.ID+"/health", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("preview auth %s", resp.Status)
	}
	resp = h.do(t, http.MethodGet, "/v1/sandboxes/"+created.ID+"/previews/"+preview.ID+"/health?access_token="+h.key, "", nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "preview-ok" {
		t.Fatalf("proxy %s %q", resp.Status, body)
	}
	resp = h.do(t, http.MethodDelete, "/v1/sandboxes/"+created.ID+"/previews/"+preview.ID, h.key, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete preview %s", resp.Status)
	}

	h.docker.NoGuest = true
	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/processes", h.key, map[string]any{"cmd": []string{"sleep", "1"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("disabled guest %s", resp.Status)
	}
}

type sseEvent struct {
	event string
	data  string
}

func readSSE(r *bufio.Reader) (sseEvent, error) {
	var ev sseEvent
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return ev, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if ev.event != "" {
				return ev, nil
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			ev.event = strings.TrimSpace(line[len("event:"):])
		}
		if strings.HasPrefix(line, "data:") {
			ev.data = strings.TrimSpace(line[len("data:"):])
		}
	}
}

func wsURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}

func TestExecStdinRejectsBoth(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{"image": "alpine:3.20"})
	var created struct {
		ID string `json:"id"`
	}
	decode(t, resp, &created)
	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/exec", h.key, map[string]any{
		"cmd": []string{"cat"}, "stdin": "a", "stdin_base64": "YQ==",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal(resp.Status)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
}
