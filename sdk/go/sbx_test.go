package sbx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClient(WithBaseURL(srv.URL+"/"), WithAPIKey("sbx_test"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewClientDefaults(t *testing.T) {
	t.Setenv("SBX_URL", "")
	t.Setenv("SBX_API_KEY", "")
	if _, err := NewClient(); err == nil {
		t.Fatal("want an error without a key")
	}
	t.Setenv("SBX_API_KEY", "sbx_env")
	c, err := NewClient()
	if err != nil || c.BaseURL() != DefaultBaseURL || c.apiKey != "sbx_env" {
		t.Fatalf("%+v %v", c, err)
	}
	t.Setenv("SBX_URL", "https://sbx.example.com/")
	c, _ = NewClient(WithAPIKey("sbx_opt"))
	if c.BaseURL() != "https://sbx.example.com" || c.apiKey != "sbx_opt" {
		t.Fatalf("%+v", c)
	}
}

func TestSeconds(t *testing.T) {
	cases := map[time.Duration]int64{
		0:                       0,
		-time.Second:            0,
		time.Millisecond:        1,
		999 * time.Millisecond:  1,
		1500 * time.Millisecond: 1,
		5 * time.Minute:         300,
	}
	for in, want := range cases {
		if got := seconds(in); got != want {
			t.Errorf("seconds(%v) = %d, want %d", in, got, want)
		}
	}
}

func TestRequestShapeAndErrors(t *testing.T) {
	var got struct {
		method, path, query, auth, ctype string
		body                             map[string]any
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.query = r.Method, r.URL.EscapedPath(), r.URL.RawQuery
		got.auth, got.ctype = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		got.body = nil
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		switch {
		case r.URL.Path == "/v1/sandboxes" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"sbx_1","status":"running","backend":"docker","image":"alpine","expires_at":"2026-01-01T00:00:00Z","node":"n"}`)
		case r.URL.Path == "/v1/sandboxes":
			fmt.Fprint(w, `{"sandboxes":[]}`)
		case strings.HasSuffix(r.URL.Path, "/missing"):
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":"not_found","message":"sandbox not found"}}`)
		case strings.HasSuffix(r.URL.Path, "/plain"):
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, "upstream down")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	ctx := context.Background()
	box, err := c.CreateSandbox(ctx, CreateSandboxOptions{
		From:        FromImage("node:22").Run("npm i"),
		Steps:       []BuildStep{{Run: "echo extra"}},
		TTL:         90 * time.Second,
		IdleTimeout: 500 * time.Millisecond,
		Network:     &NetworkPolicy{Mode: NetworkAllow, Allow: []string{"a.com"}, Deny: []string{}},
		Mounts:      []VolumeMount{{VolumeID: "vol_1", Path: "/data"}},
		CPU:         0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if box.ID != "sbx_1" || box.Files.id != "sbx_1" || box.ExpiresAt.Year() != 2026 {
		t.Fatalf("%+v", box.SandboxInfo)
	}
	if got.auth != "Bearer sbx_test" || got.ctype != "application/json" {
		t.Fatalf("headers %q %q", got.auth, got.ctype)
	}
	want := map[string]any{
		"image":                "node:22",
		"steps":                []any{map[string]any{"run": "npm i"}, map[string]any{"run": "echo extra"}},
		"ttl_seconds":          90.0,
		"idle_timeout_seconds": 1.0,
		"network":              map[string]any{"mode": "allow", "allow": []any{"a.com"}, "deny": []any{}},
		"mounts":               []any{map[string]any{"volume_id": "vol_1", "path": "/data"}},
		"cpu":                  0.5,
	}
	if !reflect.DeepEqual(got.body, want) {
		t.Fatalf("body\n got %v\nwant %v", got.body, want)
	}

	if _, err := c.ListSandboxes(ctx, ListSandboxesOptions{Status: StatusRunning, Query: "x y", Labels: map[string]string{"b": "2", "a": "1"}}); err != nil {
		t.Fatal(err)
	}
	if got.query != "label=a%3A1&label=b%3A2&q=x+y&status=running" {
		t.Fatalf("query %q", got.query)
	}

	_, err = c.GetSandbox(ctx, "missing")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "not_found" || apiErr.Message != "sandbox not found" || !IsNotFound(err) {
		t.Fatalf("%#v", err)
	}
	_, err = c.GetSandbox(ctx, "plain")
	if !errors.As(err, &apiErr) || apiErr.Status != 502 || apiErr.Code != "driver" || apiErr.Message != "Bad Gateway" {
		t.Fatalf("%#v", err)
	}

	if err := c.WriteFile(ctx, "sbx_1", "/workspace/a b.sh", []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPut || got.path != "/v1/sandboxes/sbx_1/files/content" || got.query != "mode=755&path=%2Fworkspace%2Fa+b.sh" || got.ctype != "application/octet-stream" {
		t.Fatalf("write %+v", got)
	}
	if err := c.SignalProcess(ctx, "sbx_1", "proc/1", SIGTERM); err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1/sandboxes/sbx_1/processes/proc%2F1/signal" || got.body["signal"] != "TERM" {
		t.Fatalf("signal %+v", got)
	}

	if _, err := c.Exec(ctx, "sbx_1", nil, nil); err == nil {
		t.Fatal("empty cmd should fail")
	}
	if _, err := c.CreateSandbox(ctx, CreateSandboxOptions{From: FromTemplate("").Run("")}); err == nil {
		t.Fatal("invalid template should fail")
	}
}

func TestExecBody(t *testing.T) {
	body, err := execBody(Shell("ls | wc -l"), &ExecOptions{Cwd: "/w", Env: map[string]string{"A": "1"}, Timeout: NoTimeout, Stdin: []byte{0, 1}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"cmd": []string{"sh", "-lc", "ls | wc -l"}, "cwd": "/w", "env": map[string]string{"A": "1"},
		"timeout_seconds": 0, "stdin_base64": "AAE=",
	}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("%v", body)
	}
	body, _ = execBody([]string{"true"}, &ExecOptions{Timeout: 2 * time.Minute})
	if body["timeout_seconds"] != int64(120) {
		t.Fatalf("%v", body)
	}
}

func TestFileMode(t *testing.T) {
	if got := fileMode(0o644); got != "644" {
		t.Fatal(got)
	}
	if got := fileMode(0o755 | fs.ModeDir); got != "755" {
		t.Fatal(got)
	}
}

func TestTemplateIsImmutable(t *testing.T) {
	base := FromImage("python:3.12").WithPackages("git")
	a := base.Run("pip install a")
	b := base.WithEnv(map[string]string{"X": "1"}).Workdir("/app")
	if len(base.Steps()) != 1 || len(a.Steps()) != 2 || len(b.Steps()) != 3 {
		t.Fatalf("%v %v %v", base.Steps(), a.Steps(), b.Steps())
	}
	if a.Steps()[1].Run != "pip install a" || b.Steps()[2].Workdir != "/app" {
		t.Fatal("wrong steps")
	}
	vars := map[string]string{"K": "v"}
	c := base.WithEnv(vars)
	vars["K"] = "changed"
	if c.Steps()[1].Env["K"] != "v" {
		t.Fatal("env was not copied")
	}
	bad := base.WithPackages().Run("ok")
	if bad.Err() == nil || len(bad.Steps()) != 2 {
		t.Fatalf("%v %v", bad.Err(), bad.Steps())
	}
	if FromTemplate("").BaseTemplate() != "base" || FromImage("x").Image() != "x" {
		t.Fatal("base")
	}
}

func TestKeyNetworkJSON(t *testing.T) {
	p := KeyNetworkPolicy{NetworkPolicy: NetworkPolicy{Mode: NetworkFull, EgressKbps: 10}, MaxMode: NetworkAllow}
	data, err := json.Marshal(p)
	if err != nil || string(data) != `{"egress_kbps":10,"max_mode":"allow","mode":"full"}` {
		t.Fatalf("%s %v", data, err)
	}
	var back KeyNetworkPolicy
	if err := json.Unmarshal(data, &back); err != nil || !reflect.DeepEqual(back, p) {
		t.Fatalf("%+v %v", back, err)
	}
}

func sse(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	f, _ := w.(http.Flusher)
	for _, c := range chunks {
		io.WriteString(w, c)
		if f != nil {
			f.Flush()
		}
	}
}

func TestReadSSE(t *testing.T) {
	in := ": comment\r\nid: 7\r\n\r\nevent: a\ndata: {\"x\":\ndata: 1}\n\nevent:b\ndata:2\n\nevent: tail\ndata: 3"
	var got []sseEvent
	if err := readSSE(strings.NewReader(in), func(ev sseEvent) bool { got = append(got, ev); return true }); err != nil {
		t.Fatal(err)
	}
	want := []sseEvent{
		{id: "7", hasID: true},
		{event: "a", data: "{\"x\":\n1}"},
		{event: "b", data: "2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestExecStream(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "stream=1" {
			t.Errorf("query %q", r.URL.RawQuery)
		}
		sse(w,
			"event: stdout\ndata: {\"data\":\"a\"}\n\n",
			"event: stderr\ndata: {\"data\":\"b\"}\n\n",
			"event: error\ndata: {\"code\":\"timeout\",\"message\":\"slow\"}\n\n",
			"event: exit\ndata: {\"exit_code\":3,\"duration_ms\":5,\"truncated\":true}\n\n",
		)
	})
	var got []ExecEvent
	for ev, err := range c.ExecStream(context.Background(), "sbx_1", []string{"x"}, nil) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ev)
	}
	want := []ExecEvent{
		{Type: ExecStdout, Data: "a"},
		{Type: ExecStderr, Data: "b"},
		{Type: ExecError, Code: "timeout", Message: "slow"},
		{Type: ExecExit, ExitCode: 3, DurationMs: 5, Truncated: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}

	// Breaking out early must not panic or keep yielding.
	n := 0
	for range c.ExecStream(context.Background(), "sbx_1", []string{"x"}, nil) {
		n++
		break
	}
	if n != 1 {
		t.Fatal(n)
	}
	for _, err := range c.ExecStream(context.Background(), "sbx_1", nil, nil) {
		if err == nil {
			t.Fatal("want error for empty cmd")
		}
	}
}

func TestWatchError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("recursive") != "1" {
			t.Errorf("query %q", r.URL.RawQuery)
		}
		sse(w, "event: change\ndata: {\"type\":\"write\",\"path\":\"/w/a\"}\n\n", "event: error\ndata: {\"code\":\"unsupported\",\"message\":\"no inotify\"}\n\n")
	})
	var events []FileEvent
	var last error
	for ev, err := range c.Watch(context.Background(), "sbx_1", "/w", true) {
		if err != nil {
			last = err
			continue
		}
		events = append(events, ev)
	}
	var apiErr *Error
	if len(events) != 1 || events[0].Path != "/w/a" || !errors.As(last, &apiErr) || apiErr.Code != "unsupported" {
		t.Fatalf("%+v %v", events, last)
	}
}

func TestEventsReconnectAndResume(t *testing.T) {
	var calls atomic.Int32
	var queries []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		switch calls.Add(1) {
		case 1:
			sse(w, "id: 4\n\n", "id: 5\nevent: sandbox.created\ndata: {\"id\":5,\"type\":\"sandbox.created\",\"sandbox_id\":\"sbx_1\",\"time\":\"2026-01-01T00:00:00Z\",\"data\":{\"status\":\"running\",\"extra\":1}}\n\n")
			// The connection drops here.
		case 2:
			sse(w, "id: 5\n\n", "id: 6\nevent: sandbox.lost\ndata: {\"id\":6,\"type\":\"sandbox.lost\",\"sandbox_id\":\"sbx_1\",\"time\":\"2026-01-01T00:00:01Z\",\"data\":{\"reason\":\"exited\",\"exit_code\":137,\"oom_killed\":true}}\n\n")
		default:
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"code":"rate_limited","message":"too many streams"}}`)
		}
	})
	var got []Event
	var final error
	for ev, err := range c.Events(context.Background(), EventsOptions{Sandbox: "sbx_1"}) {
		if err != nil {
			final = err
			break
		}
		got = append(got, ev)
	}
	if len(got) != 2 || got[0].ID != 5 || got[0].Data.Status != StatusRunning || !bytes.Contains(got[0].Data.Raw, []byte(`"extra"`)) {
		t.Fatalf("%+v", got)
	}
	if got[1].Data.ExitCode == nil || *got[1].Data.ExitCode != 137 || !got[1].Data.OOMKilled || got[1].Data.Reason != "exited" {
		t.Fatalf("%+v", got[1].Data)
	}
	if StatusCode(final) != http.StatusTooManyRequests {
		t.Fatalf("final %v", final)
	}
	if queries[0] != "sandbox=sbx_1" || queries[1] != "after=5&sandbox=sbx_1" || queries[2] != "after=6&sandbox=sbx_1" {
		t.Fatalf("%v", queries)
	}
}

func TestEventsStopsOnCancel(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, "id: 0\n\n")
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	zero := int64(0)
	done := make(chan struct{})
	go func() {
		for _, err := range c.Events(ctx, EventsOptions{After: &zero}) {
			if err != nil {
				t.Errorf("unexpected %v", err)
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Events did not stop")
	}
}

func TestEventsNoReconnect(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		sse(w, "id: 1\n\n")
	})
	for range c.Events(context.Background(), EventsOptions{NoReconnect: true}) {
		t.Fatal("no events expected")
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}

func TestPreviewFetch(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sbx_test" || r.Header.Get("X-Test") != "1" {
			t.Errorf("headers %v", r.Header)
		}
		w.WriteHeader(http.StatusTeapot)
		fmt.Fprint(w, r.URL.Path+"?"+r.URL.RawQuery)
	})
	resp, err := c.PreviewFetch(context.Background(), "sbx_1", "prv_1", http.MethodGet, "api?x=1", nil, http.Header{"X-Test": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusTeapot || string(body) != "/v1/sandboxes/sbx_1/previews/prv_1/api?x=1" {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

func TestRoutes(t *testing.T) {
	type call struct{ method, path string }
	var last call
	sandbox := `{"id":"sbx_1","status":"running","backend":"docker","image":"alpine","expires_at":"2026-01-01T00:00:00Z","node":"n"}`
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		last = call{r.Method, r.URL.Path}
		p := r.URL.Path
		switch {
		case r.Method == http.MethodDelete && !strings.HasPrefix(p, "/v1/sandboxes/sbx_1") || r.Method == http.MethodDelete && strings.Count(p, "/") > 3:
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(p, "/volumes"):
			fmt.Fprint(w, `{"volumes":[{"id":"vol_1","name":"v"}],"id":"vol_1","name":"v"}`)
		case strings.HasPrefix(p, "/v1/volumes/"):
			fmt.Fprint(w, `{"id":"vol_1","name":"v"}`)
		case p == "/v1/templates":
			fmt.Fprint(w, `{"templates":[{"name":"base"}],"name":"t"}`)
		case p == "/v1/builds":
			fmt.Fprint(w, `{"id":"b","image":"sbx-template:b","steps":[{"run":"x"}]}`)
		case p == "/v1/keys":
			fmt.Fprint(w, `{"keys":[{"id":"key_1"}],"id":"key_1","key":"sbx_new","network":{"max_mode":"none"}}`)
		case strings.HasPrefix(p, "/v1/keys/"):
			fmt.Fprint(w, `{"id":"key_1","registries":[]}`)
		case p == "/v1/backends":
			fmt.Fprint(w, `{"backends":[{"name":"docker","networks":["none"]}]}`)
		case strings.HasSuffix(p, "/snapshot"):
			fmt.Fprint(w, `{"id":"snap_1","template":null}`)
		case strings.HasSuffix(p, "/stats"):
			fmt.Fprint(w, `{"sandbox_id":"sbx_1","cpu":{"percent":null},"disk":{"used_bytes":5}}`)
		case strings.HasSuffix(p, "/exec"):
			fmt.Fprint(w, `{"exit_code":0,"stdout":"ok"}`)
		case strings.HasSuffix(p, "/files"):
			fmt.Fprint(w, `{"path":"/w","entries":[{"name":"a"}]}`)
		case strings.HasSuffix(p, "/files/content"), strings.HasSuffix(p, "/files/archive"):
			if r.Method == http.MethodGet {
				fmt.Fprint(w, "bytes")
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		case strings.HasSuffix(p, "/files/stat"):
			fmt.Fprint(w, `{"name":"a","size":3}`)
		case strings.HasSuffix(p, "/processes"):
			fmt.Fprint(w, `{"processes":[{"id":"proc_1"}],"id":"proc_1","status":"running"}`)
		case strings.Contains(p, "/processes/") && r.Method == http.MethodGet:
			fmt.Fprint(w, `{"id":"proc_1"}`)
		case strings.HasSuffix(p, "/terminals"):
			fmt.Fprint(w, `{"id":"term_1","cols":80}`)
		case strings.HasSuffix(p, "/previews"):
			fmt.Fprint(w, `{"previews":[{"id":"prv_1"}],"id":"prv_1","port":3000}`)
		case strings.HasPrefix(p, "/v1/sandboxes"):
			fmt.Fprint(w, sandbox)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	ctx := context.Background()
	check := func(name string, err error, method, path string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if last.method != method || last.path != path {
			t.Fatalf("%s: %s %s, want %s %s", name, last.method, last.path, method, path)
		}
	}
	must := func(_ any, err error) error { return err }

	check("CreateVolume", must(c.CreateVolume(ctx, "v", BackendDocker, IfExistsError)), "POST", "/v1/volumes")
	check("EnsureVolume", must(c.EnsureVolume(ctx, "v", "")), "POST", "/v1/volumes")
	check("ListVolumes", must(c.ListVolumes(ctx)), "GET", "/v1/volumes")
	check("GetVolume", must(c.GetVolume(ctx, "vol_1")), "GET", "/v1/volumes/vol_1")
	check("DeleteVolume", c.DeleteVolume(ctx, "vol_1"), "DELETE", "/v1/volumes/vol_1")
	check("CreateTemplate", must(c.CreateTemplate(ctx, CreateTemplateOptions{
		Name: "t", Image: "i", Description: "d", CPU: 1, MemoryMB: 1, DiskMB: 1, Env: map[string]string{"a": "b"},
		Backend: "docker", Network: NetworkNone, Setup: []string{"x"}, SetupTimeout: time.Minute, Warm: 1, Steps: []BuildStep{{Run: "x"}},
	})), "POST", "/v1/templates")
	check("ListTemplates", must(c.ListTemplates(ctx)), "GET", "/v1/templates")
	check("DeleteTemplate", c.DeleteTemplate(ctx, "t"), "DELETE", "/v1/templates/t")
	check("Build", must(c.Build(ctx, FromTemplate("python").Run("x"), "gvisor")), "POST", "/v1/builds")
	if _, err := c.Build(ctx, nil, ""); err == nil {
		t.Fatal("nil template")
	}
	if _, err := c.Build(ctx, FromImage("x").Run(""), ""); err == nil {
		t.Fatal("bad template")
	}
	key, err := c.CreateKey(ctx, "k", CreateKeyOptions{Scopes: []string{"sandbox"}, Network: &KeyNetworkPolicy{MaxMode: NetworkNone}, ExpiresAt: time.Now(), Registries: []RegistryCredential{}})
	check("CreateKey", err, "POST", "/v1/keys")
	if key.Key != "sbx_new" || key.Network.MaxMode != NetworkNone {
		t.Fatalf("%+v", key)
	}
	check("ListKeys", must(c.ListKeys(ctx)), "GET", "/v1/keys")
	check("GetKey", must(c.GetKey(ctx, "key_1")), "GET", "/v1/keys/key_1")
	check("SetKeyRegistries", must(c.SetKeyRegistries(ctx, "key_1", nil)), "PUT", "/v1/keys/key_1/registries")
	check("RevokeKey", c.RevokeKey(ctx, "key_1"), "DELETE", "/v1/keys/key_1")
	check("ListBackends", must(c.ListBackends(ctx)), "GET", "/v1/backends")

	box := c.Sandbox("sbx_1")
	if box.Client() != c {
		t.Fatal("client")
	}
	check("Refresh", must(box.Refresh(ctx)), "GET", "/v1/sandboxes/sbx_1")
	if box.Image != "alpine" {
		t.Fatal("refresh did not apply")
	}
	check("Pause", must(box.Pause(ctx)), "POST", "/v1/sandboxes/sbx_1/pause")
	check("Resume", must(box.Resume(ctx)), "POST", "/v1/sandboxes/sbx_1/resume")
	check("SetTTL", must(box.SetTTL(ctx, time.Minute)), "POST", "/v1/sandboxes/sbx_1/timeout")
	check("SetSecrets", must(box.SetSecrets(ctx, nil)), "PUT", "/v1/sandboxes/sbx_1/secrets")
	check("Snapshot", must(box.Snapshot(ctx, SnapshotOptions{Template: "t", Description: "d"})), "POST", "/v1/sandboxes/sbx_1/snapshot")
	stats, err := box.Stats(ctx)
	check("Stats", err, "GET", "/v1/sandboxes/sbx_1/stats")
	if stats.CPU.Percent != nil || stats.Disk.UsedBytes == nil || *stats.Disk.UsedBytes != 5 {
		t.Fatalf("%+v", stats)
	}
	check("Exec", must(box.Exec(ctx, []string{"x"}, nil)), "POST", "/v1/sandboxes/sbx_1/exec")
	check("List", must(box.Files.List(ctx, "/w")), "GET", "/v1/sandboxes/sbx_1/files")
	data, err := box.Files.Read(ctx, "/w/a")
	check("Read", err, "GET", "/v1/sandboxes/sbx_1/files/content")
	if string(data) != "bytes" {
		t.Fatal(string(data))
	}
	check("ReadText", must(box.Files.ReadText(ctx, "/w/a")), "GET", "/v1/sandboxes/sbx_1/files/content")
	rc, err := box.Files.Open(ctx, "/w/a")
	check("Open", err, "GET", "/v1/sandboxes/sbx_1/files/content")
	rc.Close()
	check("Write", box.Files.Write(ctx, "/w/a", nil, 0), "PUT", "/v1/sandboxes/sbx_1/files/content")
	check("WriteText", box.Files.WriteText(ctx, "/w/a", "x", 0), "PUT", "/v1/sandboxes/sbx_1/files/content")
	check("WriteFrom", box.Files.WriteFrom(ctx, "/w/a", strings.NewReader("x"), 0o600), "PUT", "/v1/sandboxes/sbx_1/files/content")
	check("Remove", box.Files.Remove(ctx, "/w/a"), "DELETE", "/v1/sandboxes/sbx_1/files")
	check("Stat", must(box.Files.Stat(ctx, "/w/a")), "GET", "/v1/sandboxes/sbx_1/files/stat")
	check("Mkdir", box.Files.Mkdir(ctx, "/w/d", 0o700), "POST", "/v1/sandboxes/sbx_1/files/mkdir")
	check("UploadArchive", box.Files.UploadArchive(ctx, "/w", strings.NewReader("tar")), "POST", "/v1/sandboxes/sbx_1/files/archive")
	rc, err = box.Files.DownloadArchive(ctx, "/w")
	check("DownloadArchive", err, "GET", "/v1/sandboxes/sbx_1/files/archive")
	rc.Close()
	check("StartProcess", must(box.StartProcess(ctx, []string{"x"}, nil)), "POST", "/v1/sandboxes/sbx_1/processes")
	if _, err := box.StartProcess(ctx, nil, nil); err == nil {
		t.Fatal("empty cmd")
	}
	check("ListProcesses", must(box.ListProcesses(ctx)), "GET", "/v1/sandboxes/sbx_1/processes")
	check("GetProcess", must(box.GetProcess(ctx, "proc_1")), "GET", "/v1/sandboxes/sbx_1/processes/proc_1")
	check("KillProcess", box.KillProcess(ctx, "proc_1"), "DELETE", "/v1/sandboxes/sbx_1/processes/proc_1")
	check("SignalProcess", box.SignalProcess(ctx, "proc_1", SIGINT), "POST", "/v1/sandboxes/sbx_1/processes/proc_1/signal")
	check("WriteProcessStdin", box.WriteProcessStdin(ctx, "proc_1", nil), "POST", "/v1/sandboxes/sbx_1/processes/proc_1/stdin")
	check("StartTerminal", must(box.StartTerminal(ctx, []string{"sh"}, &TerminalOptions{Cols: 80, Rows: 24, ExecOptions: ExecOptions{Stdin: []byte("x")}})), "POST", "/v1/sandboxes/sbx_1/terminals")
	check("StartTerminal nil", must(box.StartTerminal(ctx, []string{"sh"}, nil)), "POST", "/v1/sandboxes/sbx_1/terminals")
	if _, err := box.StartTerminal(ctx, nil, nil); err == nil {
		t.Fatal("empty cmd")
	}
	check("ResizeTerminal", box.ResizeTerminal(ctx, "term_1", 1, 2), "POST", "/v1/sandboxes/sbx_1/terminals/term_1/resize")
	check("KillTerminal", box.KillTerminal(ctx, "term_1"), "DELETE", "/v1/sandboxes/sbx_1/terminals/term_1")
	check("CreatePreview", must(box.CreatePreview(ctx, 3000, true)), "POST", "/v1/sandboxes/sbx_1/previews")
	check("ListPreviews", must(box.ListPreviews(ctx)), "GET", "/v1/sandboxes/sbx_1/previews")
	check("DeletePreview", box.DeletePreview(ctx, "prv_1"), "DELETE", "/v1/sandboxes/sbx_1/previews/prv_1")
	resp, err := box.PreviewFetch(ctx, "prv_1", http.MethodGet, "/", nil, nil)
	check("PreviewFetch", err, "GET", "/v1/sandboxes/sbx_1/previews/prv_1/")
	resp.Body.Close()
	for range box.ExecStream(ctx, []string{"x"}, nil) {
	}
	for range box.StreamProcess(ctx, "proc_1") {
	}
	for range box.Files.Watch(ctx, "/w", false) {
	}
	for range box.Events(ctx, EventsOptions{NoReconnect: true}) {
	}
	check("Events", nil, "GET", "/v1/events")
	check("Kill", must(box.Kill(ctx)), "DELETE", "/v1/sandboxes/sbx_1")
	list, err := c.ListSandboxes(ctx, ListSandboxesOptions{Backend: "docker", Template: "base"})
	if err != nil || len(list) != 0 {
		t.Fatalf("%v %v", list, err)
	}
}
