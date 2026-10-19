package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAgentRoutesAndPreviewUpgrade(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{"image": "alpine:3.20"})
	var created struct {
		ID string `json:"id"`
	}
	decode(t, resp, &created)
	base := "/v1/sandboxes/" + created.ID

	resp = h.do(t, http.MethodPost, base+"/exec", h.key, map[string]any{"cmd": []string{"cat"}, "stdin_base64": "aGVsbG8="})
	var execd struct {
		Stdout string `json:"stdout"`
	}
	decode(t, resp, &execd)
	if resp.StatusCode != http.StatusOK || execd.Stdout != "hello" {
		t.Fatalf("stdin64 %s %q", resp.Status, execd.Stdout)
	}
	resp = h.do(t, http.MethodPost, base+"/exec", h.key, map[string]any{"cmd": []string{"cat"}, "stdin_base64": "%%%"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad b64 %s", resp.Status)
	}
	resp = h.do(t, http.MethodPost, base+"/processes", h.key, []byte("{"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad process %s", resp.Status)
	}

	resp = h.do(t, http.MethodPost, base+"/processes", h.key, map[string]any{"cmd": []string{"sleep", "30"}, "timeout_seconds": 0})
	var proc struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	decode(t, resp, &proc)
	resp = h.do(t, http.MethodGet, base+"/processes", h.key, nil)
	var listed struct {
		Processes []struct {
			ID string `json:"id"`
		} `json:"processes"`
	}
	decode(t, resp, &listed)
	if resp.StatusCode != http.StatusOK || len(listed.Processes) != 1 || listed.Processes[0].ID != proc.ID {
		t.Fatalf("list %+v %s", listed, resp.Status)
	}
	resp = h.do(t, http.MethodGet, base+"/processes/"+proc.ID, h.key, nil)
	var got struct {
		ID string `json:"id"`
	}
	decode(t, resp, &got)
	if resp.StatusCode != http.StatusOK || got.ID != proc.ID {
		t.Fatalf("get %+v %s", got, resp.Status)
	}
	resp = h.do(t, http.MethodGet, base+"/processes/proc_missing", h.key, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing process %s", resp.Status)
	}
	resp = h.do(t, http.MethodPost, base+"/processes/"+proc.ID+"/signal", h.key, []byte("{"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad signal json %s", resp.Status)
	}
	resp = h.do(t, http.MethodPost, base+"/processes/"+proc.ID+"/signal", h.key, map[string]any{"signal": "NOPE"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad signal %s", resp.Status)
	}
	resp = h.do(t, http.MethodDelete, base+"/processes/"+proc.ID, h.key, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete process %s", resp.Status)
	}
	resp = h.do(t, http.MethodPost, base+"/processes/proc_missing/stdin", h.key, []byte("x"))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stdin missing %s", resp.Status)
	}

	resp = h.do(t, http.MethodPost, base+"/terminals", h.key, []byte("{"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad terminal %s", resp.Status)
	}
	resp = h.do(t, http.MethodPost, base+"/terminals", h.key, map[string]any{"cmd": []string{"sh"}, "cols": 900, "rows": 10})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad size %s", resp.Status)
	}
	resp = h.do(t, http.MethodPost, base+"/terminals", h.key, map[string]any{"cmd": []string{"sh"}})
	var term struct {
		ID   string `json:"id"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	decode(t, resp, &term)
	if resp.StatusCode != http.StatusCreated || term.Cols != 80 || term.Rows != 24 {
		t.Fatalf("terminal %+v %s", term, resp.Status)
	}
	resp = h.do(t, http.MethodPost, base+"/terminals/"+term.ID+"/resize", h.key, []byte("{"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad resize %s", resp.Status)
	}
	resp = h.do(t, http.MethodPost, base+"/terminals/"+term.ID+"/resize", h.key, map[string]any{"cols": 90, "rows": 30})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("resize %s", resp.Status)
	}
	cols, rows := h.docker.ProcSize(created.ID, term.ID)
	if cols != 90 || rows != 30 {
		t.Fatalf("size %d %d", cols, rows)
	}
	req, err := http.NewRequest(http.MethodGet, h.srv.URL+base+"/terminals/"+term.ID+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.key)
	plain, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	plain.Body.Close()
	if plain.StatusCode != http.StatusBadRequest {
		t.Fatalf("ws without upgrade %s", plain.Status)
	}
	resp = h.do(t, http.MethodDelete, base+"/terminals/"+term.ID, h.key, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete terminal %s", resp.Status)
	}

	resp = h.do(t, http.MethodPost, base+"/files/mkdir", h.key, []byte("{"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad mkdir %s", resp.Status)
	}
	resp = h.do(t, http.MethodPost, base+"/files/mkdir", h.key, map[string]any{"path": "/workspace/n", "mode": "999"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad mode %s", resp.Status)
	}
	resp = h.do(t, http.MethodGet, base+"/files/stat?path=/workspace/missing", h.key, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stat missing %s", resp.Status)
	}

	resp = h.do(t, http.MethodPost, base+"/previews", h.key, []byte("{"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad preview %s", resp.Status)
	}
	resp = h.do(t, http.MethodPost, base+"/previews", h.key, map[string]any{"port": 0})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("port %s", resp.Status)
	}
	resp = h.do(t, http.MethodPost, base+"/previews", h.key, map[string]any{"port": 8080})
	var preview struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	decode(t, resp, &preview)
	resp = h.do(t, http.MethodGet, base+"/previews", h.key, nil)
	resp.Header.Get("Content-Type")
	var previews struct {
		Previews []struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"previews"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &previews); err != nil || len(previews.Previews) != 1 || previews.Previews[0].ID != preview.ID {
		t.Fatalf("list previews %s %s %v", resp.Status, body, err)
	}
	req, err = http.NewRequest(http.MethodGet, h.srv.URL+base+"/previews", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.key)
	req.Header.Set("X-Forwarded-Proto", "https")
	forwarded, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer forwarded.Body.Close()
	var forwardedBody struct {
		Previews []struct {
			URL string `json:"url"`
		} `json:"previews"`
	}
	decode(t, forwarded, &forwardedBody)
	if len(forwardedBody.Previews) != 1 || !strings.HasPrefix(forwardedBody.Previews[0].URL, "https://") {
		t.Fatalf("forwarded %+v", forwardedBody)
	}

	resp = h.do(t, http.MethodDelete, base+"/previews/prv_0000000000000000", h.key, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing preview %s", resp.Status)
	}

	h.docker.DropDial = true
	resp = h.do(t, http.MethodGet, base+"/previews/"+preview.ID+"/health?access_token="+h.key, "", nil)
	if resp.StatusCode != http.StatusBadGateway {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("dropped preview %s %s", resp.Status, b)
	}
	h.docker.DropDial = false
}
