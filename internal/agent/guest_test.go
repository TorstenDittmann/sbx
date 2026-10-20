package agent

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDialedSessionSharesProcessTable(t *testing.T) {
	a := New("tok")
	server, client := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	once := make(chan struct{}, 1)
	once <- struct{}{}
	go serveDialed(ctx, a, func() (net.Conn, error) {
		select {
		case <-once:
			return server, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	hctx, hcancel := context.WithTimeout(context.Background(), 3*time.Second)
	dialed, err := Handshake(hctx, client, "tok")
	hcancel()
	if err != nil {
		t.Fatal(err)
	}
	defer dialed.Close()
	other, done := pair(t, a)
	defer done()

	run, cancelRun := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRun()
	info, err := dialed.Start(run, ExecSpec{Cmd: []string{"sleep", "30"}})
	if err != nil || info.ID == "" {
		t.Fatalf("start %+v %v", info, err)
	}
	defer func() { _ = other.Signal(context.Background(), info.ID, "KILL") }()
	list, err := other.List(run)
	if err != nil || len(list) != 1 || list[0].ID != info.ID {
		t.Fatalf("list %+v %v", list, err)
	}
	if err := other.Signal(run, info.ID, "KILL"); err != nil {
		t.Fatal(err)
	}
}

func TestGuestConfigAndMessages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "guest.json")
	if err := os.WriteFile(path, []byte(`{"token":"secret","ip":"10.0.0.2/30","gateway":"10.0.0.1","env":{"A":"b","SBX_AGENT_TOKEN":"no"},"dns":["1.1.1.1",""]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvGuestConfig, path)
	t.Setenv(EnvToken, "")
	cfg, err := loadGuest()
	if err != nil || cfg.Token != "secret" || cfg.VsockPort != DefaultVsockPort {
		t.Fatalf("%+v %v", cfg, err)
	}
	tok, err := tokenFrom(cfg)
	if err != nil || tok != "secret" {
		t.Fatal(tok, err)
	}
	applyGuestEnv(cfg)
	if os.Getenv("A") != "b" || os.Getenv(EnvToken) != "" {
		t.Fatalf("env A=%q token=%q", os.Getenv("A"), os.Getenv(EnvToken))
	}
	body := resolvBody(cfg.DNS)
	if body != "nameserver 1.1.1.1\n" {
		t.Fatalf("%q", body)
	}
	msg, err := addrMessage(2, net.ParseIP("10.0.0.2"), 30)
	if err != nil || len(msg) < 16 {
		t.Fatal(err, len(msg))
	}
	if binary.LittleEndian.Uint16(msg[4:]) != 20 {
		t.Fatalf("type %d", binary.LittleEndian.Uint16(msg[4:]))
	}
	route, err := routeMessage(2, net.ParseIP("10.0.0.1"))
	if err != nil || binary.LittleEndian.Uint16(route[4:]) != 24 {
		t.Fatal(err)
	}
	if _, err := addrMessage(1, net.ParseIP("2001:db8::1"), 64); err == nil {
		t.Fatal("expected ipv4 error")
	}
	if _, err := routeMessage(1, nil); err == nil {
		t.Fatal("expected gateway error")
	}
	if err := configureGuestNet("", ""); err != nil {
		t.Fatal(err)
	}
	if err := configureGuestNet("not-a-cidr", ""); err == nil {
		t.Fatal("expected parse error")
	}
	cfg.IP = ""
	cfg.Gateway = ""
	cfg.DNS = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := serveGuest(ctx, cfg); err != nil && !strings.Contains(err.Error(), "vsock") {
		t.Fatal(err)
	}
	if ln, err := listenVsock(52); err != nil {
		if !strings.Contains(err.Error(), "vsock") {
			t.Fatal(err)
		}
	} else {
		_ = ln.Close()
	}
	if _, err := listenAddr("vsock:nope"); err == nil {
		t.Fatal("expected port error")
	}
	sock := filepath.Join(dir, "agent.sock")
	ln, err := listenAddr("unix:" + sock)
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
	reapZombies()
	if err := exerciseVsockConn(); err != nil {
		t.Fatal(err)
	}
	if conn, err := dialHostVsock(1); err == nil {
		_ = conn.Close()
		t.Fatal("dialHostVsock(1) succeeded; expected an error with nothing listening")
	} else if err.Error() == "" {
		t.Fatal("dialHostVsock(1) returned an empty error")
	}
	cancelled, cancelDial := context.WithCancel(context.Background())
	cancelDial()
	stopped := make(chan struct{})
	go func() {
		dialHostUntil(cancelled, 1, New("tok"))
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("dialHostUntil ignored a cancelled context")
	}
	if err := preparePID1(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvGuestConfig, filepath.Join(dir, "missing"))
	if _, err := loadGuest(); err == nil {
		t.Fatal("expected missing file")
	}
}
