package api_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSandboxStats(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{"image": "alpine:3.20", "memory_mb": 256, "disk_mb": 128})
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s", resp.Status, b)
	}
	var created struct {
		ID string `json:"id"`
	}
	decode(t, resp, &created)
	resp = h.doRaw(t, http.MethodPut, "/v1/sandboxes/"+created.ID+"/files/content?path=/workspace/a.txt", h.key, "application/octet-stream", []byte("12345"))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatal(resp.Status)
	}

	resp = h.do(t, http.MethodGet, "/v1/sandboxes/"+created.ID+"/stats", h.key, nil)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s", resp.Status, b)
	}
	var st struct {
		SandboxID string `json:"sandbox_id"`
		Status    string `json:"status"`
		CPU       struct {
			UsageSeconds float64  `json:"usage_seconds"`
			Percent      *float64 `json:"percent"`
		} `json:"cpu"`
		Memory struct {
			UsedBytes  int64 `json:"used_bytes"`
			LimitBytes int64 `json:"limit_bytes"`
		} `json:"memory"`
		Disk struct {
			UsedBytes  *int64 `json:"used_bytes"`
			LimitBytes int64  `json:"limit_bytes"`
		} `json:"disk"`
		Network struct {
			RxBytes int64 `json:"rx_bytes"`
			TxBytes int64 `json:"tx_bytes"`
		} `json:"network"`
		PIDs struct {
			Current int64 `json:"current"`
		} `json:"pids"`
	}
	decode(t, resp, &st)
	if st.SandboxID != created.ID || st.Status != "running" || st.CPU.UsageSeconds != 1.5 || st.CPU.Percent == nil || *st.CPU.Percent != 12.5 {
		t.Fatalf("%+v", st)
	}
	if st.Memory.LimitBytes != 256<<20 || st.Disk.UsedBytes == nil || *st.Disk.UsedBytes != 5 || st.Disk.LimitBytes != 128<<20 {
		t.Fatalf("%+v", st)
	}
	if st.Network.RxBytes != 1024 || st.Network.TxBytes != 2048 || st.PIDs.Current != 3 {
		t.Fatalf("%+v", st)
	}

	resp = h.do(t, http.MethodGet, "/metrics", "", nil)
	raw, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		`sbx_sandbox_cpu_seconds_total{sandbox="` + created.ID + `",backend="docker"} 1.5`,
		`sbx_sandbox_memory_limit_bytes{sandbox="` + created.ID + `",backend="docker"} 2.68435456e+08`,
		`sbx_sandbox_network_receive_bytes_total{sandbox="` + created.ID + `",backend="docker"} 1024`,
		"# TYPE sbx_sandbox_memory_bytes gauge",
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %q in\n%s", want, raw)
		}
	}

	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/pause", h.key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodGet, "/v1/sandboxes/"+created.ID+"/stats", h.key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("paused stats %s", resp.Status)
	}

	h.docker.StatsErr = errors.New("engine down")
	resp = h.do(t, http.MethodGet, "/v1/sandboxes/"+created.ID+"/stats", h.key, nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("driver error %s", resp.Status)
	}
	resp = h.do(t, http.MethodGet, "/metrics", "", nil)
	raw, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || strings.Contains(string(raw), `sandbox="`+created.ID) {
		t.Fatalf("failed sample should be skipped: %s %s", resp.Status, raw)
	}
	h.docker.StatsErr = nil

	resp = h.do(t, http.MethodDelete, "/v1/sandboxes/"+created.ID, h.key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodGet, "/v1/sandboxes/"+created.ID+"/stats", h.key, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stopped stats %s", resp.Status)
	}
	resp = h.do(t, http.MethodGet, "/v1/sandboxes/sbx_missing/stats", h.key, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing stats %s", resp.Status)
	}
}
