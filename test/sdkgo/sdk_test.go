//go:build sdkgo

// Package sdkgo runs the Go SDK against the real API handler with the fake
// driver. The SDK is its own module, so this needs a workspace:
//
//	go work init . ./sdk/go
//	go test -tags sdkgo ./test/sdkgo/
package sdkgo

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/api"
	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/core"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
	"github.com/TorstenDittmann/sbx/internal/id"
	"github.com/TorstenDittmann/sbx/internal/store"
	sbx "github.com/TorstenDittmann/sbx/sdk/go"
)

func newServer(t *testing.T) (*sbx.Client, *fake.Driver) {
	t.Helper()
	cfg := config.Default()
	st, err := store.Open(filepath.Join(t.TempDir(), "sbx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	plain, hash, prefix := id.Key()
	if err := st.CreateKey(context.Background(), store.APIKey{
		ID: id.RecordID(), Name: "test", Hash: hash, Prefix: prefix,
		Scopes: []string{"admin"}, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	fd := fake.New("docker")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := core.New(cfg, st, map[string]driver.Driver{"docker": fd}, log)
	if err := mgr.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(mgr, log, []byte("openapi: 3.1.0\n")).Handler())
	t.Cleanup(srv.Close)
	client, err := sbx.NewClient(sbx.WithBaseURL(srv.URL), sbx.WithAPIKey(plain))
	if err != nil {
		t.Fatal(err)
	}
	return client, fd
}

func TestSandboxLifecycle(t *testing.T) {
	client, _ := newServer(t)
	ctx := context.Background()

	backends, err := client.ListBackends(ctx)
	if err != nil || len(backends) == 0 || backends[0].Name != "docker" {
		t.Fatalf("backends %+v %v", backends, err)
	}
	templates, err := client.ListTemplates(ctx)
	if err != nil || len(templates) == 0 {
		t.Fatalf("templates %+v %v", templates, err)
	}

	box, err := client.CreateSandbox(ctx, sbx.CreateSandboxOptions{
		Image:   "alpine:3.20",
		TTL:     5 * time.Minute,
		Env:     map[string]string{"FOO": "bar"},
		Labels:  map[string]string{"team": "sdk"},
		Secrets: map[string]string{"TOKEN": "s3cret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(box.ID, "sbx_") || box.Status != sbx.StatusRunning || box.Labels["team"] != "sdk" {
		t.Fatalf("create %+v", box.SandboxInfo)
	}
	if len(box.SecretKeys) != 1 || box.SecretKeys[0] != "TOKEN" {
		t.Fatalf("secret keys %v", box.SecretKeys)
	}
	if box.ExpiresAt.IsZero() || box.CreatedAt.IsZero() {
		t.Fatalf("times %+v", box.SandboxInfo)
	}

	got, err := client.GetSandbox(ctx, box.ID)
	if err != nil || got.ID != box.ID {
		t.Fatalf("get %+v %v", got, err)
	}
	list, err := client.ListSandboxes(ctx, sbx.ListSandboxesOptions{Labels: map[string]string{"team": "sdk"}})
	if err != nil || len(list) != 1 {
		t.Fatalf("list %d %v", len(list), err)
	}
	list, err = client.ListSandboxes(ctx, sbx.ListSandboxesOptions{Labels: map[string]string{"team": "other"}})
	if err != nil || len(list) != 0 {
		t.Fatalf("list other %d %v", len(list), err)
	}

	result, err := box.Exec(ctx, []string{"echo", "hi"}, &sbx.ExecOptions{Timeout: 10 * time.Second})
	if err != nil || result.ExitCode != 0 || result.Stdout != "hi\n" {
		t.Fatalf("exec %+v %v", result, err)
	}
	result, err = box.Exec(ctx, []string{"cat"}, &sbx.ExecOptions{Stdin: []byte{1, 2, 3}})
	if err != nil || result.Stdout != "\x01\x02\x03" {
		t.Fatalf("stdin %+v %v", result, err)
	}

	var out strings.Builder
	exit := -1
	for ev, err := range box.ExecStream(ctx, []string{"echo", "streamed"}, nil) {
		if err != nil {
			t.Fatal(err)
		}
		switch ev.Type {
		case sbx.ExecStdout:
			out.WriteString(ev.Data)
		case sbx.ExecExit:
			exit = ev.ExitCode
		}
	}
	if out.String() != "streamed\n" || exit != 0 {
		t.Fatalf("stream %q %d", out.String(), exit)
	}

	info, err := box.SetTTL(ctx, time.Hour)
	if err != nil || time.Until(info.ExpiresAt) < 50*time.Minute || box.ExpiresAt != info.ExpiresAt {
		t.Fatalf("ttl %+v %v", info, err)
	}
	if _, err := box.SetSecrets(ctx, map[string]string{"A": "1", "B": "2"}); err != nil || len(box.SecretKeys) != 2 {
		t.Fatalf("secrets %v %v", box.SecretKeys, err)
	}
	if _, err := box.Pause(ctx); err != nil || box.Status != sbx.StatusPaused {
		t.Fatalf("pause %s %v", box.Status, err)
	}
	if _, err := box.Resume(ctx); err != nil || box.Status != sbx.StatusRunning {
		t.Fatalf("resume %s %v", box.Status, err)
	}
	stats, err := box.Stats(ctx)
	if err != nil || stats.SandboxID != box.ID {
		t.Fatalf("stats %+v %v", stats, err)
	}

	if _, err := box.Kill(ctx); err != nil || box.Status != sbx.StatusStopped {
		t.Fatalf("kill %s %v", box.Status, err)
	}
	_, err = client.Exec(ctx, box.ID, []string{"echo"}, nil)
	var apiErr *sbx.Error
	if !errors.As(err, &apiErr) || apiErr.Code == "" || apiErr.Message == "" {
		t.Fatalf("exec after kill %v", err)
	}

	_, err = client.GetSandbox(ctx, "sbx_0000000000000000")
	if !sbx.IsNotFound(err) {
		t.Fatalf("missing sandbox %v", err)
	}
}

func TestFilesProcessesTerminalsPreviews(t *testing.T) {
	client, fd := newServer(t)
	ctx := context.Background()
	box, err := client.CreateSandbox(ctx, sbx.CreateSandboxOptions{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}

	if err := box.Files.Mkdir(ctx, "/workspace/sub", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := box.Files.WriteText(ctx, "/workspace/sub/a.txt", "abc", 0o644); err != nil {
		t.Fatal(err)
	}
	text, err := box.Files.ReadText(ctx, "/workspace/sub/a.txt")
	if err != nil || text != "abc" {
		t.Fatalf("read %q %v", text, err)
	}
	entry, err := box.Files.Stat(ctx, "/workspace/sub/a.txt")
	if err != nil || entry.Type != "file" || entry.Size != 3 {
		t.Fatalf("stat %+v %v", entry, err)
	}
	listing, err := box.Files.List(ctx, "/workspace/sub")
	if err != nil || len(listing.Entries) != 1 || listing.Entries[0].Name != "a.txt" {
		t.Fatalf("list %+v %v", listing, err)
	}

	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	_ = tw.WriteHeader(&tar.Header{Name: "from-tar.txt", Mode: 0o644, Size: 2})
	_, _ = tw.Write([]byte("hi"))
	_ = tw.Close()
	if err := box.Files.UploadArchive(ctx, "/workspace", &archive); err != nil {
		t.Fatal(err)
	}
	// The fake driver has no native archives, so the server falls back to
	// tar inside the sandbox, which the fake answers with "ok".
	if !hasExec(fd, "tar", "-xf") {
		t.Fatalf("no tar -xf exec: %+v", fd.Execs())
	}
	rc, err := box.Files.DownloadArchive(ctx, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	packed, _ := io.ReadAll(rc)
	rc.Close()
	if string(packed) != "ok\n" || !hasExec(fd, "tar", "-czf") {
		t.Fatalf("download %q", packed)
	}

	watchCtx, cancelWatch := context.WithTimeout(ctx, 10*time.Second)
	defer cancelWatch()
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = client.WriteFile(ctx, box.ID, "/workspace/watched.txt", []byte("z"), 0)
	}()
	seen := false
	for ev, err := range box.Files.Watch(watchCtx, "/workspace", true) {
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(ev.Path, "watched.txt") {
			seen = true
			break
		}
	}
	if !seen {
		t.Fatal("no watch event")
	}

	if err := box.Files.Remove(ctx, "/workspace/sub/a.txt"); err != nil {
		t.Fatal(err)
	}

	proc, err := box.StartProcess(ctx, []string{"sleep", "30"}, &sbx.ExecOptions{Timeout: sbx.NoTimeout})
	if err != nil || !strings.HasPrefix(proc.ID, "proc_") || proc.Status != "running" {
		t.Fatalf("process %+v %v", proc, err)
	}
	if err := box.WriteProcessStdin(ctx, proc.ID, []byte("ignored")); err != nil {
		t.Fatal(err)
	}
	if string(fd.ProcStdin(box.ID, proc.ID)) != "ignored" {
		t.Fatalf("stdin %q", fd.ProcStdin(box.ID, proc.ID))
	}
	procs, err := box.ListProcesses(ctx)
	if err != nil || len(procs) == 0 {
		t.Fatalf("processes %+v %v", procs, err)
	}
	if p, err := box.GetProcess(ctx, proc.ID); err != nil || p.ID != proc.ID {
		t.Fatalf("get process %+v %v", p, err)
	}
	signalled := false
	exitCode := -1
	for ev, err := range box.StreamProcess(ctx, proc.ID) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Type == sbx.ExecStdout && !signalled {
			signalled = true
			if err := box.SignalProcess(ctx, proc.ID, sbx.SIGKILL); err != nil {
				t.Fatal(err)
			}
		}
		if ev.Type == sbx.ExecExit {
			exitCode = ev.ExitCode
		}
	}
	if exitCode != 137 {
		t.Fatalf("exit %d", exitCode)
	}

	term, err := box.StartTerminal(ctx, []string{"sh"}, &sbx.TerminalOptions{Cols: 100, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	session, err := box.ConnectTerminal(ctx, term.ID)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	n, err := session.Read(buf)
	if err != nil || string(buf[:n]) != "ok\n" {
		t.Fatalf("terminal read %q %v", buf[:n], err)
	}
	if err := session.Resize(120, 50); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Write([]byte("echo\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cols, rows := fd.ProcSize(box.ID, term.ID); cols == 120 && rows == 50 && strings.Contains(string(fd.ProcStdin(box.ID, term.ID)), "echo") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cols, rows := fd.ProcSize(box.ID, term.ID); cols != 120 || rows != 50 {
		t.Fatalf("size %d %d", cols, rows)
	}
	if err := box.ResizeTerminal(ctx, term.ID, 80, 24); err != nil {
		t.Fatal(err)
	}
	session.Close()

	preview, err := box.CreatePreview(ctx, 3000, false)
	if err != nil || !strings.HasPrefix(preview.ID, "prv_") {
		t.Fatalf("preview %+v %v", preview, err)
	}
	previews, err := box.ListPreviews(ctx)
	if err != nil || len(previews) != 1 {
		t.Fatalf("previews %+v %v", previews, err)
	}
	resp, err := box.PreviewFetch(ctx, preview.ID, http.MethodGet, "health", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "preview-ok" {
		t.Fatalf("proxy %s %q", resp.Status, body)
	}
	if err := box.DeletePreview(ctx, preview.ID); err != nil {
		t.Fatal(err)
	}
}

func TestVolumesKeysTemplatesEvents(t *testing.T) {
	client, _ := newServer(t)
	ctx := context.Background()

	vol, err := client.CreateVolume(ctx, "data", "", "")
	if err != nil || !strings.HasPrefix(vol.ID, "vol_") {
		t.Fatalf("volume %+v %v", vol, err)
	}
	if _, err := client.CreateVolume(ctx, "data", "", ""); !sbx.IsConflict(err) {
		t.Fatalf("duplicate volume %v", err)
	}
	again, err := client.EnsureVolume(ctx, "data", "")
	if err != nil || again.ID != vol.ID {
		t.Fatalf("ensure %+v %v", again, err)
	}
	vols, err := client.ListVolumes(ctx)
	if err != nil || len(vols) != 1 {
		t.Fatalf("volumes %+v %v", vols, err)
	}
	if v, err := client.GetVolume(ctx, vol.ID); err != nil || v.Name != "data" {
		t.Fatalf("get volume %+v %v", v, err)
	}

	key, err := client.CreateKey(ctx, "ci", sbx.CreateKeyOptions{
		Scopes:     []string{"sandbox"},
		Network:    &sbx.KeyNetworkPolicy{NetworkPolicy: sbx.NetworkPolicy{Mode: sbx.NetworkAllow, Allow: []string{"example.com"}}, MaxMode: sbx.NetworkAllow},
		ExpiresAt:  time.Now().Add(time.Hour),
		Registries: []sbx.RegistryCredential{{Server: "ghcr.io", Username: "me", Password: "pw"}},
	})
	if err != nil || !strings.HasPrefix(key.Key, "sbx_") {
		t.Fatalf("key %+v %v", key, err)
	}
	if key.Network.MaxMode != sbx.NetworkAllow || len(key.Network.Allow) != 1 || len(key.Registries) != 1 || key.ExpiresAt == nil {
		t.Fatalf("key fields %+v", key.KeyInfo)
	}
	keys, err := client.ListKeys(ctx)
	if err != nil || len(keys) < 2 {
		t.Fatalf("keys %+v %v", keys, err)
	}
	updated, err := client.SetKeyRegistries(ctx, key.ID, nil)
	if err != nil || len(updated.Registries) != 0 {
		t.Fatalf("registries %+v %v", updated, err)
	}
	if got, err := client.GetKey(ctx, key.ID); err != nil || got.Name != "ci" {
		t.Fatalf("get key %+v %v", got, err)
	}

	scoped, err := sbx.NewClient(sbx.WithBaseURL(client.BaseURL()), sbx.WithAPIKey(key.Key))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.ListKeys(ctx); sbx.StatusCode(err) != http.StatusForbidden {
		t.Fatalf("sandbox key listing keys %v", err)
	}
	if err := client.RevokeKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.ListSandboxes(ctx, sbx.ListSandboxesOptions{}); sbx.StatusCode(err) != http.StatusUnauthorized {
		t.Fatalf("revoked key %v", err)
	}

	tpl, err := client.CreateTemplate(ctx, sbx.CreateTemplateOptions{Name: "py", Image: "python:3.12-slim", Description: "Python", Network: sbx.NetworkNone, MemoryMB: 256})
	if err != nil || tpl.Name != "py" || tpl.MemoryMB != 256 {
		t.Fatalf("template %+v %v", tpl, err)
	}

	builder := sbx.FromImage("node:22").WithPackages("git").WithEnv(map[string]string{"A": "1"}).Workdir("/app").Run("npm init -y")
	build, err := client.Build(ctx, builder, "")
	if err != nil || build.ID == "" || len(build.Steps) != 4 {
		t.Fatalf("build %+v %v", build, err)
	}
	again2, err := client.Build(ctx, builder, "")
	if err != nil || !again2.Cached || again2.Image != build.Image {
		t.Fatalf("cached build %+v %v", again2, err)
	}

	zero := int64(0)
	evCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	events := client.Events(evCtx, sbx.EventsOptions{After: &zero})

	box, err := client.CreateSandbox(ctx, sbx.CreateSandboxOptions{
		From:   builder,
		Mounts: []sbx.VolumeMount{{VolumeID: vol.ID, Path: "/data"}},
		Network: &sbx.NetworkPolicy{
			Mode: sbx.NetworkAllow, Allow: []string{"example.com"}, Deny: []string{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if box.Image != build.Image || len(box.Mounts) != 1 || box.Network != "allow" {
		t.Fatalf("from template %+v", box.SandboxInfo)
	}
	snap, err := box.Snapshot(ctx, sbx.SnapshotOptions{Template: "snapped", Description: "after build"})
	if err != nil || !strings.HasPrefix(snap.ID, "snap_") || snap.Template == nil || snap.Template.Name != "snapped" {
		t.Fatalf("snapshot %+v %v", snap, err)
	}
	if err := client.DeleteTemplate(ctx, "snapped"); err != nil {
		t.Fatal(err)
	}
	if _, err := box.Kill(ctx); err != nil {
		t.Fatal(err)
	}

	var types []sbx.EventType
	for ev, err := range events {
		if err != nil {
			t.Fatal(err)
		}
		if ev.SandboxID != box.ID {
			continue
		}
		types = append(types, ev.Type)
		if ev.Type == sbx.EventDeleted {
			if ev.Data.Status != sbx.StatusStopped || len(ev.Data.Raw) == 0 {
				t.Fatalf("deleted data %+v", ev.Data)
			}
			break
		}
	}
	if len(types) < 2 || types[0] != sbx.EventCreated || types[len(types)-1] != sbx.EventDeleted {
		t.Fatalf("events %v", types)
	}

	if err := client.DeleteVolume(ctx, vol.ID); err != nil {
		t.Fatal(err)
	}
}

func hasExec(fd *fake.Driver, cmd ...string) bool {
	for _, call := range fd.Execs() {
		if len(call.Cmd) >= len(cmd) && strings.Join(call.Cmd[:len(cmd)], " ") == strings.Join(cmd, " ") {
			return true
		}
	}
	return false
}
