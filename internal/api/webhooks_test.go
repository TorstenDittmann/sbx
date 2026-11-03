package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/core"
)

type hookCall struct {
	id, ts, sig string
	body        []byte
}

// hookReceiver records deliveries and answers with the next queued status,
// or 204 when the queue is empty.
type hookReceiver struct {
	mu     sync.Mutex
	calls  []hookCall
	status []int
	got    chan struct{}
	srv    *httptest.Server
}

func newHookReceiver(t *testing.T) *hookReceiver {
	r := &hookReceiver{got: make(chan struct{}, 64)}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.calls = append(r.calls, hookCall{
			id: req.Header.Get("webhook-id"), ts: req.Header.Get("webhook-timestamp"),
			sig: req.Header.Get("webhook-signature"), body: body,
		})
		code := http.StatusNoContent
		if len(r.status) > 0 {
			code, r.status = r.status[0], r.status[1:]
		}
		r.mu.Unlock()
		w.WriteHeader(code)
		r.got <- struct{}{}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *hookReceiver) wait(t *testing.T, n int) []hookCall {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		r.mu.Lock()
		if len(r.calls) >= n {
			out := append([]hookCall{}, r.calls...)
			r.mu.Unlock()
			return out
		}
		r.mu.Unlock()
		select {
		case <-r.got:
		case <-deadline:
			t.Fatalf("waited for %d webhook calls", n)
		}
	}
}

func webhookHarness(t *testing.T) *harness {
	cfg := config.Default()
	cfg.Webhooks.AllowPrivate = true
	h := newConfiguredHarness(t, cfg)
	h.mgr.SetWebhookBackoff([]time.Duration{10 * time.Millisecond, 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.mgr.RunWebhooks(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return h
}

type webhookBody struct {
	ID         string   `json:"id"`
	URL        string   `json:"url"`
	Events     []string `json:"events"`
	Enabled    bool     `json:"enabled"`
	Secret     string   `json:"secret"`
	LastStatus int      `json:"last_status"`
	LastError  string   `json:"last_error"`
	Failures   int      `json:"failures"`
}

func (h *harness) createWebhook(t *testing.T, key string, body map[string]any) webhookBody {
	t.Helper()
	resp := h.do(t, http.MethodPost, "/v1/webhooks", key, body)
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create webhook %s %s", resp.Status, b)
	}
	var out webhookBody
	decode(t, resp, &out)
	return out
}

func (h *harness) createSandbox(t *testing.T, key string) string {
	t.Helper()
	resp := h.do(t, http.MethodPost, "/v1/sandboxes", key, map[string]any{"image": "alpine:3.20"})
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create sandbox %s %s", resp.Status, b)
	}
	var sb struct {
		ID string `json:"id"`
	}
	decode(t, resp, &sb)
	return sb.ID
}

func TestWebhookDeliversSignedEvents(t *testing.T) {
	h := webhookHarness(t)
	recv := newHookReceiver(t)
	wh := h.createWebhook(t, h.key, map[string]any{"url": recv.srv.URL + "/hook", "events": []string{"sandbox.created", "sandbox.deleted"}})
	if wh.Secret == "" || len(wh.Events) != 2 || !wh.Enabled {
		t.Fatalf("%+v", wh)
	}

	sbID := h.createSandbox(t, h.key)
	if resp := h.do(t, http.MethodPost, "/v1/sandboxes/"+sbID+"/pause", h.key, nil); resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	if resp := h.do(t, http.MethodDelete, "/v1/sandboxes/"+sbID, h.key, nil); resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	calls := recv.wait(t, 2)
	want := []string{"sandbox.created", "sandbox.deleted"}
	for i, c := range calls[:2] {
		var ev core.EventPayload
		if err := json.Unmarshal(c.body, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type != want[i] || ev.SandboxID != sbID || c.id != "evt_"+strconv.FormatInt(ev.ID, 10) {
			t.Fatalf("call %d: %+v id %s", i, ev, c.id)
		}
		ts, err := strconv.ParseInt(c.ts, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		sig, err := core.SignWebhook(wh.Secret, c.id, ts, c.body)
		if err != nil || sig != c.sig {
			t.Fatalf("signature %q, want %q (%v)", c.sig, sig, err)
		}
	}

	// The secret is not shown again, and status shows the last success.
	resp := h.do(t, http.MethodGet, "/v1/webhooks/"+wh.ID, h.key, nil)
	var got webhookBody
	decode(t, resp, &got)
	if got.Secret != "" || got.LastStatus != http.StatusNoContent || got.Failures != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestWebhookRetriesThenDisables(t *testing.T) {
	h := webhookHarness(t)
	recv := newHookReceiver(t)
	recv.status = []int{500, 502}
	wh := h.createWebhook(t, h.key, map[string]any{"url": recv.srv.URL})

	sbID := h.createSandbox(t, h.key)
	calls := recv.wait(t, 3)
	for _, c := range calls[1:3] {
		if c.id != calls[0].id {
			t.Fatalf("retry changed the message id: %q vs %q", c.id, calls[0].id)
		}
	}

	// Every attempt fails: the webhook turns itself off and skips the event.
	recv.mu.Lock()
	recv.status = []int{500, 500, 500}
	recv.mu.Unlock()
	if resp := h.do(t, http.MethodDelete, "/v1/sandboxes/"+sbID, h.key, nil); resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	recv.wait(t, 6)
	deadline := time.Now().Add(5 * time.Second)
	var got webhookBody
	for {
		decode(t, h.do(t, http.MethodGet, "/v1/webhooks/"+wh.ID, h.key, nil), &got)
		if !got.Enabled || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got.Enabled || got.Failures != 3 || got.LastStatus != 500 || got.LastError == "" {
		t.Fatalf("%+v", got)
	}

	// Re-enabling resumes after the dropped event.
	resp := h.do(t, http.MethodPatch, "/v1/webhooks/"+wh.ID, h.key, map[string]any{"enabled": true})
	decode(t, resp, &got)
	if !got.Enabled || got.Failures != 0 {
		t.Fatalf("%+v", got)
	}
	h.createSandbox(t, h.key)
	calls = recv.wait(t, 7)
	var ev core.EventPayload
	_ = json.Unmarshal(calls[6].body, &ev)
	if ev.Type != "sandbox.created" {
		t.Fatalf("after re-enable got %s", ev.Type)
	}
}

func TestWebhookOwnershipAndValidation(t *testing.T) {
	h := webhookHarness(t)
	resp := h.do(t, http.MethodPost, "/v1/keys", h.key, map[string]any{"name": "ci", "scopes": []string{"sandbox"}})
	var key struct {
		Key string `json:"key"`
	}
	decode(t, resp, &key)

	adminRecv := newHookReceiver(t)
	userRecv := newHookReceiver(t)
	h.createWebhook(t, h.key, map[string]any{"url": adminRecv.srv.URL})
	userHook := h.createWebhook(t, key.Key, map[string]any{"url": userRecv.srv.URL})

	// The admin's sandbox reaches only the admin's webhook.
	h.createSandbox(t, h.key)
	userSB := h.createSandbox(t, key.Key)
	userCalls := userRecv.wait(t, 1)
	adminRecv.wait(t, 2)
	var ev core.EventPayload
	_ = json.Unmarshal(userCalls[0].body, &ev)
	if ev.SandboxID != userSB {
		t.Fatalf("sandbox key saw %s", ev.SandboxID)
	}
	time.Sleep(50 * time.Millisecond)
	userRecv.mu.Lock()
	n := len(userRecv.calls)
	userRecv.mu.Unlock()
	if n != 1 {
		t.Fatalf("sandbox key webhook got %d calls", n)
	}

	// A sandbox key sees its own webhooks only; the admin sees every one.
	var list struct {
		Webhooks []webhookBody `json:"webhooks"`
	}
	decode(t, h.do(t, http.MethodGet, "/v1/webhooks", key.Key, nil), &list)
	if len(list.Webhooks) != 1 || list.Webhooks[0].ID != userHook.ID {
		t.Fatalf("%+v", list)
	}
	decode(t, h.do(t, http.MethodGet, "/v1/webhooks", h.key, nil), &list)
	if len(list.Webhooks) != 2 {
		t.Fatalf("%+v", list)
	}
	adminHook := list.Webhooks[0].ID
	if resp := h.do(t, http.MethodDelete, "/v1/webhooks/"+adminHook, key.Key, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete another key's webhook: %s", resp.Status)
	}

	for _, body := range []map[string]any{
		{"url": "ftp://example.com"},
		{"url": "not a url"},
		{"url": "http://169.254.169.254/latest"},
		{"url": "https://example.com", "events": []string{"sandbox.nope"}},
		{"url": "https://example.com", "secret": "mine"},
	} {
		if resp := h.do(t, http.MethodPost, "/v1/webhooks", key.Key, body); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%v: %s", body, resp.Status)
		}
	}

	// Test pings are signed with the current secret; rotation changes it.
	resp = h.do(t, http.MethodPost, "/v1/webhooks/"+userHook.ID+"/rotate-secret", key.Key, nil)
	var rotated webhookBody
	decode(t, resp, &rotated)
	if rotated.Secret == "" || rotated.Secret == userHook.Secret {
		t.Fatalf("rotate: %+v", rotated)
	}
	resp = h.do(t, http.MethodPost, "/v1/webhooks/"+userHook.ID+"/test", key.Key, nil)
	var test struct {
		OK         bool `json:"ok"`
		StatusCode int  `json:"status_code"`
	}
	decode(t, resp, &test)
	if !test.OK || test.StatusCode != http.StatusNoContent {
		t.Fatalf("%+v", test)
	}
	calls := userRecv.wait(t, 2)
	ts, _ := strconv.ParseInt(calls[1].ts, 10, 64)
	if sig, _ := core.SignWebhook(rotated.Secret, calls[1].id, ts, calls[1].body); sig != calls[1].sig {
		t.Fatal("test ping not signed with the rotated secret")
	}
	_ = json.Unmarshal(calls[1].body, &ev)
	if ev.Type != core.EventWebhookTest {
		t.Fatalf("%+v", ev)
	}

	if resp := h.do(t, http.MethodDelete, "/v1/webhooks/"+userHook.ID, key.Key, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatal(resp.Status)
	}
}

func TestWebhookBlocksPrivateByDefault(t *testing.T) {
	h := newConfiguredHarness(t, config.Default())
	recv := newHookReceiver(t)
	// 127.0.0.1 is refused when the URL is checked.
	if resp := h.do(t, http.MethodPost, "/v1/webhooks", h.key, map[string]any{"url": recv.srv.URL}); resp.StatusCode != http.StatusBadRequest {
		t.Fatal(resp.Status)
	}
	// A name that resolves to loopback passes the URL check but not the dial.
	u, _ := url.Parse(recv.srv.URL)
	port := u.Port()
	wh := h.createWebhook(t, h.key, map[string]any{"url": "http://localhost:" + port})
	resp := h.do(t, http.MethodPost, "/v1/webhooks/"+wh.ID+"/test", h.key, nil)
	var test struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	decode(t, resp, &test)
	if test.OK || test.Error == "" {
		t.Fatalf("%+v", test)
	}
	recv.mu.Lock()
	defer recv.mu.Unlock()
	if len(recv.calls) != 0 {
		t.Fatal("blocked webhook reached the receiver")
	}
}
