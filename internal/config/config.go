// Package config loads sbx settings from defaults, a YAML file, environment
// variables, and process flags. Later sources win.
package config

import (
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the resolved server configuration.
type Config struct {
	Addr        string
	DataDir     string
	Backend     string
	LogLevel    string
	Docker      DockerConfig
	Kata        KataConfig
	Firecracker FirecrackerConfig
	Limits      Limits
	Auth        AuthConfig
	Network     NetworkConfig
	Builds      BuildConfig
}

// DockerConfig controls the engine socket and sandbox hardening.
type DockerConfig struct {
	Host           string
	GVisorRuntime  string
	ReadOnlyRootfs bool
	SandboxUID     int
	SandboxGID     int
	// AgentBin is the host path of the static sbx-agent binary. Empty keeps
	// the shell exec and file path.
	AgentBin string
	// SandboxRoot runs sandboxes as uid 0 so image package managers can write
	// the rootfs. The default stays uid 1000.
	SandboxRoot bool
}

// KataConfig selects the Docker runtime class used for Kata sandboxes.
type KataConfig struct {
	// Runtime is the name in daemon.json. Default "kata".
	Runtime string
	// RuntimeType is the containerd shim. Default "io.containerd.kata.v2".
	RuntimeType string
}

// FirecrackerConfig selects the microVM binary, kernel, and optional jailer.
// Empty Bin and Kernel mean the files written by `sbx setup firecracker`
// under the data directory, then a firecracker binary on PATH.
type FirecrackerConfig struct {
	Bin    string
	Kernel string
	// Jailer is a path to the Firecracker jailer. Empty leaves the jailer off.
	Jailer string
	UID    int
	GID    int
}

// Limits are server-enforced ceilings. Zero values are replaced by Default.
type Limits struct {
	DefaultTTLSeconds  int
	MaxTTLSeconds      int
	DefaultCPU         float64
	DefaultMemoryMB    int
	DefaultDiskMB      int
	DefaultPIDs        int
	MaxCPU             float64
	MaxMemoryMB        int
	MaxDiskMB          int
	MaxPIDs            int
	MaxSandboxesPerKey int
	// MaxSandboxes is the node-wide cap across every API key. 0 means no cap.
	MaxSandboxes int
	// RequestsPerMinute is the per-key request budget. 0 disables the limiter.
	RequestsPerMinute      int
	DefaultExecSeconds     int
	MaxExecSeconds         int
	MaxOutputBytes         int
	MaxUploadBytes         int
	ReaperIntervalSeconds  int
	RetainStoppedSeconds   int
	CreatingTimeoutSeconds int
}

// BuildConfig controls template image builds. A build runs its steps as root
// in Docker's builder, with the daemon's default network, so only admin keys
// may request one unless AllowSandboxKeys is set.
type BuildConfig struct {
	AllowSandboxKeys bool
	TimeoutSeconds   int
}

// AuthConfig optionally pins the first API key.
type AuthConfig struct {
	BootstrapKey string
}

// NetworkConfig is the operator network policy. Sandbox and API-key settings
// can tighten it. They cannot open metadata or private ranges unless the
// operator turns those blocks off or lists a private CIDR in PrivateAllow.
type NetworkConfig struct {
	DefaultMode         string
	BlockPrivate        bool
	BlockMetadata       bool
	PrivateAllow        []string
	DNS                 []string
	IPv6                string
	EgressAllow         []string
	EgressDeny          []string
	DefaultEgressKbps   int
	DefaultMaxConns     int
	PreviewDomain       string
	PublicURL           string
	AllowPublicPreviews bool
	// TrustForwardedHost routes preview hosts by X-Forwarded-Host. Turn it
	// on only behind a proxy that sets or strips that header.
	TrustForwardedHost bool
	PreviewMaxStreams  int
}

// Default is the secure single-node configuration.
func Default() Config {
	return Config{
		Addr:     "127.0.0.1:8080",
		DataDir:  "data",
		Backend:  "docker",
		LogLevel: "info",
		Docker: DockerConfig{
			Host:           "/var/run/docker.sock",
			GVisorRuntime:  "runsc",
			ReadOnlyRootfs: true,
			SandboxUID:     1000,
			SandboxGID:     1000,
		},
		Kata: KataConfig{
			Runtime:     "kata",
			RuntimeType: "io.containerd.kata.v2",
		},
		Firecracker: FirecrackerConfig{
			UID: 123,
			GID: 123,
		},
		Limits: Limits{
			DefaultTTLSeconds:      300,
			MaxTTLSeconds:          86400,
			DefaultCPU:             1,
			DefaultMemoryMB:        512,
			DefaultDiskMB:          1024,
			DefaultPIDs:            256,
			MaxCPU:                 4,
			MaxMemoryMB:            8192,
			MaxDiskMB:              10240,
			MaxPIDs:                4096,
			MaxSandboxesPerKey:     20,
			MaxSandboxes:           100,
			RequestsPerMinute:      1200,
			DefaultExecSeconds:     60,
			MaxExecSeconds:         3600,
			MaxOutputBytes:         8 << 20,
			MaxUploadBytes:         32 << 20,
			ReaperIntervalSeconds:  5,
			RetainStoppedSeconds:   86400,
			CreatingTimeoutSeconds: 900,
		},
		Network: NetworkConfig{
			DefaultMode:         "none",
			BlockPrivate:        true,
			BlockMetadata:       true,
			DNS:                 []string{"1.1.1.1", "8.8.8.8"},
			IPv6:                "auto",
			AllowPublicPreviews: true,
			PreviewMaxStreams:   64,
		},
		Builds: BuildConfig{
			TimeoutSeconds: 1200,
		},
	}
}

// DBPath is the SQLite file inside the data directory.
func (c Config) DBPath() string {
	return strings.TrimRight(c.DataDir, "/") + "/sbx.db"
}

// KeyPath is where the one-time generated API key is written.
func (c Config) KeyPath() string {
	return strings.TrimRight(c.DataDir, "/") + "/initial_api_key"
}

type fileConfig struct {
	Addr        *string          `yaml:"addr"`
	DataDir     *string          `yaml:"data_dir"`
	Backend     *string          `yaml:"backend"`
	LogLevel    *string          `yaml:"log_level"`
	Docker      *fileDocker      `yaml:"docker"`
	Kata        *fileKata        `yaml:"kata"`
	Firecracker *fileFirecracker `yaml:"firecracker"`
	Limits      *fileLimits      `yaml:"limits"`
	Auth        *fileAuth        `yaml:"auth"`
	Network     *fileNetwork     `yaml:"network"`
	Builds      *fileBuilds      `yaml:"builds"`
}

type fileBuilds struct {
	AllowSandboxKeys *bool `yaml:"allow_sandbox_keys"`
	TimeoutSeconds   *int  `yaml:"timeout_seconds"`
}

type fileDocker struct {
	Host           *string `yaml:"host"`
	GVisorRuntime  *string `yaml:"gvisor_runtime"`
	ReadOnlyRootfs *bool   `yaml:"read_only_rootfs"`
	SandboxUID     *int    `yaml:"sandbox_uid"`
	SandboxGID     *int    `yaml:"sandbox_gid"`
	AgentBin       *string `yaml:"agent_bin"`
	SandboxRoot    *bool   `yaml:"sandbox_root"`
}

type fileKata struct {
	Runtime     *string `yaml:"runtime"`
	RuntimeType *string `yaml:"runtime_type"`
}

type fileFirecracker struct {
	Bin    *string `yaml:"bin"`
	Kernel *string `yaml:"kernel"`
	Jailer *string `yaml:"jailer"`
	UID    *int    `yaml:"uid"`
	GID    *int    `yaml:"gid"`
}

type fileLimits struct {
	DefaultTTLSeconds      *int     `yaml:"default_ttl_seconds"`
	MaxTTLSeconds          *int     `yaml:"max_ttl_seconds"`
	DefaultCPU             *float64 `yaml:"default_cpu"`
	DefaultMemoryMB        *int     `yaml:"default_memory_mb"`
	DefaultDiskMB          *int     `yaml:"default_disk_mb"`
	DefaultPIDs            *int     `yaml:"default_pids"`
	MaxCPU                 *float64 `yaml:"max_cpu"`
	MaxMemoryMB            *int     `yaml:"max_memory_mb"`
	MaxDiskMB              *int     `yaml:"max_disk_mb"`
	MaxPIDs                *int     `yaml:"max_pids"`
	MaxSandboxesPerKey     *int     `yaml:"max_sandboxes_per_key"`
	MaxSandboxes           *int     `yaml:"max_sandboxes"`
	RequestsPerMinute      *int     `yaml:"requests_per_minute"`
	DefaultExecSeconds     *int     `yaml:"default_exec_seconds"`
	MaxExecSeconds         *int     `yaml:"max_exec_seconds"`
	MaxOutputBytes         *int     `yaml:"max_output_bytes"`
	MaxUploadBytes         *int     `yaml:"max_upload_bytes"`
	ReaperIntervalSeconds  *int     `yaml:"reaper_interval_seconds"`
	RetainStoppedSeconds   *int     `yaml:"retain_stopped_seconds"`
	CreatingTimeoutSeconds *int     `yaml:"creating_timeout_seconds"`
}

type fileAuth struct {
	BootstrapKey *string `yaml:"bootstrap_key"`
}

type fileNetwork struct {
	DefaultMode         *string  `yaml:"default_mode"`
	BlockPrivate        *bool    `yaml:"block_private"`
	BlockMetadata       *bool    `yaml:"block_metadata"`
	PrivateAllow        []string `yaml:"private_allow"`
	DNS                 []string `yaml:"dns"`
	IPv6                *string  `yaml:"ipv6"`
	EgressAllow         []string `yaml:"egress_allow"`
	EgressDeny          []string `yaml:"egress_deny"`
	DefaultEgressKbps   *int     `yaml:"default_egress_kbps"`
	DefaultMaxConns     *int     `yaml:"default_max_connections"`
	PreviewDomain       *string  `yaml:"preview_domain"`
	PublicURL           *string  `yaml:"public_url"`
	AllowPublicPreviews *bool    `yaml:"allow_public_previews"`
	TrustForwardedHost  *bool    `yaml:"trust_forwarded_host"`
	PreviewMaxStreams   *int     `yaml:"preview_max_streams"`
}

// Load resolves configuration. An empty path skips the file. Environment
// variables always override the file. Flags are applied by the caller.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		var file fileConfig
		if err := yaml.Unmarshal(raw, &file); err != nil {
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
		file.apply(&cfg)
	}
	applyEnv(&cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (f fileConfig) apply(cfg *Config) {
	setString(&cfg.Addr, f.Addr)
	setString(&cfg.DataDir, f.DataDir)
	setString(&cfg.Backend, f.Backend)
	setString(&cfg.LogLevel, f.LogLevel)
	if d := f.Docker; d != nil {
		setString(&cfg.Docker.Host, d.Host)
		setString(&cfg.Docker.GVisorRuntime, d.GVisorRuntime)
		setBool(&cfg.Docker.ReadOnlyRootfs, d.ReadOnlyRootfs)
		setInt(&cfg.Docker.SandboxUID, d.SandboxUID)
		setInt(&cfg.Docker.SandboxGID, d.SandboxGID)
		setString(&cfg.Docker.AgentBin, d.AgentBin)
		setBool(&cfg.Docker.SandboxRoot, d.SandboxRoot)
	}
	if k := f.Kata; k != nil {
		setString(&cfg.Kata.Runtime, k.Runtime)
		setString(&cfg.Kata.RuntimeType, k.RuntimeType)
	}
	if fc := f.Firecracker; fc != nil {
		setString(&cfg.Firecracker.Bin, fc.Bin)
		setString(&cfg.Firecracker.Kernel, fc.Kernel)
		setString(&cfg.Firecracker.Jailer, fc.Jailer)
		setInt(&cfg.Firecracker.UID, fc.UID)
		setInt(&cfg.Firecracker.GID, fc.GID)
	}
	if l := f.Limits; l != nil {
		setInt(&cfg.Limits.DefaultTTLSeconds, l.DefaultTTLSeconds)
		setInt(&cfg.Limits.MaxTTLSeconds, l.MaxTTLSeconds)
		setFloat(&cfg.Limits.DefaultCPU, l.DefaultCPU)
		setInt(&cfg.Limits.DefaultMemoryMB, l.DefaultMemoryMB)
		setInt(&cfg.Limits.DefaultDiskMB, l.DefaultDiskMB)
		setInt(&cfg.Limits.DefaultPIDs, l.DefaultPIDs)
		setFloat(&cfg.Limits.MaxCPU, l.MaxCPU)
		setInt(&cfg.Limits.MaxMemoryMB, l.MaxMemoryMB)
		setInt(&cfg.Limits.MaxDiskMB, l.MaxDiskMB)
		setInt(&cfg.Limits.MaxPIDs, l.MaxPIDs)
		setInt(&cfg.Limits.MaxSandboxesPerKey, l.MaxSandboxesPerKey)
		setInt(&cfg.Limits.MaxSandboxes, l.MaxSandboxes)
		setInt(&cfg.Limits.RequestsPerMinute, l.RequestsPerMinute)
		setInt(&cfg.Limits.DefaultExecSeconds, l.DefaultExecSeconds)
		setInt(&cfg.Limits.MaxExecSeconds, l.MaxExecSeconds)
		setInt(&cfg.Limits.MaxOutputBytes, l.MaxOutputBytes)
		setInt(&cfg.Limits.MaxUploadBytes, l.MaxUploadBytes)
		setInt(&cfg.Limits.ReaperIntervalSeconds, l.ReaperIntervalSeconds)
		setInt(&cfg.Limits.RetainStoppedSeconds, l.RetainStoppedSeconds)
		setInt(&cfg.Limits.CreatingTimeoutSeconds, l.CreatingTimeoutSeconds)
	}
	if b := f.Builds; b != nil {
		setBool(&cfg.Builds.AllowSandboxKeys, b.AllowSandboxKeys)
		setInt(&cfg.Builds.TimeoutSeconds, b.TimeoutSeconds)
	}
	if a := f.Auth; a != nil {
		setString(&cfg.Auth.BootstrapKey, a.BootstrapKey)
	}
	if n := f.Network; n != nil {
		setString(&cfg.Network.DefaultMode, n.DefaultMode)
		setBool(&cfg.Network.BlockPrivate, n.BlockPrivate)
		setBool(&cfg.Network.BlockMetadata, n.BlockMetadata)
		if n.PrivateAllow != nil {
			cfg.Network.PrivateAllow = n.PrivateAllow
		}
		if n.DNS != nil {
			cfg.Network.DNS = n.DNS
		}
		setString(&cfg.Network.IPv6, n.IPv6)
		if n.EgressAllow != nil {
			cfg.Network.EgressAllow = n.EgressAllow
		}
		if n.EgressDeny != nil {
			cfg.Network.EgressDeny = n.EgressDeny
		}
		setInt(&cfg.Network.DefaultEgressKbps, n.DefaultEgressKbps)
		setInt(&cfg.Network.DefaultMaxConns, n.DefaultMaxConns)
		setString(&cfg.Network.PreviewDomain, n.PreviewDomain)
		setString(&cfg.Network.PublicURL, n.PublicURL)
		setBool(&cfg.Network.AllowPublicPreviews, n.AllowPublicPreviews)
		setBool(&cfg.Network.TrustForwardedHost, n.TrustForwardedHost)
		setInt(&cfg.Network.PreviewMaxStreams, n.PreviewMaxStreams)
	}
}

func applyEnv(cfg *Config) {
	envString(&cfg.Addr, "SBX_ADDR")
	envString(&cfg.DataDir, "SBX_DATA_DIR")
	envString(&cfg.Backend, "SBX_BACKEND")
	envString(&cfg.LogLevel, "SBX_LOG_LEVEL")
	envString(&cfg.Docker.Host, "SBX_DOCKER_HOST")
	envString(&cfg.Docker.GVisorRuntime, "SBX_GVISOR_RUNTIME")
	envBool(&cfg.Docker.ReadOnlyRootfs, "SBX_READ_ONLY_ROOTFS")
	envInt(&cfg.Docker.SandboxUID, "SBX_SANDBOX_UID")
	envInt(&cfg.Docker.SandboxGID, "SBX_SANDBOX_GID")
	envString(&cfg.Docker.AgentBin, "SBX_AGENT_BIN")
	envBool(&cfg.Docker.SandboxRoot, "SBX_SANDBOX_ROOT")
	envString(&cfg.Kata.Runtime, "SBX_KATA_RUNTIME")
	envString(&cfg.Kata.RuntimeType, "SBX_KATA_RUNTIME_TYPE")
	envString(&cfg.Firecracker.Bin, "SBX_FIRECRACKER_BIN")
	envString(&cfg.Firecracker.Kernel, "SBX_FIRECRACKER_KERNEL")
	envString(&cfg.Firecracker.Jailer, "SBX_FIRECRACKER_JAILER")
	envInt(&cfg.Firecracker.UID, "SBX_FIRECRACKER_UID")
	envInt(&cfg.Firecracker.GID, "SBX_FIRECRACKER_GID")
	envString(&cfg.Auth.BootstrapKey, "SBX_BOOTSTRAP_KEY")
	envInt(&cfg.Limits.DefaultTTLSeconds, "SBX_DEFAULT_TTL_SECONDS")
	envInt(&cfg.Limits.MaxTTLSeconds, "SBX_MAX_TTL_SECONDS")
	envInt(&cfg.Limits.MaxSandboxesPerKey, "SBX_MAX_SANDBOXES_PER_KEY")
	envInt(&cfg.Limits.MaxSandboxes, "SBX_MAX_SANDBOXES")
	envInt(&cfg.Limits.RequestsPerMinute, "SBX_REQUESTS_PER_MINUTE")
	envInt(&cfg.Limits.DefaultMemoryMB, "SBX_DEFAULT_MEMORY_MB")
	envInt(&cfg.Limits.MaxUploadBytes, "SBX_MAX_UPLOAD_BYTES")
	envString(&cfg.Network.DefaultMode, "SBX_NETWORK_MODE")
	envBool(&cfg.Network.BlockPrivate, "SBX_BLOCK_PRIVATE")
	envBool(&cfg.Network.BlockMetadata, "SBX_BLOCK_METADATA")
	envCSV(&cfg.Network.PrivateAllow, "SBX_PRIVATE_ALLOW")
	envCSV(&cfg.Network.DNS, "SBX_DNS")
	envString(&cfg.Network.IPv6, "SBX_IPV6")
	envCSV(&cfg.Network.EgressAllow, "SBX_EGRESS_ALLOW")
	envCSV(&cfg.Network.EgressDeny, "SBX_EGRESS_DENY")
	envInt(&cfg.Network.DefaultEgressKbps, "SBX_DEFAULT_EGRESS_KBPS")
	envInt(&cfg.Network.DefaultMaxConns, "SBX_DEFAULT_MAX_CONNECTIONS")
	envString(&cfg.Network.PreviewDomain, "SBX_PREVIEW_DOMAIN")
	envString(&cfg.Network.PublicURL, "SBX_PUBLIC_URL")
	envBool(&cfg.Network.AllowPublicPreviews, "SBX_ALLOW_PUBLIC_PREVIEWS")
	envBool(&cfg.Network.TrustForwardedHost, "SBX_TRUST_FORWARDED_HOST")
	envInt(&cfg.Network.PreviewMaxStreams, "SBX_PREVIEW_MAX_STREAMS")
	envBool(&cfg.Builds.AllowSandboxKeys, "SBX_BUILDS_ALLOW_SANDBOX_KEYS")
	envInt(&cfg.Builds.TimeoutSeconds, "SBX_BUILD_TIMEOUT_SECONDS")
}

// Validate checks the resolved configuration.
func (c Config) Validate() error {
	switch c.Backend {
	case "docker", "gvisor", "kata", "firecracker":
	default:
		return fmt.Errorf("backend must be docker, gvisor, kata, or firecracker, got %q", c.Backend)
	}
	if c.Firecracker.UID < 0 || c.Firecracker.GID < 0 {
		return fmt.Errorf("firecracker uid and gid must be >= 0")
	}
	if c.Firecracker.Jailer != "" && (c.Firecracker.UID < 1 || c.Firecracker.GID < 1) {
		return fmt.Errorf("firecracker jailer requires uid and gid >= 1")
	}
	if c.Kata.Runtime == "" || c.Kata.RuntimeType == "" {
		return fmt.Errorf("kata runtime and runtime_type are required")
	}
	if c.Addr == "" {
		return fmt.Errorf("addr is required")
	}
	if c.DataDir == "" {
		return fmt.Errorf("data_dir is required")
	}
	if c.Docker.SandboxUID < 1 || c.Docker.SandboxGID < 1 {
		return fmt.Errorf("sandbox uid and gid must be >= 1")
	}
	if c.Limits.DefaultTTLSeconds < 1 || c.Limits.DefaultTTLSeconds > c.Limits.MaxTTLSeconds {
		return fmt.Errorf("default_ttl_seconds must be between 1 and max_ttl_seconds")
	}
	if c.Limits.MaxSandboxesPerKey < 1 {
		return fmt.Errorf("max_sandboxes_per_key must be >= 1")
	}
	if c.Limits.MaxSandboxes < 0 || c.Limits.RequestsPerMinute < 0 {
		return fmt.Errorf("max_sandboxes and requests_per_minute must be >= 0")
	}
	if c.Limits.DefaultCPU <= 0 || c.Limits.DefaultCPU > c.Limits.MaxCPU {
		return fmt.Errorf("default_cpu must be between 0 and max_cpu")
	}
	if c.Limits.DefaultMemoryMB < 16 || c.Limits.DefaultMemoryMB > c.Limits.MaxMemoryMB {
		return fmt.Errorf("default_memory_mb must be between 16 and max_memory_mb")
	}
	if c.Limits.DefaultDiskMB < 16 || c.Limits.DefaultDiskMB > c.Limits.MaxDiskMB {
		return fmt.Errorf("default_disk_mb out of range")
	}
	if c.Limits.DefaultPIDs < 8 || c.Limits.DefaultPIDs > c.Limits.MaxPIDs {
		return fmt.Errorf("default_pids out of range")
	}
	if c.Limits.DefaultExecSeconds < 1 || c.Limits.DefaultExecSeconds > c.Limits.MaxExecSeconds {
		return fmt.Errorf("default_exec_seconds out of range")
	}
	if c.Limits.MaxOutputBytes < 1024 || c.Limits.MaxUploadBytes < 1024 {
		return fmt.Errorf("output and upload limits must be at least 1024 bytes")
	}
	if c.Limits.ReaperIntervalSeconds < 1 {
		return fmt.Errorf("reaper_interval_seconds must be >= 1")
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log_level must be debug, info, warn, or error")
	}
	switch c.Network.DefaultMode {
	case "none", "full", "allow":
	default:
		return fmt.Errorf("network.default_mode must be none, full, or allow")
	}
	switch c.Network.IPv6 {
	case "auto", "on", "off":
	default:
		return fmt.Errorf("network.ipv6 must be auto, on, or off")
	}
	if c.Network.DefaultMode == "allow" && len(c.Network.EgressAllow) == 0 {
		return fmt.Errorf("network.default_mode allow requires network.egress_allow")
	}
	if c.Network.DefaultEgressKbps < 0 || c.Network.DefaultMaxConns < 0 {
		return fmt.Errorf("network limits must be >= 0")
	}
	if c.Builds.TimeoutSeconds < 1 {
		return fmt.Errorf("builds.timeout_seconds must be >= 1")
	}
	if c.Network.PreviewMaxStreams < 1 {
		return fmt.Errorf("preview_max_streams must be >= 1")
	}
	if err := validateRules("dns", c.Network.DNS, true); err != nil {
		return err
	}
	if err := validateRules("private_allow", c.Network.PrivateAllow, true); err != nil {
		return err
	}
	if err := validateRules("egress_allow", c.Network.EgressAllow, false); err != nil {
		return err
	}
	if err := validateRules("egress_deny", c.Network.EgressDeny, false); err != nil {
		return err
	}
	return nil
}

func validateRules(name string, items []string, cidrOnly bool) error {
	for _, item := range items {
		if cidrOnly {
			if _, err := parseIPOrPrefix(item); err != nil {
				return fmt.Errorf("%s: %s", name, err.Error())
			}
			continue
		}
		if _, err := parseRule(item); err != nil {
			return fmt.Errorf("%s: %s", name, err.Error())
		}
	}
	return nil
}

func parseIPOrPrefix(item string) (string, error) {
	if _, err := netip.ParseAddr(item); err == nil {
		return item, nil
	}
	if _, err := netip.ParsePrefix(item); err == nil {
		return item, nil
	}
	return "", fmt.Errorf("%q must be an IP or CIDR", item)
}

func parseRule(item string) (string, error) {
	if _, err := parseIPOrPrefix(item); err == nil {
		return item, nil
	}
	if strings.HasPrefix(item, "*.") {
		return "", fmt.Errorf("%q is a wildcard; the packet filter needs each hostname", item)
	}
	name := item
	if name == "" || strings.ContainsAny(name, " /\\") || strings.Contains(name, "://") {
		return "", fmt.Errorf("%q must be a domain, IP, or CIDR", item)
	}
	return item, nil
}

func setString(dst *string, src *string) {
	if src != nil {
		*dst = *src
	}
}

func setInt(dst *int, src *int) {
	if src != nil {
		*dst = *src
	}
}

func setFloat(dst *float64, src *float64) {
	if src != nil {
		*dst = *src
	}
}

func setBool(dst *bool, src *bool) {
	if src != nil {
		*dst = *src
	}
}

func envString(dst *string, key string) {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		*dst = v
	}
}

func envInt(dst *int, key string) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return
	}
	*dst = n
}

func envCSV(dst *[]string, key string) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	*dst = out
}

func envBool(dst *bool, key string) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return
	}
	*dst = b
}
