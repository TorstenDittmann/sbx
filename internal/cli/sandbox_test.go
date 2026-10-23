package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/api"
	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/core"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
	"github.com/TorstenDittmann/sbx/internal/id"
	"github.com/TorstenDittmann/sbx/internal/store"
)

// syncBuffer is written by command goroutines while tests read it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// liveServer starts the API on a fake driver and points the CLI at it.
func liveServer(t *testing.T) (*fake.Driver, *syncBuffer, *syncBuffer) {
	t.Helper()
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
	mgr := core.New(config.Default(), st, map[string]driver.Driver{"docker": fd}, log)
	if err := mgr.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(mgr, log, nil).Handler())
	t.Cleanup(srv.Close)
	t.Setenv("SBX_URL", srv.URL)
	t.Setenv("SBX_API_KEY", plain)
	out, errOut := &syncBuffer{}, &syncBuffer{}
	oldOut, oldErr, oldIn := stdout, stderr, stdin
	stdout, stderr, stdin = out, errOut, strings.NewReader("")
	t.Cleanup(func() { stdout, stderr, stdin = oldOut, oldErr, oldIn })
	return fd, out, errOut
}

func TestSandboxLifecycle(t *testing.T) {
	_, out, _ := liveServer(t)
	if err := Run([]string{"create", "base", "--ttl", "5m", "--label", "team=cli", "--env", "A=1"}); err != nil {
		t.Fatal(err)
	}
	sbID := strings.TrimSpace(out.String())
	if !strings.HasPrefix(sbID, "sbx_") {
		t.Fatalf("id %q", sbID)
	}

	out.buf.Reset()
	if err := Run([]string{"ls", "--label", "team:cli"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, sbID) || !strings.Contains(got, "team=cli") || !strings.Contains(got, "in 4m") {
		t.Fatalf("ls output:\n%s", got)
	}
	out.buf.Reset()
	if err := Run([]string{"ls", "--quiet", "--label", "team:other"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "" {
		t.Fatalf("filtered ls: %q", out.String())
	}

	out.buf.Reset()
	if err := Run([]string{"get", sbID}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil || got["id"] != sbID {
		t.Fatalf("get: %v %s", err, out.String())
	}

	out.buf.Reset()
	if err := Run([]string{"ttl", sbID, "600"}); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"pause", sbID}); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"resume", sbID}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "running") {
		t.Fatalf("resume: %s", out.String())
	}

	out.buf.Reset()
	if err := Run([]string{"rm", sbID}); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"rm", "sbx_missing"}); err == nil {
		t.Fatal("expected missing sandbox")
	}
	if err := Run([]string{"get", "sbx_missing"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("get missing: %v", err)
	}
}

func TestExecStreamsAndExitCode(t *testing.T) {
	fd, out, _ := liveServer(t)
	if err := Run([]string{"create", "base", "--json"}); err != nil {
		t.Fatal(err)
	}
	var sb sandbox
	if err := json.Unmarshal([]byte(out.String()), &sb); err != nil {
		t.Fatal(err)
	}
	out.buf.Reset()
	if err := Run([]string{"exec", "--cwd", "src", sb.ID, "--", "echo", "-n", "hi"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "-n hi\n" {
		t.Fatalf("exec output %q", out.String())
	}

	out.buf.Reset()
	stdin = strings.NewReader("piped")
	if err := Run([]string{"exec", "-i", sb.ID, "cat"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "piped" {
		t.Fatalf("stdin %q", out.String())
	}

	fd.ExecCode = 3
	err := Run([]string{"exec", sb.ID, "false"})
	if code, ok := ExitCode(err); !ok || code != 3 {
		t.Fatalf("exit code %v", err)
	}
	if err := Run([]string{"exec", sb.ID}); err == nil {
		t.Fatal("expected usage")
	}
}

func TestCopyFiles(t *testing.T) {
	_, out, _ := liveServer(t)
	if err := Run([]string{"create", "--template", "base"}); err != nil {
		t.Fatal(err)
	}
	sbID := strings.TrimSpace(out.String())
	dir := t.TempDir()
	src := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"cp", src, sbID + ":app/hello.txt"}); err != nil {
		t.Fatal(err)
	}
	back := filepath.Join(dir, "back.txt")
	if err := Run([]string{"cp", sbID + ":/workspace/app/hello.txt", back}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(back); err != nil || string(b) != "hello" {
		t.Fatalf("%q %v", b, err)
	}
	// An existing local directory receives the file under its own name.
	into := t.TempDir()
	if err := Run([]string{"cp", sbID + ":app/hello.txt", into}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(into, "hello.txt")); err != nil {
		t.Fatal(err)
	}
	// An existing remote directory does too.
	if err := Run([]string{"cp", back, sbID + ":app"}); err != nil {
		t.Fatal(err)
	}
	out.buf.Reset()
	if err := Run([]string{"cp", sbID + ":app/back.txt", "-"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hello" {
		t.Fatalf("stdout %q", out.String())
	}
	stdin = strings.NewReader("from stdin")
	if err := Run([]string{"cp", "-", sbID + ":note.txt"}); err != nil {
		t.Fatal(err)
	}
	out.buf.Reset()
	if err := Run([]string{"cp", sbID + ":note.txt", "-"}); err != nil || out.String() != "from stdin" {
		t.Fatalf("%q %v", out.String(), err)
	}
	if err := Run([]string{"cp", sbID + ":missing.txt", back}); err == nil {
		t.Fatal("expected missing file")
	}
	if err := Run([]string{"cp", src, back}); err == nil {
		t.Fatal("expected usage: both local")
	}
}

func TestShellTerminal(t *testing.T) {
	_, out, _ := liveServer(t)
	if err := Run([]string{"create", "--template", "base"}); err != nil {
		t.Fatal(err)
	}
	sbID := strings.TrimSpace(out.String())
	out.buf.Reset()
	done := make(chan error, 1)
	go func() { done <- Run([]string{"shell", sbID}) }()

	c, err := connFlags(newFlags("t"))()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "ok") {
		if time.Now().After(deadline) {
			t.Fatal("no terminal output")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var procs struct {
		Processes []struct {
			ID string `json:"id"`
		} `json:"processes"`
	}
	if err := c.json(http.MethodGet, sandboxPath(sbID, "processes"), nil, nil, &procs); err != nil || len(procs.Processes) != 1 {
		t.Fatalf("%+v %v", procs, err)
	}
	if err := c.json(http.MethodDelete, sandboxPath(sbID, "terminals", procs.Processes[0].ID), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if code, ok := ExitCode(err); !ok || code != 137 {
			t.Fatalf("shell exit %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shell did not return")
	}
}

func TestPackAndUnpack(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a", "b", "c.txt"), []byte("c"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("b/c.txt", filepath.Join(src, "a", "link")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := packDir(&buf, src, "proj"); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := unpack(&buf, dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "proj", "a", "link")); err != nil || string(b) != "c" {
		t.Fatalf("%q %v", b, err)
	}
	if st, err := os.Stat(filepath.Join(dst, "proj", "a", "b", "c.txt")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", st, err)
	}

	bad := func(hdrs ...*tar.Header) []byte {
		var b bytes.Buffer
		gz := gzip.NewWriter(&b)
		tw := tar.NewWriter(gz)
		for _, h := range hdrs {
			if err := tw.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
		}
		tw.Close()
		gz.Close()
		return b.Bytes()
	}
	cases := map[string][]byte{
		"dotdot":   bad(&tar.Header{Name: "../evil", Typeflag: tar.TypeReg, Mode: 0o644}),
		"abs link": bad(&tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "/etc"}),
		"up link":  bad(&tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "../x"}),
		"through": bad(
			&tar.Header{Name: "a/", Typeflag: tar.TypeDir, Mode: 0o755},
			&tar.Header{Name: "a/b", Typeflag: tar.TypeSymlink, Linkname: ".."},
			&tar.Header{Name: "a/b/c", Typeflag: tar.TypeSymlink, Linkname: ".."},
		),
	}
	for name, archive := range cases {
		if err := unpack(bytes.NewReader(archive), t.TempDir()); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
}

func TestCLIHelpers(t *testing.T) {
	for in, want := range map[string]int{"300": 300, "5m": 300, "1h30m": 5400} {
		if got, err := durationSeconds(in); err != nil || got != want {
			t.Fatalf("%s: %d %v", in, got, err)
		}
	}
	if _, err := durationSeconds("soon"); err == nil {
		t.Fatal("expected bad duration")
	}
	for in, want := range map[string]string{"sbx_1:/tmp/x": "/tmp/x", "sbx_1:src": "/workspace/src", "sbx_1:": "/workspace"} {
		if _, p, ok := splitRemote(in); !ok || p != want {
			t.Fatalf("%s: %s %v", in, p, ok)
		}
	}
	for _, local := range []string{"./a:b", "C:/x", "file.txt", "/abs/x:y"} {
		if _, _, ok := splitRemote(local); ok {
			t.Fatalf("%s should be local", local)
		}
	}
	fs := newFlags("t")
	n := fs.Int("n", 0, "")
	pos, err := parseMixed(fs, []string{"a", "-n", "2", "b", "--", "-c"})
	if err != nil || *n != 2 || strings.Join(pos, " ") != "a b -c" {
		t.Fatalf("%v %d %v", pos, *n, err)
	}
	c := &client{base: "https://sbx.example.com"}
	if got := c.wsURL("/x"); got != "wss://sbx.example.com/x" {
		t.Fatal(got)
	}
	t.Setenv("SBX_API_KEY", "")
	t.Setenv("SBX_DATA_DIR", t.TempDir())
	if _, err := connFlags(newFlags("t"))(); err == nil {
		t.Fatal("expected missing key")
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("SBX_DATA_DIR"), "initial_api_key"), []byte("sbx_file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := connFlags(newFlags("t"))(); err != nil || c.key != "sbx_file" {
		t.Fatalf("%+v %v", c, err)
	}
}
