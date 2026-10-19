package agent

import (
	"encoding/json"
	"errors"
	"os"
)

const (
	// DefaultGuestFile is the config the Firecracker init reads.
	DefaultGuestFile = "/opt/sbx/guest.json"
	// DefaultVsockPort is the guest port the host dials.
	DefaultVsockPort = 52
	// EnvGuestConfig overrides DefaultGuestFile.
	EnvGuestConfig = "SBX_GUEST_CONFIG"
)

// GuestConfig is written into the rootfs before the microVM boots.
type GuestConfig struct {
	Token     string            `json:"token"`
	IP        string            `json:"ip,omitempty"`
	Gateway   string            `json:"gateway,omitempty"`
	VsockPort int               `json:"vsock_port,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	DNS       []string          `json:"dns,omitempty"`
}

func guestPath() string {
	if p := os.Getenv(EnvGuestConfig); p != "" {
		return p
	}
	return DefaultGuestFile
}

func loadGuest() (GuestConfig, error) {
	raw, err := os.ReadFile(guestPath())
	if err != nil {
		return GuestConfig{}, err
	}
	var cfg GuestConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return GuestConfig{}, err
	}
	if cfg.VsockPort == 0 {
		cfg.VsockPort = DefaultVsockPort
	}
	return cfg, nil
}

func tokenFrom(cfg GuestConfig) (string, error) {
	if t := os.Getenv(EnvToken); t != "" {
		return t, nil
	}
	if cfg.Token != "" {
		return cfg.Token, nil
	}
	return "", errors.New("SBX_AGENT_TOKEN is required")
}

func applyGuestEnv(cfg GuestConfig) {
	for k, v := range cfg.Env {
		if k == "" || k == EnvToken {
			continue
		}
		_ = os.Setenv(k, v)
	}
}
