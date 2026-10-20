package agent

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/TorstenDittmann/sbx/internal/version"
)

const (
	// EnvToken is the per-sandbox shared secret.
	EnvToken = "SBX_AGENT_TOKEN"
	// EnvSocket is the unix socket the agent listens on inside the sandbox.
	EnvSocket = "SBX_AGENT_SOCK"
	// DefaultSocket is used when EnvSocket is unset.
	DefaultSocket = "/tmp/sbx-agent.sock"
)

// Run is the sbx-agent command line. args does not include argv0.
// With no arguments, a guest.json (Firecracker init) serves on vsock.
// Otherwise it listens on the unix socket used by Docker and Kata.
func Run(args []string) error {
	// The kernel passes unrecognized boot args to init. A guest.json token
	// means this process is the microVM init, whatever those args were.
	if len(args) == 0 || !knownAgentCommand(args[0]) {
		if cfg, err := loadGuest(); err == nil && cfg.Token != "" {
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return serveGuest(ctx, cfg)
		}
		if len(args) == 0 {
			args = []string{"init"}
		}
	}
	switch args[0] {
	case "init":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return Init(ctx)
	case "dial":
		return Dial(os.Stdin, os.Stdout)
	case "serve":
		return serveListen(args[1:])
	case "version", "--version", "-v":
		fmt.Fprint(os.Stdout, version.FormatBinary("sbx-agent"))
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(os.Stdout, "usage: sbx-agent init | dial | serve -listen ADDR | version\n")
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func knownAgentCommand(s string) bool {
	switch s {
	case "init", "dial", "serve", "version", "--version", "-v", "help", "-h", "--help":
		return true
	default:
		return false
	}
}

// Init listens on the unix socket and serves until ctx is cancelled.
func Init(ctx context.Context) error {
	cfg, _ := loadGuest()
	token, err := tokenFrom(cfg)
	if err != nil {
		return err
	}
	sock := os.Getenv(EnvSocket)
	if sock == "" {
		sock = DefaultSocket
	}
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	_ = os.Chmod(sock, 0o600)
	defer ln.Close()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	a := New(token)
	var tempDelay time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			tempDelay = nextDelay(tempDelay)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(tempDelay):
			}
			continue
		}
		tempDelay = 0
		go a.ServeConn(conn)
	}
}

func nextDelay(d time.Duration) time.Duration {
	if d == 0 {
		return 5 * time.Millisecond
	}
	d *= 2
	if d > time.Second {
		d = time.Second
	}
	return d
}

// Dial connects to the in-sandbox socket and splices r and w to it.
// Docker reaches the agent by exec'ing this command.
func Dial(r io.Reader, w io.Writer) error {
	sock := os.Getenv(EnvSocket)
	if sock == "" {
		sock = DefaultSocket
	}
	conn, err := dialUnix(sock, 10*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	errc := make(chan error, 2)
	go func() {
		_, err := io.Copy(w, conn)
		errc <- err
	}()
	go func() {
		_, err := io.Copy(conn, r)
		errc <- err
		_ = conn.Close()
	}()
	err = <-errc
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

func dialUnix(sock string, wait time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(wait)
	var last error
	for {
		conn, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
		if err == nil {
			return conn, nil
		}
		last = err
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("agent socket %s: %w", sock, last)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func serveListen(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	listen := fs.String("listen", "127.0.0.1:0", "tcp, unix:PATH, or vsock:PORT")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, _ := loadGuest()
	token, err := tokenFrom(cfg)
	if err != nil {
		return err
	}
	ln, err := listenAddr(*listen)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return serveListener(ctx, ln, token)
}

func listenAddr(addr string) (net.Listener, error) {
	switch {
	case strings.HasPrefix(addr, "vsock:"):
		port, err := strconv.ParseUint(strings.TrimPrefix(addr, "vsock:"), 10, 32)
		if err != nil || port == 0 {
			return nil, fmt.Errorf("vsock port %q", strings.TrimPrefix(addr, "vsock:"))
		}
		return listenVsock(uint32(port))
	case strings.HasPrefix(addr, "unix:"):
		path := strings.TrimPrefix(addr, "unix:")
		_ = os.Remove(path)
		return net.Listen("unix", path)
	default:
		return net.Listen("tcp", addr)
	}
}

// serveGuest is PID 1 inside a Firecracker microVM.
func serveGuest(ctx context.Context, cfg GuestConfig) error {
	token, err := tokenFrom(cfg)
	if err != nil {
		return err
	}
	if err := preparePID1(); err != nil {
		return err
	}
	reapZombies()
	applyGuestEnv(cfg)
	if err := configureGuestNet(cfg.IP, cfg.Gateway); err != nil {
		return err
	}
	if err := writeResolv("/etc/resolv.conf", cfg.DNS); err != nil {
		return err
	}
	port := cfg.VsockPort
	if port == 0 {
		port = DefaultVsockPort
	}
	ln, err := listenVsock(uint32(port))
	if err != nil {
		return err
	}
	go dialHostUntil(ctx, uint32(port), token)
	return serveListener(ctx, ln, token)
}

func serveListener(ctx context.Context, ln net.Listener, token string) error {
	fmt.Fprintf(os.Stderr, "sbx-agent listening on %s\n", ln.Addr())
	defer ln.Close()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	a := New(token)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		go a.ServeConn(conn)
	}
}
