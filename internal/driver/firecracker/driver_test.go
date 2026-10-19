package firecracker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/agent"
	"github.com/TorstenDittmann/sbx/internal/driver"
)

func TestMicroVMLifecycle(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin", "firecracker")
	kernel := filepath.Join(dir, "bin", "vmlinux")
	agentBin := filepath.Join(dir, "bin", "sbx-agent")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{bin, kernel, agentBin} {
		if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var (
		mu     sync.Mutex
		token  string
		got    = map[string][]byte{}
		nic    bool
		closed = false
	)
	var listeners []io.Closer
	closeAll := func() {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return
		}
		closed = true
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}
	t.Cleanup(closeAll)
	sleep := exec.Command("sleep", "60")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleep.Process.Kill() })

	opt := Options{
		Bin: bin, Kernel: kernel, AgentBin: agentBin, DataDir: dir, SkipKVM: true,
		Build: func(_ context.Context, spec RootSpec) error {
			var cfg agent.GuestConfig
			if err := json.Unmarshal(spec.Guest, &cfg); err != nil {
				return err
			}
			mu.Lock()
			token = cfg.Token
			mu.Unlock()
			return os.WriteFile(spec.Dest, []byte("disk"), 0o644)
		},
		Spawn: func(spec SpawnSpec) (Process, error) {
			ln, err := net.Listen("unix", spec.APISocket)
			if err != nil {
				return nil, err
			}
			srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				got[r.Method+" "+r.URL.Path] = body
				if strings.Contains(string(body), "eth0") {
					nic = true
				}
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
			})}
			go srv.Serve(ln)
			vs, err := net.Listen("unix", spec.VsockPath())
			if err != nil {
				return nil, err
			}
			mu.Lock()
			listeners = append(listeners, ln, vs)
			tok := token
			mu.Unlock()
			go acceptVsock(vs, tok)
			return &fakeProc{pid: sleep.Process.Pid, stop: func() {
				_ = srv.Close()
				_ = vs.Close()
				_ = sleep.Process.Kill()
			}}, nil
		},
	}
	d, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	if !d.GuestEnabled() || d.Name() != "firecracker" || !d.Capabilities().Preview {
		t.Fatal(d.Capabilities())
	}
	ctx := context.Background()
	info := d.Report(ctx)
	if !info.Available || info.Isolation != "firecracker" {
		t.Fatalf("%+v", info)
	}
	h, err := d.Create(ctx, driver.SandboxSpec{
		ID: "sbx_fc000000000001", Image: "alpine:3.20", CPU: 1.2, MemoryMB: 128, DiskMB: 64, PIDs: 32,
		Network: driver.NetNone, Env: map[string]string{"HI": "there"},
	})
	if err != nil || h.RuntimeName != "firecracker" {
		t.Fatal(err, h)
	}
	mu.Lock()
	boot := string(got["PUT /boot-source"])
	machine := string(got["PUT /machine-config"])
	mu.Unlock()
	if !strings.Contains(boot, "init=/opt/sbx/sbx-agent") || !strings.Contains(machine, `"vcpu_count":2`) || !strings.Contains(machine, `"mem_size_mib":128`) {
		t.Fatalf("boot %s machine %s", boot, machine)
	}
	if nic {
		t.Fatal("network=none should not attach a nic")
	}
	res, err := d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"echo", "hi"}})
	if err != nil || res.ExitCode != 0 {
		t.Fatal(res, err)
	}
	var stdout []byte
	res, err = d.ExecStream(ctx, h.ID, driver.ExecRequest{Cmd: []string{"sh", "-c", "echo out; echo err >&2"}}, func(chunk driver.ExecChunk) error {
		if chunk.Kind == driver.StreamStdout {
			stdout = append(stdout, chunk.Data...)
		}
		return nil
	})
	if err != nil || !bytes.Contains(stdout, []byte("out")) {
		t.Fatalf("%q %v %v", stdout, res, err)
	}
	file := filepath.Join(t.TempDir(), "note.txt")
	if err := d.WriteFile(ctx, h.ID, file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, err := d.ReadFile(ctx, h.ID, file, 100)
	if err != nil || string(body) != "hello" {
		t.Fatalf("%q %v", body, err)
	}
	entries, err := d.ListFiles(ctx, h.ID, filepath.Dir(file))
	if err != nil || len(entries) == 0 {
		t.Fatal(err, entries)
	}
	st, err := d.StatFile(ctx, h.ID, file)
	if err != nil || st.Name != "note.txt" {
		t.Fatal(st, err)
	}
	if err := d.Mkdir(ctx, h.ID, filepath.Join(filepath.Dir(file), "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveFile(ctx, h.ID, file); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte("pong"))
		_ = conn.Close()
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	raw, err := d.DialPort(ctx, h.ID, port)
	if err != nil {
		t.Fatal(err)
	}
	pong, _ := io.ReadAll(raw)
	_ = raw.Close()
	if string(pong) != "pong" {
		t.Fatalf("preview %q", pong)
	}
	wctx, wcancel := context.WithTimeout(ctx, 200*time.Millisecond)
	_ = d.Watch(wctx, h.ID, filepath.Dir(file), true, func(driver.FileEvent) error { return nil })
	wcancel()
	proc, err := d.StartProcess(ctx, h.ID, driver.ExecRequest{Cmd: []string{"sleep", "30"}}, false, 0, 0)
	if err != nil || proc.ID == "" {
		t.Fatal(proc, err)
	}
	if err := d.WriteProcessStdin(ctx, h.ID, proc.ID, []byte("x")); err != nil {
		t.Fatal(err)
	}
	sctx, scancel := context.WithTimeout(ctx, time.Second)
	_, _ = d.StreamProcess(sctx, h.ID, proc.ID, func(driver.ExecChunk) error { return nil })
	scancel()
	if tty, err := d.StartProcess(ctx, h.ID, driver.ExecRequest{Cmd: []string{"cat"}}, true, 80, 24); err == nil {
		_ = d.ResizeProcess(ctx, h.ID, tty.ID, 40, 12)
		_ = d.SignalProcess(ctx, h.ID, tty.ID, "KILL")
	}
	items, err := d.ListProcesses(ctx, h.ID)
	if err != nil || len(items) == 0 {
		t.Fatal(items, err)
	}
	if err := d.SignalProcess(ctx, h.ID, proc.ID, "KILL"); err != nil {
		t.Fatal(err)
	}
	if err := d.Pause(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	status, err := d.Inspect(ctx, h.ID)
	if err != nil || status.State != driver.StatePaused || status.SandboxID != h.ID {
		t.Fatalf("%+v %v", status, err)
	}
	if err := d.Resume(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	again, err := loaded.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"echo", "again"}})
	if err != nil || again.ExitCode != 0 {
		t.Fatal(again, err)
	}
	list, err := loaded.List(ctx)
	if err != nil || len(list) != 1 || list[0].State != driver.StateRunning {
		t.Fatalf("%+v %v", list, err)
	}
	if err := loaded.Delete(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.Inspect(ctx, h.ID); err != driver.ErrNotFound {
		t.Fatal(err)
	}
	if _, err := d.Inspect(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkAllowAndScripts(t *testing.T) {
	dir := t.TempDir()
	bin, kernel, agentBin := filepath.Join(dir, "bin", "firecracker"), filepath.Join(dir, "bin", "vmlinux"), filepath.Join(dir, "bin", "sbx-agent")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{bin, kernel, agentBin} {
		if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var script string
	d, err := New(Options{
		Bin: bin, Kernel: kernel, AgentBin: agentBin, DataDir: dir, SkipKVM: true,
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
		},
		SetupNet: func(_ context.Context, spec NetSpec) error {
			script = spec.Script
			return nil
		},
		TeardownNet: func(context.Context, NetSpec) error { return nil },
		Build: func(_ context.Context, spec RootSpec) error {
			return os.WriteFile(spec.Dest, []byte("disk"), 0o644)
		},
		Spawn: func(spec SpawnSpec) (Process, error) {
			ln, err := net.Listen("unix", spec.APISocket)
			if err != nil {
				return nil, err
			}
			srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})}
			go srv.Serve(ln)
			return &fakeProc{pid: os.Getpid(), stop: func() { _ = srv.Close(); _ = ln.Close() }}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	blocked := driver.NetPolicy{Allow: []string{"169.254.169.254"}, BlockMetadata: true, BlockPrivate: true}
	_, err = d.Create(ctx, driver.SandboxSpec{ID: "sbx_fc000000000002", Image: "alpine:3.20", CPU: 1, MemoryMB: 128, DiskMB: 64, Network: driver.NetFull, Policy: blocked})
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	open := driver.NetPolicy{Allow: []string{"example.com"}, Deny: []string{"198.51.100.9"}, BlockMetadata: true, BlockPrivate: true, MaxConns: 20, PrivateAllow: []string{"10.1.0.0/16"}}
	_, err = d.Create(short, driver.SandboxSpec{ID: "sbx_fc000000000003", Image: "alpine:3.20", CPU: 1, MemoryMB: 128, DiskMB: 64, Network: driver.NetFull, Policy: open})
	if err == nil || script == "" || !strings.Contains(script, "203.0.113.10/32") || !strings.Contains(script, "169.254.0.0/16") || !strings.Contains(script, "MASQUERADE") || !strings.Contains(script, "connlimit-above 20") || !strings.Contains(script, "198.51.100.9/32") || !strings.Contains(script, "10.1.0.0/16") {
		t.Fatalf("script %s err %v", script, err)
	}
	if _, err := d.Create(ctx, driver.SandboxSpec{ID: "sbx_fc000000000004", Image: "alpine:3.20", CPU: 1, MemoryMB: 128, DiskMB: 64, Network: driver.NetFull, Policy: driver.NetPolicy{EgressKbps: 100}}); err == nil || !strings.Contains(err.Error(), "egress_kbps") {
		t.Fatal(err)
	}
	if _, err := d.Create(ctx, driver.SandboxSpec{ID: "sbx_fc000000000005", Image: "alpine:3.20", CPU: 1, MemoryMB: 128, DiskMB: 64, Network: driver.NetNone, Policy: driver.NetPolicy{Allow: []string{"1.1.1.1"}}}); err == nil {
		t.Fatal("expected none to reject rules")
	}
	full, err := netScript(NetSpec{Tap: "fc0123456789ab", HostCIDR: "172.31.0.1/30", GuestIP: "172.31.0.2", Mode: driver.NetFull})
	if err != nil || !strings.Contains(full, "RETURN") {
		t.Fatal(full, err)
	}
	if _, err := netScript(NetSpec{Tap: "bad name"}); err == nil {
		t.Fatal("expected tap error")
	}
	down := teardownScript(NetSpec{Tap: "fc0123456789ab", GuestIP: "172.31.0.2"})
	if !strings.Contains(down, "ip link del") {
		t.Fatal(down)
	}
	if _, _, _, _, err := subnet(0); err == nil {
		t.Fatal("expected subnet error")
	}
	host, guest, ip, gw, err := subnet(2)
	if err != nil || host != "172.31.0.5/30" || guest != "172.31.0.6/30" || ip != "172.31.0.6" || gw != "172.31.0.5" {
		t.Fatalf("%s %s %s %s %v", host, guest, ip, gw, err)
	}
	if _, err := resolveAllow(ctx, []string{"10.0.0.0/7"}, driver.NetPolicy{BlockPrivate: true}, true, nil); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatal(err)
	}
	if _, err := resolveAllow(ctx, []string{" "}, driver.NetPolicy{}, true, nil); err == nil {
		t.Fatal("expected empty")
	}
	v6, err := resolveAllow(ctx, []string{"2001:db8::1"}, driver.NetPolicy{}, false, nil)
	if err != nil || len(ipv4Only(v6)) != 0 {
		t.Fatal(v6, err)
	}
}

func TestCheckAndRootfsHelpers(t *testing.T) {
	dir := t.TempDir()
	d, err := New(Options{DataDir: dir, SkipKVM: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Check(context.Background()); err == nil {
		t.Fatal("expected missing binary")
	}
	if _, err := New(Options{}); err == nil {
		t.Fatal("expected data dir error")
	}
	kernel := filepath.Join(dir, "kernel")
	disk := filepath.Join(dir, "disk")
	if err := os.WriteFile(kernel, []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disk, []byte("d"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "jail")
	if err := stageJail(root, kernel, disk, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "kernel")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "bin", Typeflag: tar.TypeDir, Mode: 0o755})
	body := []byte("hi")
	_ = tw.WriteHeader(&tar.Header{Name: "bin/echo-early", Typeflag: tar.TypeLink, Linkname: "bin/echo"})
	_ = tw.WriteHeader(&tar.Header{Name: "bin/echo", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))})
	_, _ = tw.Write(body)
	_ = tw.WriteHeader(&tar.Header{Name: "bin/echo-late", Typeflag: tar.TypeLink, Linkname: "bin/echo"})
	_ = tw.WriteHeader(&tar.Header{Name: "bin/sh", Typeflag: tar.TypeSymlink, Linkname: "echo"})
	_ = tw.Close()
	dest := filepath.Join(dir, "root")
	if err := extractTar(&buf, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "bin", "echo"))
	if err != nil || string(got) != "hi" {
		t.Fatal(err, got)
	}
	if target, err := os.Readlink(filepath.Join(dest, "bin", "sh")); err != nil || target != "echo" {
		t.Fatal(target, err)
	}
	for _, name := range []string{"echo-early", "echo-late"} {
		got, err := os.ReadFile(filepath.Join(dest, "bin", name))
		if err != nil || string(got) != "hi" {
			t.Fatal(name, err, got)
		}
	}
	src := filepath.Join(dir, "base.ext4")
	if err := os.WriteFile(src, []byte("ext4"), 0o644); err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(dir, "copy.ext4")
	if err := buildRootfs(context.Background(), "", filepath.Join(dir, "cache"), RootSpec{Image: "ext4:" + src, Dest: copied, DiskMB: 32, Guest: nil}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(copied); err != nil || string(raw) != "ext4" {
		t.Fatal(err, raw)
	}
	if err := d.EnsureImage(context.Background(), "ext4:"+copied); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureImage(context.Background(), "ext4:"+filepath.Join(dir, "missing")); err == nil {
		t.Fatal("expected missing ext4")
	}
	conn, err := DialVsock(filepath.Join(dir, "missing.sock"), 52)
	if err == nil {
		_ = conn.Close()
		t.Fatal("expected dial error")
	}
	bad, err := net.Listen("unix", filepath.Join(dir, "bad.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	go func() {
		c, err := bad.Accept()
		if err != nil {
			return
		}
		_, _ = c.Write([]byte("NO\n"))
		_ = c.Close()
	}()
	if conn, err = DialVsock(bad.Addr().String(), 52); err == nil {
		_ = conn.Close()
		t.Fatal("expected handshake error")
	}
	okSock := filepath.Join(dir, "ok.sock")
	okLn, err := net.Listen("unix", okSock)
	if err != nil {
		t.Fatal(err)
	}
	defer okLn.Close()
	go func() {
		c, err := okLn.Accept()
		if err != nil {
			return
		}
		r := bufio.NewReader(c)
		_, _ = r.ReadString('\n')
		_, _ = c.Write([]byte("OK 9\nleftover"))
		_ = c.Close()
	}()
	conn, err = DialVsock(okSock, 9)
	if err != nil {
		t.Fatal(err)
	}
	packet := make([]byte, 8)
	n, _ := conn.Read(packet)
	if string(packet[:n]) != "leftover" {
		t.Fatalf("buffered %q", packet[:n])
	}
	if _, err := conn.(interface {
		ReadFrom(io.Reader) (int64, error)
	}).ReadFrom(bytes.NewReader([]byte("x"))); err != nil && !strings.Contains(err.Error(), "closed") && !strings.Contains(err.Error(), "broken") {
		// The peer already closed. Either a short write or a closed-pipe error is fine.
	}
	_ = conn.Close()
	if vcpus(0) != 1 || vcpus(1.1) != 2 || vcpus(100) != 32 {
		t.Fatal(vcpus(0), vcpus(1.1), vcpus(100))
	}
	if jailRoot("/jails", "sbx_a") == "" {
		t.Fatal("jail root")
	}
	sum, err := fileSHA(kernel)
	if err != nil || len(sum) != 64 {
		t.Fatal(sum, err)
	}
	if cacheKey("alpine", sum, 32) == "" {
		t.Fatal("cache key")
	}
	if err := runScript(context.Background(), "true"); err != nil {
		t.Fatal(err)
	}
	if err := runScript(context.Background(), "false"); err == nil {
		t.Fatal("expected script error")
	}
	made := filepath.Join(dir, "made.ext4")
	if err := makeExt4(context.Background(), made, 16); err != nil {
		t.Fatal(err)
	}
	if err := inject(context.Background(), made, []byte(`{"token":"t"}`)); err != nil && os.Getuid() == 0 {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "no-docker.sock")); err == nil {
		t.Fatal("socket should not exist yet")
	}
	if err := buildOCI(context.Background(), filepath.Join(dir, "no-docker.sock"), RootSpec{Image: "alpine:3.20", Agent: kernel, Dest: filepath.Join(dir, "out.ext4"), DiskMB: 32}, filepath.Join(dir, "out.ext4")); err == nil {
		t.Fatal("expected docker error")
	}
	short, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := waitUnix(short, filepath.Join(dir, "missing.sock")); err == nil {
		t.Fatal("expected wait timeout")
	}
	if err := d.Ping(context.Background()); err == nil {
		t.Fatal("ping should fail without a binary")
	}
}

func TestSpawnAndReloadNet(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fc.sh")
	script := []byte("#!/bin/sh\ntrap '' TERM\nsleep 30\n")
	if err := os.WriteFile(bin, script, 0o755); err != nil {
		t.Fatal(err)
	}
	proc, err := spawnFirecracker(SpawnSpec{Bin: bin, APISocket: filepath.Join(dir, "api.sock"), Log: filepath.Join(dir, "c.log")})
	if err != nil || proc.PID() <= 1 {
		t.Fatal(err, proc)
	}
	if pidExited(proc.PID()) || pidExited(0) == false {
		t.Fatal("expected a live child and a missing pid to count as exited")
	}
	if err := proc.Stop(); err != nil {
		t.Fatal(err)
	}
	if !pidExited(proc.PID()) {
		t.Fatal("expected the stopped child to be gone")
	}
	if _, err := spawnFirecracker(SpawnSpec{}); err == nil {
		t.Fatal("expected missing binary")
	}
	jail := filepath.Join(dir, "jailer.sh")
	if err := os.WriteFile(jail, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	proc, err = spawnFirecracker(SpawnSpec{Jailer: jail, Bin: bin, ID: "sbx_jail", UID: 1, GID: 1, ChrootBase: dir, Log: filepath.Join(dir, "j.log")})
	if err != nil {
		t.Fatal(err)
	}
	_ = proc.Stop()

	data := filepath.Join(dir, "data")
	id := "sbx_fc000000000009"
	vmDir := filepath.Join(data, "firecracker", "vms", id)
	if err := os.MkdirAll(vmDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := record{
		ID: id, Rootfs: filepath.Join(vmDir, "rootfs.ext4"), APISocket: filepath.Join(vmDir, "api.sock"),
		Net:      &netRecord{Tap: "fc0123456789ab", GuestIP: "172.31.0.2", HostCIDR: "172.31.0.1/30", Mode: "full"},
		NetIndex: 4, PID: -1, Image: "alpine:3.20",
	}
	raw, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(vmDir, "vm.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec.Rootfs, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	torn := false
	d, err := New(Options{
		DataDir: data, SkipKVM: true, Bin: bin, Kernel: bin, AgentBin: bin,
		TeardownNet: func(context.Context, NetSpec) error { torn = true; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := d.Inspect(context.Background(), id)
	if err != nil || st.State != driver.StateStopped {
		t.Fatalf("%+v %v", st, err)
	}
	if err := d.Delete(context.Background(), id); err != nil || !torn {
		t.Fatal(err, torn)
	}
}

func (s SpawnSpec) VsockPath() string {
	return strings.TrimSuffix(s.APISocket, "api.sock") + "vsock.sock"
}

type fakeProc struct {
	pid  int
	stop func()
}

func (p *fakeProc) PID() int { return p.pid }
func (p *fakeProc) Stop() error {
	if p.stop != nil {
		p.stop()
	}
	return nil
}

func acceptVsock(ln net.Listener, token string) {
	a := agent.New(token)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(conn net.Conn) {
			r := bufio.NewReader(conn)
			line, err := r.ReadString('\n')
			if err != nil || !strings.HasPrefix(line, "CONNECT ") {
				_ = conn.Close()
				return
			}
			if _, err := io.WriteString(conn, "OK 52\n"); err != nil {
				_ = conn.Close()
				return
			}
			a.ServeConn(&stitch{Conn: conn, r: r})
		}(conn)
	}
}

type stitch struct {
	net.Conn
	r *bufio.Reader
}

func (s *stitch) Read(p []byte) (int, error) { return s.r.Read(p) }
