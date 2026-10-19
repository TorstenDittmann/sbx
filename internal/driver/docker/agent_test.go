package docker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/agent"
	"github.com/TorstenDittmann/sbx/internal/driver"
)

func TestGuestHookSession(t *testing.T) {
	fd, sock := startFakeDaemon(t)
	_ = fd
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	d := NewWithClient(Options{Name: "docker", UID: 1000, GID: 1000, ReadOnlyRootfs: true}, cli)
	ctx := context.Background()
	h, err := d.Create(ctx, driver.SandboxSpec{
		ID: "sbx_agenthook000001", Image: "alpine:3.20", CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.GuestEnabled() {
		t.Fatal("agent binary is not configured")
	}
	cwd := t.TempDir()
	ag := agent.New("hook-token")
	var once sync.Once
	var client *agent.Client
	var herr error
	d.guestHook = func(ctx context.Context, id string) (*agent.Client, error) {
		if id != h.ID {
			t.Errorf("guest id %s", id)
		}
		once.Do(func() {
			left, right := net.Pipe()
			go ag.ServeConn(right)
			client, herr = agent.Handshake(ctx, left, "hook-token")
		})
		return client, herr
	}
	if !d.GuestEnabled() {
		t.Fatal("hook should enable the guest")
	}
	res, err := d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"echo", "from-agent"}, Cwd: cwd, TimeoutSeconds: 10})
	if err != nil || res.ExitCode != 0 || string(res.Stdout) != "from-agent\n" {
		t.Fatalf("%+v %v", res, err)
	}
	dir := filepath.Join(cwd, "nested")
	if err := d.Mkdir(ctx, h.ID, dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "note.txt")
	if err := d.WriteFile(ctx, h.ID, target, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	var archived bytes.Buffer
	tw := tar.NewWriter(&archived)
	if err := tw.WriteHeader(&tar.Header{Name: "from.tar", Mode: 0o644, Size: 3, ModTime: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("tar")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	arcDir := filepath.Join(cwd, "arc")
	if err := d.ExtractArchive(ctx, h.ID, arcDir, bytes.NewReader(archived.Bytes()), 1<<20); err != nil {
		t.Fatal(err)
	}
	arcBody, err := os.ReadFile(filepath.Join(arcDir, "from.tar"))
	if err != nil || string(arcBody) != "tar" {
		t.Fatalf("%q %v", arcBody, err)
	}
	packed, err := d.PackArchive(ctx, h.ID, arcDir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	foundArc := false
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "from.tar" {
			foundArc = true
		}
	}
	if !foundArc {
		t.Fatal("driver archive round trip dropped from.tar")
	}
	st, err := d.StatFile(ctx, h.ID, target)
	if err != nil || st.Type != driver.FileFile || st.Size != 3 {
		t.Fatalf("%+v %v", st, err)
	}
	body, err := d.ReadFile(ctx, h.ID, target, 100)
	if err != nil || string(body) != "abc" {
		t.Fatalf("%q %v", body, err)
	}
	entries, err := d.ListFiles(ctx, h.ID, dir)
	if err != nil || len(entries) != 1 || entries[0].Name != "note.txt" {
		t.Fatalf("%+v %v", entries, err)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan driver.FileEvent, 4)
	watchErr := make(chan error, 1)
	go func() {
		watchErr <- d.Watch(watchCtx, h.ID, dir, true, func(ev driver.FileEvent) error {
			events <- ev
			return nil
		})
	}()
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "later.txt"), []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		if ev.Type != "create" || !strings.HasSuffix(ev.Path, "later.txt") {
			t.Fatalf("%+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watch timed out")
	}
	cancel()
	select {
	case err := <-watchErr:
		if err != nil && !strings.Contains(err.Error(), "canceled") && err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watch did not return")
	}

	proc, err := d.StartProcess(ctx, h.ID, driver.ExecRequest{Cmd: []string{"sleep", "30"}, Cwd: cwd}, false, 0, 0)
	if err != nil || proc.Status != "running" || proc.PID == 0 {
		t.Fatalf("%+v %v", proc, err)
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
	out, err := d.StreamProcess(ctx, h.ID, proc.ID, nil)
	if err != nil || out.ExitCode == 0 {
		t.Fatalf("%+v %v", out, err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 4)
		_, _ = io.ReadFull(c, buf)
		_, _ = c.Write([]byte("pong"))
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	rw, err := d.DialPort(ctx, h.ID, port)
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	if _, err := rw.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(rw, got); err != nil || string(got) != "pong" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestAgentVolumeAndDial(t *testing.T) {
	fd, sock := startFakeDaemon(t)
	fd.mu.Lock()
	fd.images["alpine:3.20"] = true
	fd.mu.Unlock()
	bin := filepath.Join(t.TempDir(), "sbx-agent")
	if err := os.WriteFile(bin, []byte("not-a-real-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	d := NewWithClient(Options{Name: "docker", UID: 1000, GID: 1000, ReadOnlyRootfs: true, AgentBin: bin}, cli)
	ctx := context.Background()
	h, err := d.Create(ctx, driver.SandboxSpec{
		ID: "sbx_agentdial000001", Image: "alpine:3.20", CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 32,
		Env: map[string]string{"FOO": "bar"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	body := fd.created
	labels := fd.volumes[agentVolume]
	archive := append([]byte(nil), fd.archives["cid-"+agentSeedName]...)
	fd.mu.Unlock()
	if len(body.Entrypoint) != 2 || body.Entrypoint[0] != agentBinary || body.Entrypoint[1] != "init" {
		t.Fatalf("entrypoint %+v", body.Entrypoint)
	}
	if len(body.HostConfig.Binds) != 1 || body.HostConfig.Binds[0] != agentVolume+":/opt/sbx:ro" {
		t.Fatalf("binds %+v", body.HostConfig.Binds)
	}
	token := ""
	for _, item := range body.Env {
		if strings.HasPrefix(item, agentTokenEnv+"=") {
			token = strings.TrimPrefix(item, agentTokenEnv+"=")
		}
		if strings.HasPrefix(item, "FOO=") && item != "FOO=bar" {
			t.Fatalf("env %s", item)
		}
	}
	if token == "" || labels["sbx.agent.sha256"] == "" || labels[labelManaged] != "true" {
		t.Fatalf("token %q labels %+v", token, labels)
	}
	tr := tar.NewReader(bytes.NewReader(archive))
	hdr, err := tr.Next()
	if err != nil || hdr.Name != "sbx-agent" || hdr.Mode&0o111 == 0 {
		t.Fatalf("archive %+v %v", hdr, err)
	}
	// A second create reuses the seeded volume.
	if _, err := d.Create(ctx, driver.SandboxSpec{
		ID: "sbx_agentdial000002", Image: "alpine:3.20", CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 32,
	}); err != nil {
		t.Fatal(err)
	}

	cwd := t.TempDir()
	ag := agent.New(token)
	fd.mu.Lock()
	fd.onAttach = func(stdin io.Reader, stdout io.Writer) {
		left, right := net.Pipe()
		go ag.ServeConn(right)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer left.Close()
			_, _ = io.Copy(left, stdin)
		}()
		buf := make([]byte, 32<<10)
		for {
			n, rerr := left.Read(buf)
			if n > 0 {
				writeFrame(stdout, 1, string(buf[:n]))
			}
			if rerr != nil {
				break
			}
		}
		wg.Wait()
	}
	fd.mu.Unlock()
	d.sessMu.Lock()
	delete(d.tokens, h.ID)
	d.sessMu.Unlock()

	res, err := d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"echo", "dialed"}, Cwd: cwd, TimeoutSeconds: 10})
	if err != nil || res.ExitCode != 0 || string(res.Stdout) != "dialed\n" {
		t.Fatalf("%+v %v", res, err)
	}
	if err := d.Delete(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
}
