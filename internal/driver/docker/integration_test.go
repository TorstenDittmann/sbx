package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/id"
)

func TestIntegrationLifecycle(t *testing.T) {
	if os.Getenv("SBX_INTEGRATION") != "1" {
		t.Skip("set SBX_INTEGRATION=1 to run against a Docker daemon")
	}
	d, err := New(Options{Name: "docker", ReadOnlyRootfs: true, UID: 1000, GID: 1000, MaxOutputBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sbID := id.Sandbox()
	h, err := d.Create(ctx, driver.SandboxSpec{
		ID: sbID, Image: "alpine:3.20", CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 64,
		Network: driver.NetNone, Env: map[string]string{"FOO": "bar"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Delete(context.Background(), h.ID) })

	raw, err := exec.Command("docker", "inspect", h.ID).Output()
	if err != nil {
		t.Fatal(err)
	}
	var inspected []struct {
		HostConfig struct {
			CapDrop        []string
			NetworkMode    string
			ReadonlyRootfs bool
			Privileged     bool
			Memory         int64
			NanoCpus       int64
		}
		Config struct {
			User string
		}
	}
	if err := json.Unmarshal(raw, &inspected); err != nil {
		t.Fatal(err)
	}
	hc := inspected[0].HostConfig
	if hc.Privileged || !hc.ReadonlyRootfs || hc.NetworkMode != "none" || len(hc.CapDrop) == 0 || hc.CapDrop[0] != "ALL" {
		t.Fatalf("host config %+v", hc)
	}
	if inspected[0].Config.User != "1000:1000" || hc.Memory != 64*1024*1024 || hc.NanoCpus != 500_000_000 {
		t.Fatalf("user %s mem %d cpu %d", inspected[0].Config.User, hc.Memory, hc.NanoCpus)
	}

	res, err := d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"echo", "hello-sbx"}, TimeoutSeconds: 20})
	if err != nil || res.ExitCode != 0 || string(res.Stdout) != "hello-sbx\n" {
		t.Fatalf("%+v %v", res, err)
	}
	res, err = d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"id"}, TimeoutSeconds: 20})
	if err != nil || !strings.Contains(string(res.Stdout), "uid=1000") {
		t.Fatalf("id %q %v", res.Stdout, err)
	}
	res, err = d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"ls", "/sys/class/net"}, TimeoutSeconds: 20})
	if err != nil || strings.TrimSpace(string(res.Stdout)) != "lo" {
		t.Fatalf("net %q %v", res.Stdout, err)
	}
	res, err = d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"touch", "/etc/sbx-nope"}, TimeoutSeconds: 20})
	if err != nil || res.ExitCode == 0 {
		t.Fatalf("expected rootfs write to fail: %+v %v", res, err)
	}
	if err := d.WriteFile(ctx, h.ID, "/workspace/hello.txt", []byte("from-sbx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := d.ReadFile(ctx, h.ID, "/workspace/hello.txt", 1<<20)
	if err != nil || string(got) != "from-sbx\n" {
		t.Fatalf("read %q %v", got, err)
	}
	entries, err := d.ListFiles(ctx, h.ID, "/workspace")
	if err != nil || len(entries) != 1 || entries[0].Name != "hello.txt" || entries[0].Type != driver.FileFile {
		t.Fatalf("%+v %v", entries, err)
	}
	var streamed bytes.Buffer
	res, err = d.ExecStream(ctx, h.ID, driver.ExecRequest{Cmd: []string{"echo", "streamed"}, TimeoutSeconds: 20}, func(chunk driver.ExecChunk) error {
		if chunk.Kind == driver.StreamStdout {
			streamed.Write(chunk.Data)
		}
		return nil
	})
	if err != nil || res.ExitCode != 0 || streamed.String() != "streamed\n" {
		t.Fatalf("stream %q %+v %v", streamed.String(), res, err)
	}
	res, err = d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"sleep", "5"}, TimeoutSeconds: 1})
	if err != nil || res.ExitCode == 0 {
		t.Fatalf("timeout %+v %v", res, err)
	}
	if err := d.Pause(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	st, err := d.Inspect(ctx, h.ID)
	if err != nil || st.State != driver.StatePaused {
		t.Fatalf("%+v %v", st, err)
	}
	if err := d.Resume(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveFile(ctx, h.ID, "/workspace/hello.txt"); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Inspect(ctx, h.ID); !errors.Is(err, driver.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestIntegrationFullNetworkBlocksPrivate(t *testing.T) {
	if os.Getenv("SBX_INTEGRATION") != "1" {
		t.Skip("set SBX_INTEGRATION=1 to run against a Docker daemon")
	}
	d, err := New(Options{Name: "docker", ReadOnlyRootfs: true, UID: 1000, GID: 1000, MaxOutputBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	h, err := d.Create(ctx, driver.SandboxSpec{
		ID: id.Sandbox(), Image: "alpine:3.20", CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 64,
		Network: driver.NetFull,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Delete(context.Background(), h.ID) })

	raw, err := exec.Command("docker", "inspect", h.ID).Output()
	if err != nil {
		t.Fatal(err)
	}
	var inspected []struct {
		HostConfig struct {
			NetworkMode string
		}
	}
	if err := json.Unmarshal(raw, &inspected); err != nil {
		t.Fatal(err)
	}
	if inspected[0].HostConfig.NetworkMode != sandboxNetwork {
		t.Fatalf("network %s", inspected[0].HostConfig.NetworkMode)
	}

	public, err := d.Exec(ctx, h.ID, driver.ExecRequest{
		Cmd:            []string{"wget", "-T", "8", "-q", "-O", "-", "http://1.1.1.1/"},
		TimeoutSeconds: 20,
	})
	if err != nil || public.ExitCode != 0 || len(public.Stdout) == 0 {
		t.Fatalf("public egress %+v %v", public, err)
	}
	for _, host := range []string{
		"169.254.169.254",
		"10.255.255.1",
		"192.168.255.1",
		"172.16.0.1",
		"100.100.100.200",
	} {
		res, err := d.Exec(ctx, h.ID, driver.ExecRequest{
			Cmd:            []string{"wget", "-T", "3", "-q", "-O", "/dev/null", "http://" + host + "/"},
			TimeoutSeconds: 15,
		})
		if err != nil {
			t.Fatal(host, err)
		}
		if res.ExitCode == 0 {
			t.Fatalf("%s was reachable on network=full", host)
		}
	}
}

func TestIntegrationAgent(t *testing.T) {
	if os.Getenv("SBX_INTEGRATION") != "1" {
		t.Skip("set SBX_INTEGRATION=1 to run against a Docker daemon")
	}
	bin := buildAgent(t)
	d, err := New(Options{Name: "docker", ReadOnlyRootfs: true, UID: 1000, GID: 1000, MaxOutputBytes: 1 << 20, AgentBin: bin})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	h, err := d.Create(ctx, driver.SandboxSpec{
		ID: id.Sandbox(), Image: "alpine:3.20", CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 64,
		Network: driver.NetNone, Env: map[string]string{"FOO": "bar"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Delete(context.Background(), h.ID) })

	res, err := d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"sh", "-c", "echo $FOO"}, TimeoutSeconds: 20})
	if err != nil || res.ExitCode != 0 || string(res.Stdout) != "bar\n" {
		t.Fatalf("env %+v %v", res, err)
	}
	if err := d.Mkdir(ctx, h.ID, "/workspace/sub", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteFile(ctx, h.ID, "/workspace/sub/note.txt", []byte("agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := d.StatFile(ctx, h.ID, "/workspace/sub/note.txt")
	if err != nil || st.Type != driver.FileFile || st.Size != 6 {
		t.Fatalf("stat %+v %v", st, err)
	}
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	seen := make(chan driver.FileEvent, 1)
	go func() {
		_ = d.Watch(watchCtx, h.ID, "/workspace/sub", false, func(ev driver.FileEvent) error {
			select {
			case seen <- ev:
			default:
			}
			return nil
		})
	}()
	time.Sleep(300 * time.Millisecond)
	if err := d.WriteFile(ctx, h.ID, "/workspace/sub/later.txt", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-seen:
		if (ev.Type != "create" && ev.Type != "write") || !strings.HasSuffix(ev.Path, "later.txt") || strings.Contains(ev.Path, ".sbx-tmp") {
			t.Fatalf("%+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch timed out")
	}
	watchCancel()

	proc, err := d.StartProcess(ctx, h.ID, driver.ExecRequest{Cmd: []string{"sleep", "30"}, TimeoutSeconds: 0}, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := d.ListProcesses(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range listed {
		if item.ID == proc.ID && item.Status == "running" {
			found = true
		}
	}
	if !found {
		t.Fatalf("%+v", listed)
	}
	if err := d.SignalProcess(ctx, h.ID, proc.ID, "KILL"); err != nil {
		t.Fatal(err)
	}

	term, err := d.StartProcess(ctx, h.ID, driver.ExecRequest{Cmd: []string{"sh"}, TimeoutSeconds: 0}, true, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	termCtx, termCancel := context.WithCancel(ctx)
	defer termCancel()
	got := make(chan string, 1)
	var buf bytes.Buffer
	var mu sync.Mutex
	go func() {
		_, _ = d.StreamProcess(termCtx, h.ID, term.ID, func(chunk driver.ExecChunk) error {
			mu.Lock()
			buf.Write(chunk.Data)
			text := buf.String()
			mu.Unlock()
			if strings.Contains(text, "pty-ok") {
				select {
				case got <- text:
				default:
				}
			}
			return nil
		})
	}()
	time.Sleep(200 * time.Millisecond)
	if err := d.ResizeProcess(ctx, h.ID, term.ID, 100, 30); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteProcessStdin(ctx, h.ID, term.ID, []byte("echo pty-ok\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(15 * time.Second):
		mu.Lock()
		text := buf.String()
		mu.Unlock()
		t.Fatalf("pty output %q", text)
	}
	_ = d.SignalProcess(ctx, h.ID, term.ID, "KILL")
	termCancel()

	// Alpine's busybox has nc and not httpd. One listener answers a single GET.
	_, err = d.StartProcess(ctx, h.ID, driver.ExecRequest{
		Cmd:            []string{"sh", "-c", `printf 'HTTP/1.0 200 OK\r\nContent-Length: 13\r\nConnection: close\r\n\r\nhello-preview' | nc -l -p 18080`},
		TimeoutSeconds: 0,
	}, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var conn io.ReadWriteCloser
	for {
		conn, err = d.DialPort(ctx, h.ID, 18080)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "GET /index.html HTTP/1.0\r\nHost: localhost\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 2048)
	n, err := conn.Read(body)
	if err != nil && n == 0 {
		t.Fatal(err)
	}
	if !bytes.Contains(body[:n], []byte("hello-preview")) {
		t.Fatalf("preview %q", body[:n])
	}
}

func TestIntegrationKillDuringExec(t *testing.T) {
	if os.Getenv("SBX_INTEGRATION") != "1" {
		t.Skip("set SBX_INTEGRATION=1 to run against a Docker daemon")
	}
	d, err := New(Options{Name: "docker", ReadOnlyRootfs: true, UID: 1000, GID: 1000, MaxOutputBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	h, err := d.Create(ctx, driver.SandboxSpec{
		ID: id.Sandbox(), Image: "alpine:3.20", CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Delete(context.Background(), h.ID) })
	done := make(chan error, 1)
	go func() {
		res, err := d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"sleep", "30"}, TimeoutSeconds: 40})
		if err != nil {
			done <- err
			return
		}
		if res.ExitCode == 0 {
			done <- errors.New("sleep exited 0 after the container was killed")
			return
		}
		done <- nil
	}()
	time.Sleep(300 * time.Millisecond)
	if out, err := exec.Command("docker", "kill", h.ID).CombinedOutput(); err != nil {
		t.Fatalf("kill %v %s", err, out)
	}
	select {
	case err := <-done:
		if err != nil && err.Error() == "sleep exited 0 after the container was killed" {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("exec did not return after the container was killed")
	}
}

func buildAgent(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("SBX_AGENT_BIN"); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
	out := filepath.Join(t.TempDir(), "sbx-agent")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/sbx-agent")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build agent: %v %s", err, raw)
	}
	return out
}

func TestIntegrationGVisorUnavailable(t *testing.T) {
	if os.Getenv("SBX_INTEGRATION") != "1" {
		t.Skip("set SBX_INTEGRATION=1 to run against a Docker daemon")
	}
	d, err := New(Options{Name: "gvisor", Runtime: "runsc", ReadOnlyRootfs: true, UID: 1000, GID: 1000})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = d.Create(ctx, driver.SandboxSpec{ID: id.Sandbox(), Image: "alpine:3.20", CPU: 0.25, MemoryMB: 64, DiskMB: 32, PIDs: 32})
	if !errors.Is(err, driver.ErrRuntimeUnavailable) {
		info, infoErr := d.client.Info(ctx)
		if infoErr == nil {
			if _, ok := info.Runtimes["runsc"]; ok {
				t.Skip("runsc is installed; skipping the missing-runtime assertion")
			}
		}
		t.Fatal(err)
	}
}
