package docker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

type fakeDaemon struct {
	mu         sync.Mutex
	images     map[string]bool
	containers map[string]*fakeContainer
	execs      map[string]string
	runtimes   map[string]bool
	created    createRequest
	pulls      []string
	network    bool
	netStatus  int
	netCreates int
	waitCode   int
	logs       string
	lastCmd    []string
	execCode   int
	failStart  bool
	failPull   bool
	pullGhost  bool
	volumes    map[string]map[string]string
	archives   map[string][]byte
	onAttach   func(stdin io.Reader, stdout io.Writer)
}

type fakeContainer struct {
	id     string
	status string
	body   createRequest
}

func startFakeDaemon(t *testing.T) (*fakeDaemon, string) {
	t.Helper()
	fd := &fakeDaemon{
		images:     map[string]bool{},
		containers: map[string]*fakeContainer{},
		execs:      map[string]string{},
		runtimes:   map[string]bool{"runc": true},
		volumes:    map[string]map[string]string{},
		archives:   map[string][]byte{},
	}
	ln, err := net.Listen("unix", t.TempDir()+"/docker.sock")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: fd}
	go srv.Serve(ln)
	t.Cleanup(func() {
		srv.Close()
		ln.Close()
	})
	return fd, ln.Addr().String()
}

func (f *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/version":
		writeJSON(w, http.StatusOK, map[string]string{"ApiVersion": "1.44", "MinAPIVersion": "1.44"})
	case r.Method == http.MethodPost && p == "/v1.44/networks/create":
		f.mu.Lock()
		status := f.netStatus
		f.netCreates++
		if status == 0 || status == http.StatusCreated || status == http.StatusConflict {
			f.network = true
		}
		f.mu.Unlock()
		if status == http.StatusConflict {
			writeJSON(w, http.StatusConflict, map[string]string{"message": "network exists"})
			return
		}
		if status >= 300 {
			writeJSON(w, status, map[string]string{"message": "network create failed"})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"Id": "net-sbx"})
	case r.Method == http.MethodGet && p == "/v1.44/networks/sbx":
		f.mu.Lock()
		ok := f.network
		f.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "network sbx not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"Id": "netsbx0123456789", "Name": "sbx",
			"Options": map[string]string{"com.docker.network.bridge.name": "sbx0"},
			"IPAM":    map[string]any{"Config": []map[string]string{{"Subnet": "172.30.0.0/16", "Gateway": "172.30.0.1"}}},
		})
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/v1.44/containers/") && strings.HasSuffix(p, "/wait"):
		f.mu.Lock()
		code := f.waitCode
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]int{"StatusCode": code})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/v1.44/containers/") && strings.HasSuffix(p, "/logs"):
		f.mu.Lock()
		msg := f.logs
		f.mu.Unlock()
		if msg == "" {
			msg = "ok\n"
		}
		writeFrame(w, 1, msg)
	case p == "/v1.44/_ping":
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "OK")
	case p == "/v1.44/info":
		f.mu.Lock()
		rts := map[string]any{}
		for name := range f.runtimes {
			rts[name] = map[string]string{"path": name}
		}
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ServerVersion": "test", "Runtimes": rts})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/v1.44/images/") && strings.HasSuffix(p, "/json"):
		ref := imageRefFromPath(p)
		f.mu.Lock()
		ok := f.images[ref]
		f.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such image: " + ref})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"Config": map[string]any{"Env": []string{"PATH=/usr/bin:/bin"}}})
	case r.Method == http.MethodPost && p == "/v1.44/images/create":
		f.mu.Lock()
		fail, ghost := f.failPull, f.pullGhost
		f.mu.Unlock()
		if fail {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "pull failed"})
			return
		}
		if ghost {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ghost"})
			return
		}
		ref := r.URL.Query().Get("fromImage")
		if tag := r.URL.Query().Get("tag"); tag != "" {
			ref += ":" + tag
		}
		f.mu.Lock()
		f.images[ref] = true
		f.pulls = append(f.pulls, ref)
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"status": "pulled"})
	case r.Method == http.MethodPost && p == "/v1.44/containers/create":
		var body createRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		id := "cid-" + r.URL.Query().Get("name")
		f.mu.Lock()
		f.created = body
		f.containers[id] = &fakeContainer{id: id, status: "created", body: body}
		f.mu.Unlock()
		writeJSON(w, http.StatusCreated, map[string]string{"Id": id})
	case r.Method == http.MethodGet && p == "/v1.44/containers/json":
		f.mu.Lock()
		var list []any
		for id, ctn := range f.containers {
			list = append(list, map[string]any{"Id": id, "Image": ctn.body.Image, "State": ctn.status, "Labels": ctn.body.Labels})
		}
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, list)
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/v1.44/containers/") && strings.HasSuffix(p, "/start"):
		f.mu.Lock()
		fail := f.failStart
		f.mu.Unlock()
		if fail {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "cannot start"})
			return
		}
		f.setStatus(containerID(p), "running")
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/v1.44/containers/") && strings.HasSuffix(p, "/pause"):
		f.setStatus(containerID(p), "paused")
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/v1.44/containers/") && strings.HasSuffix(p, "/unpause"):
		f.setStatus(containerID(p), "running")
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/v1.44/containers/"):
		id := containerID(p)
		f.mu.Lock()
		delete(f.containers, id)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/v1.44/containers/") && strings.HasSuffix(p, "/json"):
		id := containerID(p)
		f.mu.Lock()
		ctn, ok := f.containers[id]
		f.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"Id": id, "State": map[string]any{"Status": ctn.status, "Running": ctn.status == "running", "Paused": ctn.status == "paused", "Pid": 4242},
			"Config": map[string]any{"Image": ctn.body.Image, "Labels": ctn.body.Labels, "Env": ctn.body.Env},
			"NetworkSettings": map[string]any{"Networks": map[string]any{
				"sbx": map[string]any{"IPAddress": "172.30.0.8", "GlobalIPv6Address": ""},
			}},
		})
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/v1.44/containers/") && strings.HasSuffix(p, "/exec"):
		var body execConfig
		_ = json.NewDecoder(r.Body).Decode(&body)
		id := "exec-1"
		f.mu.Lock()
		f.execs[id] = containerID(p)
		f.lastCmd = body.Cmd
		f.mu.Unlock()
		writeJSON(w, http.StatusCreated, map[string]string{"Id": id})
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/v1.44/exec/") && strings.HasSuffix(p, "/start"):
		_, _ = io.Copy(io.Discard, r.Body)
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", 500)
			return
		}
		conn, bufrw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Type: application/vnd.docker.multiplexed-stream\r\n\r\n")
		_ = bufrw.Flush()
		f.mu.Lock()
		cmd := strings.Join(f.lastCmd, " ")
		bridge := f.onAttach
		f.mu.Unlock()
		if bridge != nil && strings.Contains(cmd, "sbx-agent") && strings.Contains(cmd, "dial") {
			bridge(bufrw, conn)
			return
		}
		stdout := "hello\n"
		if strings.Contains(cmd, "stat -c") {
			stdout = "regular file|5|1700000000|81a4|/workspace/a.txt\n" +
				"directory|0|1700000000|41ed|/workspace/sub\n" +
				"symbolic link|0|1700000000|a1ff|/workspace/link\n"
		}
		writeFrame(conn, 1, stdout)
		writeFrame(conn, 2, "warn\n")
	case r.Method == http.MethodPost && p == "/v1.44/volumes/create":
		var body struct {
			Name   string            `json:"Name"`
			Labels map[string]string `json:"Labels"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		_, exists := f.volumes[body.Name]
		if !exists {
			if body.Labels == nil {
				body.Labels = map[string]string{}
			}
			f.volumes[body.Name] = body.Labels
		}
		f.mu.Unlock()
		if exists {
			writeJSON(w, http.StatusConflict, map[string]string{"message": "volume exists"})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"Name": body.Name})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/v1.44/volumes/"):
		name := strings.TrimPrefix(p, "/v1.44/volumes/")
		f.mu.Lock()
		labels, ok := f.volumes[name]
		f.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "no such volume"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"Name": name, "Labels": labels})
	case r.Method == http.MethodPut && strings.HasPrefix(p, "/v1.44/containers/") && strings.HasSuffix(p, "/archive"):
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		f.archives[containerID(p)] = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/v1.44/exec/") && strings.HasSuffix(p, "/json"):
		f.mu.Lock()
		code := f.execCode
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ID": "exec-1", "ExitCode": code, "Running": false})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "unhandled " + r.Method + " " + p})
	}
}

func (f *fakeDaemon) setStatus(id, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ctn, ok := f.containers[id]; ok {
		ctn.status = status
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeFrame(w io.Writer, stream byte, payload string) {
	hdr := make([]byte, 8)
	hdr[0] = stream
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload)))
	_, _ = w.Write(hdr)
	_, _ = w.Write([]byte(payload))
}

func indexOf(s, part string) int {
	for i := 0; i+len(part) <= len(s); i++ {
		if s[i:i+len(part)] == part {
			return i
		}
	}
	return -1
}

func containerID(p string) string {
	// /v1.44/containers/{id}/action
	const prefix = "/v1.44/containers/"
	rest := p[len(prefix):]
	if i := indexOf(rest, "/"); i >= 0 {
		return rest[:i]
	}
	return rest
}

func imageRefFromPath(p string) string {
	const prefix = "/v1.44/images/"
	rest := p[len(prefix):]
	if i := indexOf(rest, "/json"); i >= 0 {
		rest = rest[:i]
	}
	if decoded, err := url.PathUnescape(rest); err == nil {
		return decoded
	}
	return rest
}

func TestDriverAgainstFakeDaemon(t *testing.T) {
	fd, sock := startFakeDaemon(t)
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	d := NewWithClient(Options{Name: "docker", UID: 1000, GID: 1000, ReadOnlyRootfs: true, MaxOutputBytes: 1 << 20}, cli)
	ctx := context.Background()
	if err := d.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	h, err := d.Create(ctx, driver.SandboxSpec{
		ID: "sbx_aaaabbbbccccdddd", Image: "alpine:3.20", CPU: 0.5, MemoryMB: 128, DiskMB: 32, PIDs: 64,
		Network: driver.NetNone, Labels: map[string]string{"user": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	body := fd.created
	pulls := append([]string(nil), fd.pulls...)
	fd.mu.Unlock()
	if body.HostConfig.NetworkMode != "none" || body.HostConfig.CapDrop[0] != "ALL" || !body.HostConfig.ReadonlyRootfs {
		t.Fatalf("insecure create: %+v", body.HostConfig)
	}
	if body.User != "1000:1000" || body.HostConfig.Privileged {
		t.Fatalf("%+v", body)
	}
	if len(pulls) != 1 || pulls[0] != "alpine:3.20" {
		t.Fatalf("pulls %v", pulls)
	}
	res, err := d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"echo", "hi"}, TimeoutSeconds: 5})
	if err != nil || res.ExitCode != 0 || string(res.Stdout) != "hello\n" || string(res.Stderr) != "warn\n" {
		t.Fatalf("%+v %v", res, err)
	}
	st, err := d.Inspect(ctx, h.ID)
	if err != nil || st.State != driver.StateRunning || st.SandboxID != "sbx_aaaabbbbccccdddd" {
		t.Fatalf("%+v %v", st, err)
	}
	if err := d.Pause(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	st, err = d.Inspect(ctx, h.ID)
	if err != nil || st.State != driver.StatePaused {
		t.Fatalf("paused %+v %v", st, err)
	}
	if err := d.Resume(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	list, err := d.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("%+v %v", list, err)
	}
	if err := d.Delete(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Inspect(ctx, h.ID); err == nil {
		t.Fatal("expected not found")
	}

	gv := NewWithClient(Options{Name: "gvisor", Runtime: "runsc", UID: 1000, GID: 1000, ReadOnlyRootfs: true, NoEgressFilter: true}, cli)
	_, err = gv.Create(ctx, driver.SandboxSpec{ID: "sbx_gvisor00000001", Image: "alpine:3.20", CPU: 1, MemoryMB: 64, DiskMB: 32, PIDs: 32})
	if err == nil {
		t.Fatal("expected missing runtime")
	}
	fd.mu.Lock()
	fd.runtimes["runsc"] = true
	fd.mu.Unlock()
	if _, err := gv.Create(ctx, driver.SandboxSpec{ID: "sbx_gvisor00000001", Image: "alpine:3.20", CPU: 1, MemoryMB: 64, DiskMB: 32, PIDs: 32, Network: driver.NetFull}); err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	if fd.created.HostConfig.Runtime != "runsc" || fd.created.HostConfig.NetworkMode != "sbx" {
		t.Fatalf("%+v", fd.created.HostConfig)
	}
	fd.mu.Unlock()
}
