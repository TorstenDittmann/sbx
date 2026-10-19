// Package cli is the sbx command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	apispec "github.com/TorstenDittmann/sbx/api"
	"github.com/TorstenDittmann/sbx/internal/api"
	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/core"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/docker"
	"github.com/TorstenDittmann/sbx/internal/id"
	"github.com/TorstenDittmann/sbx/internal/store"
	"github.com/TorstenDittmann/sbx/internal/version"
)

const usageText = `sbx is a single-node sandbox server.

Usage:
  sbx serve [--config sbx.yaml] [--addr 127.0.0.1:8080] [--data-dir ./data] [--backend docker|gvisor]
  sbx doctor [--config sbx.yaml]
  sbx keys create --name NAME [--admin] [--data-dir ./data]
  sbx healthcheck
  sbx version

Environment (overrides the config file):
  SBX_ADDR SBX_DATA_DIR SBX_BACKEND SBX_DOCKER_HOST SBX_BOOTSTRAP_KEY
  SBX_GVISOR_RUNTIME SBX_READ_ONLY_ROOTFS SBX_SANDBOX_ROOT SBX_LOG_LEVEL
  SBX_CONFIG SBX_AGENT_BIN SBX_NETWORK_MODE SBX_DNS SBX_IPV6
  SBX_PREVIEW_DOMAIN SBX_PUBLIC_URL
`

// Run executes a command. args is os.Args[1:].
func Run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usageText)
		return errors.New("a command is required")
	}
	switch args[0] {
	case "serve", "server":
		return serve(args[1:])
	case "doctor":
		return doctor(args[1:])
	case "healthcheck":
		return healthcheck(args[1:])
	case "keys":
		return keys(args[1:])
	case "version", "--version", "-v":
		fmt.Fprint(os.Stdout, version.Format())
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(os.Stdout, usageText)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func serve(args []string) error {
	cfg, err := loadFlags("serve", args)
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel, os.Stderr)
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	cfg.Docker.AgentBin = agentBinary(cfg)
	if cfg.Docker.AgentBin == "" {
		log.Warn("sbx-agent not found; exec and files use a shell inside the image")
	} else {
		log.Info("sandbox agent", "path", cfg.Docker.AgentBin)
	}
	drivers, err := openDrivers(cfg)
	if err != nil {
		return err
	}
	if err := checkDrivers(context.Background(), cfg, drivers, log); err != nil {
		return err
	}
	mgr := core.New(cfg, st, drivers, log)
	if err := mgr.Seed(context.Background()); err != nil {
		return err
	}
	plain, err := ensureKey(context.Background(), st, cfg)
	if err != nil {
		return err
	}
	if plain != "" {
		log.Warn("generated API key; store it now, it will not be shown again", "SBX_API_KEY", plain)
		fmt.Fprintf(os.Stderr, "\nSBX_API_KEY=%s\n\n", plain)
	}
	if _, err := mgr.Sweep(context.Background()); err != nil {
		log.Warn("startup sweep failed", "err", err)
	}
	if err := mgr.Reconcile(context.Background()); err != nil {
		log.Warn("startup reconcile failed", "err", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return runServer(ctx, cfg, mgr, log)
}

func runServer(ctx context.Context, cfg config.Config, mgr *core.Manager, log *slog.Logger) error {
	go mgr.RunReaper(ctx)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.New(mgr, log, apispec.OpenAPI).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Info("listening", "addr", cfg.Addr, "backend", cfg.Backend, "version", version.Version)
	select {
	case <-ctx.Done():
		log.Info("shutting down")
		shut, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shut)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func doctor(args []string) error {
	cfg, err := loadFlags("doctor", args)
	if err != nil {
		return err
	}
	ok := true
	report := func(pass bool, name, detail string) {
		state := "ok"
		if !pass {
			state = "fail"
			ok = false
		}
		fmt.Printf("%-6s %s", state, name)
		if detail != "" {
			fmt.Printf(" — %s", detail)
		}
		fmt.Println()
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		report(false, "data dir", err.Error())
	} else {
		report(true, "data dir", cfg.DataDir)
	}
	drivers, err := openDrivers(cfg)
	if err != nil {
		report(false, "docker socket", err.Error())
		return errors.New("doctor found problems")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dock := drivers["docker"].(*docker.Driver)
	if err := dock.Ping(ctx); err != nil {
		report(false, "docker", err.Error())
	} else {
		report(true, "docker", "daemon reachable")
	}
	gvis := drivers["gvisor"].(*docker.Driver)
	if err := gvis.Check(ctx); err != nil {
		detail := err.Error() + " (optional unless --backend gvisor; install runsc and register it as a Docker runtime)"
		if cfg.Backend == "gvisor" {
			report(false, "gvisor", detail)
		} else {
			fmt.Printf("%-6s %s — %s\n", "warn", "gvisor", detail)
		}
	} else {
		report(true, "gvisor", "runsc is registered")
	}
	report(true, "backend", cfg.Backend)
	bin := agentBinary(cfg)
	if bin == "" {
		fmt.Printf("%-6s %s — %s\n", "warn", "agent", "sbx-agent not found; exec and files use a shell inside the image")
	} else if st, err := os.Stat(bin); err != nil || st.IsDir() {
		report(false, "agent", bin)
	} else {
		report(true, "agent", bin)
	}
	if !ok {
		return errors.New("doctor found problems")
	}
	return nil
}

func healthcheck(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addr := fs.String("addr", os.Getenv("SBX_ADDR"), "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	url := healthURL(*addr)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck %s returned %s", url, resp.Status)
	}
	return nil
}

// HealthURL maps a listen address to the local health endpoint.
func HealthURL(addr string) string { return healthURL(addr) }

func healthURL(addr string) string {
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/") + "/healthz"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://127.0.0.1:8080/healthz"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz"
}

func keys(args []string) error {
	if len(args) == 0 || args[0] != "create" {
		return errors.New("usage: sbx keys create --name NAME [--admin]")
	}
	fs := flag.NewFlagSet("keys create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "key name")
	admin := fs.Bool("admin", false, "grant the admin scope")
	dataDir := fs.String("data-dir", "", "data directory")
	configPath := fs.String("config", os.Getenv("SBX_CONFIG"), "config file")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("--name is required")
	}
	cfg, err := load(*configPath)
	if err != nil {
		return err
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()
	scopes := []string{"sandbox"}
	if *admin {
		scopes = []string{"admin"}
	}
	plain, hash, prefix := id.Key()
	key := store.APIKey{
		ID: id.RecordID(), Name: *name, Hash: hash, Prefix: prefix, Scopes: scopes, CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateKey(context.Background(), key); err != nil {
		return err
	}
	fmt.Println(plain)
	return nil
}

func loadFlags(name string, args []string) (config.Config, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", os.Getenv("SBX_CONFIG"), "config file")
	addr := fs.String("addr", "", "listen address")
	dataDir := fs.String("data-dir", "", "data directory")
	backend := fs.String("backend", "", "docker or gvisor")
	dockerHost := fs.String("docker-host", "", "docker socket")
	agentBin := fs.String("agent-bin", "", "path to the sbx-agent binary")
	bootstrap := fs.String("bootstrap-key", "", "API key created on first boot")
	if err := fs.Parse(args); err != nil {
		return config.Config{}, err
	}
	cfg, err := load(*configPath)
	if err != nil {
		return config.Config{}, err
	}
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}
	if *backend != "" {
		cfg.Backend = *backend
	}
	if *dockerHost != "" {
		cfg.Docker.Host = *dockerHost
	}
	if *agentBin != "" {
		cfg.Docker.AgentBin = *agentBin
	}
	if *bootstrap != "" {
		cfg.Auth.BootstrapKey = *bootstrap
	}
	if err := cfg.Validate(); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func load(path string) (config.Config, error) {
	if path == "" {
		if _, err := os.Stat("sbx.yaml"); err == nil {
			path = "sbx.yaml"
		}
	}
	return config.Load(path)
}

func agentBinary(cfg config.Config) string {
	if cfg.Docker.AgentBin != "" {
		return cfg.Docker.AgentBin
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	candidate := filepath.Join(filepath.Dir(exe), "sbx-agent")
	st, err := os.Stat(candidate)
	if err != nil || st.IsDir() {
		return ""
	}
	return candidate
}

func openDrivers(cfg config.Config) (map[string]driver.Driver, error) {
	base := docker.Options{
		Host:             cfg.Docker.Host,
		ReadOnlyRootfs:   cfg.Docker.ReadOnlyRootfs,
		UID:              cfg.Docker.SandboxUID,
		GID:              cfg.Docker.SandboxGID,
		MaxOutputBytes:   cfg.Limits.MaxOutputBytes,
		AgentBin:         agentBinary(cfg),
		Root:             cfg.Docker.SandboxRoot,
		ConfiguredPolicy: true,
		BlockPrivate:     cfg.Network.BlockPrivate,
		BlockMetadata:    cfg.Network.BlockMetadata,
		PrivateAllow:     append([]string{}, cfg.Network.PrivateAllow...),
		IPv6:             cfg.Network.IPv6,
	}
	dockOpt := base
	dockOpt.Name = "docker"
	gOpt := base
	gOpt.Name = "gvisor"
	gOpt.Runtime = cfg.Docker.GVisorRuntime
	dock, err := docker.New(dockOpt)
	if err != nil {
		return nil, err
	}
	gvis, err := docker.New(gOpt)
	if err != nil {
		return nil, err
	}
	return map[string]driver.Driver{"docker": dock, "gvisor": gvis}, nil
}

func checkDrivers(ctx context.Context, cfg config.Config, drivers map[string]driver.Driver, log *slog.Logger) error {
	dock := drivers["docker"].(*docker.Driver)
	if err := dock.Ping(ctx); err != nil {
		return fmt.Errorf("docker is not reachable: %w", err)
	}
	gvis := drivers["gvisor"].(*docker.Driver)
	if err := gvis.Check(ctx); err != nil {
		if cfg.Backend == "gvisor" {
			return err
		}
		log.Warn("gvisor runtime is not registered; backend=gvisor will fail until runsc is installed", "err", err)
	}
	return nil
}

func ensureKey(ctx context.Context, st *store.Store, cfg config.Config) (string, error) {
	n, err := st.CountKeys(ctx)
	if err != nil {
		return "", err
	}
	if n > 0 {
		return "", nil
	}
	plain := cfg.Auth.BootstrapKey
	generated := false
	if plain == "" {
		var hash, prefix string
		plain, hash, prefix = id.Key()
		generated = true
		if err := st.CreateKey(ctx, store.APIKey{
			ID: id.RecordID(), Name: "bootstrap", Hash: hash, Prefix: prefix,
			Scopes: []string{"admin"}, CreatedAt: time.Now().UTC(),
		}); err != nil {
			return "", err
		}
		if err := os.WriteFile(cfg.KeyPath(), []byte(plain+"\n"), 0o600); err != nil {
			return "", err
		}
		return plain, nil
	}
	prefix := plain
	if len(prefix) > 12 {
		prefix = plain[:12]
	}
	if err := st.CreateKey(ctx, store.APIKey{
		ID: id.RecordID(), Name: "bootstrap", Hash: id.Hash(plain), Prefix: prefix,
		Scopes: []string{"admin"}, CreatedAt: time.Now().UTC(),
	}); err != nil {
		return "", err
	}
	if generated {
		return plain, nil
	}
	return "", nil
}

func newLogger(level string, w io.Writer) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lv}))
}
