// Package config loads sbx settings from defaults, a YAML file, environment
// variables, and process flags. Later sources win.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the resolved server configuration.
type Config struct {
	Addr     string
	DataDir  string
	Backend  string
	LogLevel string
	Docker   DockerConfig
	Limits   Limits
	Auth     AuthConfig
}

// DockerConfig controls the engine socket and sandbox hardening.
type DockerConfig struct {
	Host           string
	GVisorRuntime  string
	ReadOnlyRootfs bool
	SandboxUID     int
	SandboxGID     int
}

// Limits are server-enforced ceilings. Zero values are replaced by Default.
type Limits struct {
	DefaultTTLSeconds      int
	MaxTTLSeconds          int
	DefaultCPU             float64
	DefaultMemoryMB        int
	DefaultDiskMB          int
	DefaultPIDs            int
	MaxCPU                 float64
	MaxMemoryMB            int
	MaxDiskMB              int
	MaxPIDs                int
	MaxSandboxesPerKey     int
	DefaultExecSeconds     int
	MaxExecSeconds         int
	MaxOutputBytes         int
	MaxUploadBytes         int
	ReaperIntervalSeconds  int
	RetainStoppedSeconds   int
	CreatingTimeoutSeconds int
}

// AuthConfig optionally pins the first API key.
type AuthConfig struct {
	BootstrapKey string
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
			DefaultExecSeconds:     60,
			MaxExecSeconds:         3600,
			MaxOutputBytes:         8 << 20,
			MaxUploadBytes:         32 << 20,
			ReaperIntervalSeconds:  5,
			RetainStoppedSeconds:   86400,
			CreatingTimeoutSeconds: 900,
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
	Addr     *string     `yaml:"addr"`
	DataDir  *string     `yaml:"data_dir"`
	Backend  *string     `yaml:"backend"`
	LogLevel *string     `yaml:"log_level"`
	Docker   *fileDocker `yaml:"docker"`
	Limits   *fileLimits `yaml:"limits"`
	Auth     *fileAuth   `yaml:"auth"`
}

type fileDocker struct {
	Host           *string `yaml:"host"`
	GVisorRuntime  *string `yaml:"gvisor_runtime"`
	ReadOnlyRootfs *bool   `yaml:"read_only_rootfs"`
	SandboxUID     *int    `yaml:"sandbox_uid"`
	SandboxGID     *int    `yaml:"sandbox_gid"`
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
		setInt(&cfg.Limits.DefaultExecSeconds, l.DefaultExecSeconds)
		setInt(&cfg.Limits.MaxExecSeconds, l.MaxExecSeconds)
		setInt(&cfg.Limits.MaxOutputBytes, l.MaxOutputBytes)
		setInt(&cfg.Limits.MaxUploadBytes, l.MaxUploadBytes)
		setInt(&cfg.Limits.ReaperIntervalSeconds, l.ReaperIntervalSeconds)
		setInt(&cfg.Limits.RetainStoppedSeconds, l.RetainStoppedSeconds)
		setInt(&cfg.Limits.CreatingTimeoutSeconds, l.CreatingTimeoutSeconds)
	}
	if a := f.Auth; a != nil {
		setString(&cfg.Auth.BootstrapKey, a.BootstrapKey)
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
	envString(&cfg.Auth.BootstrapKey, "SBX_BOOTSTRAP_KEY")
	envInt(&cfg.Limits.DefaultTTLSeconds, "SBX_DEFAULT_TTL_SECONDS")
	envInt(&cfg.Limits.MaxTTLSeconds, "SBX_MAX_TTL_SECONDS")
	envInt(&cfg.Limits.MaxSandboxesPerKey, "SBX_MAX_SANDBOXES_PER_KEY")
	envInt(&cfg.Limits.DefaultMemoryMB, "SBX_DEFAULT_MEMORY_MB")
	envInt(&cfg.Limits.MaxUploadBytes, "SBX_MAX_UPLOAD_BYTES")
}

// Validate checks the resolved configuration.
func (c Config) Validate() error {
	switch c.Backend {
	case "docker", "gvisor":
	default:
		return fmt.Errorf("backend must be docker or gvisor, got %q", c.Backend)
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
	return nil
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
