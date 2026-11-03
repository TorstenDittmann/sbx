package sbx

import (
	"encoding/json"
	"time"
)

// SandboxStatus is the lifecycle state of a sandbox.
type SandboxStatus string

// Sandbox statuses.
const (
	StatusCreating SandboxStatus = "creating"
	StatusRunning  SandboxStatus = "running"
	StatusPaused   SandboxStatus = "paused"
	// StatusHibernated is an idle sandbox saved to a snapshot with no
	// container. The next request, or Resume, starts it again.
	StatusHibernated SandboxStatus = "hibernated"
	StatusStopped    SandboxStatus = "stopped"
	StatusError      SandboxStatus = "error"
	StatusWarm       SandboxStatus = "warm"
)

// Backend names accepted by create, build, and volume calls.
const (
	BackendDocker      = "docker"
	BackendGVisor      = "gvisor"
	BackendKata        = "kata"
	BackendFirecracker = "firecracker"
)

// NetworkMode is a sandbox network mode.
type NetworkMode string

// Network modes. Allow is full egress limited to an allow list.
const (
	NetworkNone  NetworkMode = "none"
	NetworkFull  NetworkMode = "full"
	NetworkAllow NetworkMode = "allow"
)

// SandboxInfo is a sandbox as the API returns it.
type SandboxInfo struct {
	ID                 string            `json:"id"`
	Status             SandboxStatus     `json:"status"`
	Backend            string            `json:"backend"`
	Template           string            `json:"template,omitempty"`
	Image              string            `json:"image"`
	CPU                float64           `json:"cpu"`
	MemoryMB           int               `json:"memory_mb"`
	DiskMB             int               `json:"disk_mb"`
	PIDs               int               `json:"pids"`
	Network            string            `json:"network"`
	EgressAllow        []string          `json:"egress_allow,omitempty"`
	EgressDeny         []string          `json:"egress_deny,omitempty"`
	EgressKbps         int               `json:"egress_kbps,omitempty"`
	MaxConnections     int               `json:"max_connections,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
	Labels             map[string]string `json:"labels,omitempty"`
	SecretKeys         []string          `json:"secret_keys,omitempty"`
	IdleTimeoutSeconds int               `json:"idle_timeout_seconds,omitempty"`
	ExpiresAt          time.Time         `json:"expires_at"`
	CreatedAt          time.Time         `json:"created_at"`
	Node               string            `json:"node"`
	Mounts             []VolumeMount     `json:"mounts,omitempty"`
}

// VolumeMount attaches a volume at an absolute path inside a sandbox.
type VolumeMount struct {
	VolumeID string `json:"volume_id"`
	Path     string `json:"path"`
}

// VolumeInfo is a named volume that outlives sandboxes.
type VolumeInfo struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Backend   string    `json:"backend"`
	CreatedAt time.Time `json:"created_at"`
}

// NetworkPolicy is a sandbox's egress policy. Allow entries are hostnames,
// IPs, or CIDRs. A nil slice is omitted; an empty one clears an inherited
// list.
type NetworkPolicy struct {
	Mode           NetworkMode `json:"mode,omitempty"`
	Allow          []string    `json:"allow,omitempty"`
	Deny           []string    `json:"deny,omitempty"`
	EgressKbps     int         `json:"egress_kbps,omitempty"`
	MaxConnections int         `json:"max_connections,omitempty"`
}

// MarshalJSON keeps an empty, non-nil Allow or Deny, which clears a list.
func (p NetworkPolicy) MarshalJSON() ([]byte, error) {
	return json.Marshal(networkBody(p, ""))
}

// KeyNetworkPolicy is an API key's network policy. MaxMode makes the key's
// mode and allow list a ceiling instead of a default.
type KeyNetworkPolicy struct {
	NetworkPolicy
	MaxMode NetworkMode `json:"max_mode,omitempty"`
}

// MarshalJSON adds max_mode to the network body.
func (p KeyNetworkPolicy) MarshalJSON() ([]byte, error) {
	return json.Marshal(networkBody(p.NetworkPolicy, p.MaxMode))
}

// UnmarshalJSON reads both the shared fields and max_mode.
func (p *KeyNetworkPolicy) UnmarshalJSON(data []byte) error {
	var raw struct {
		Mode           NetworkMode `json:"mode"`
		MaxMode        NetworkMode `json:"max_mode"`
		Allow          []string    `json:"allow"`
		Deny           []string    `json:"deny"`
		EgressKbps     int         `json:"egress_kbps"`
		MaxConnections int         `json:"max_connections"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*p = KeyNetworkPolicy{
		NetworkPolicy: NetworkPolicy{
			Mode: raw.Mode, Allow: raw.Allow, Deny: raw.Deny,
			EgressKbps: raw.EgressKbps, MaxConnections: raw.MaxConnections,
		},
		MaxMode: raw.MaxMode,
	}
	return nil
}

func networkBody(p NetworkPolicy, maxMode NetworkMode) map[string]any {
	body := map[string]any{}
	if p.Mode != "" {
		body["mode"] = p.Mode
	}
	if maxMode != "" {
		body["max_mode"] = maxMode
	}
	if p.Allow != nil {
		body["allow"] = p.Allow
	}
	if p.Deny != nil {
		body["deny"] = p.Deny
	}
	if p.EgressKbps != 0 {
		body["egress_kbps"] = p.EgressKbps
	}
	if p.MaxConnections != 0 {
		body["max_connections"] = p.MaxConnections
	}
	return body
}

// BackendInfo describes one backend and what it supports.
type BackendInfo struct {
	Name         string   `json:"name"`
	Available    bool     `json:"available"`
	Detail       string   `json:"detail,omitempty"`
	Isolation    string   `json:"isolation"`
	Pause        bool     `json:"pause"`
	Snapshot     bool     `json:"snapshot"`
	Exec         bool     `json:"exec"`
	PTY          bool     `json:"pty"`
	Files        bool     `json:"files"`
	Watch        bool     `json:"watch"`
	Preview      bool     `json:"preview"`
	Networks     []string `json:"networks"`
	Requirements []string `json:"requirements"`
}

// BuildStep is one template build step. Exactly one field is set. Use the
// Template builder rather than writing steps by hand.
type BuildStep struct {
	Run      string            `json:"run,omitempty"`
	Packages []string          `json:"packages,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Workdir  string            `json:"workdir,omitempty"`
}

// BuildInfo is the result of a build. ID is empty when no step runs a
// command, and Image is then the base itself.
type BuildInfo struct {
	ID         string      `json:"id"`
	Image      string      `json:"image"`
	Base       string      `json:"base"`
	Steps      []BuildStep `json:"steps"`
	Cached     bool        `json:"cached"`
	DurationMs int64       `json:"duration_ms"`
}

// TemplateInfo is a named template.
type TemplateInfo struct {
	Name                string            `json:"name"`
	Image               string            `json:"image"`
	Description         string            `json:"description,omitempty"`
	CPU                 float64           `json:"cpu"`
	MemoryMB            int               `json:"memory_mb"`
	DiskMB              int               `json:"disk_mb"`
	Env                 map[string]string `json:"env,omitempty"`
	Backend             string            `json:"backend,omitempty"`
	Network             string            `json:"network"`
	Setup               []string          `json:"setup"`
	SetupTimeoutSeconds int               `json:"setup_timeout_seconds"`
	Warm                int               `json:"warm"`
	Steps               []BuildStep       `json:"steps"`
	Builtin             bool              `json:"builtin"`
	CreatedAt           time.Time         `json:"created_at"`
}

// SnapshotInfo is a sandbox committed to an image. ID is snap_<16 hex> and
// Image is sbx-template:<id>. Workspace is false when a volume was mounted
// at /workspace. Template is set when the snapshot was saved as a template.
type SnapshotInfo struct {
	ID         string        `json:"id"`
	Image      string        `json:"image"`
	SandboxID  string        `json:"sandbox_id"`
	Base       string        `json:"base"`
	Workspace  bool          `json:"workspace"`
	Template   *TemplateInfo `json:"template"`
	DurationMs int64         `json:"duration_ms"`
}

// SandboxStats is one resource sample. Nil pointers are values the backend
// could not read, such as disk while a sandbox is paused.
type SandboxStats struct {
	SandboxID string        `json:"sandbox_id"`
	Status    SandboxStatus `json:"status"`
	SampledAt time.Time     `json:"sampled_at"`
	CPU       struct {
		UsageSeconds float64 `json:"usage_seconds"`
		// Percent is a share of one core times 100, so 2 busy cores read 200.
		Percent    *float64 `json:"percent"`
		LimitCores float64  `json:"limit_cores"`
	} `json:"cpu"`
	Memory struct {
		UsedBytes  int64 `json:"used_bytes"`
		LimitBytes int64 `json:"limit_bytes"`
	} `json:"memory"`
	Disk struct {
		UsedBytes  *int64 `json:"used_bytes"`
		TotalBytes *int64 `json:"total_bytes"`
		LimitBytes int64  `json:"limit_bytes"`
	} `json:"disk"`
	Network struct {
		RxBytes int64 `json:"rx_bytes"`
		TxBytes int64 `json:"tx_bytes"`
	} `json:"network"`
	PIDs struct {
		Current int64 `json:"current"`
		Limit   int   `json:"limit"`
	} `json:"pids"`
}

// ExecResult is the outcome of a finished command.
type ExecResult struct {
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMs int64  `json:"duration_ms"`
	Truncated  bool   `json:"truncated"`
}

// ExecEventType is the kind of an ExecEvent.
type ExecEventType string

// Exec event types.
const (
	ExecStdout ExecEventType = "stdout"
	ExecStderr ExecEventType = "stderr"
	ExecExit   ExecEventType = "exit"
	ExecError  ExecEventType = "error"
)

// ExecEvent is one event from a streamed command. Data is set on stdout and
// stderr, ExitCode, DurationMs, and Truncated on exit, and Code and Message
// on error.
type ExecEvent struct {
	Type       ExecEventType
	Data       string
	ExitCode   int
	DurationMs int64
	Truncated  bool
	Code       string
	Message    string
}

// ProcessInfo is a background process.
type ProcessInfo struct {
	ID        string    `json:"id"`
	PID       int       `json:"pid"`
	Cmd       []string  `json:"cmd"`
	Cwd       string    `json:"cwd,omitempty"`
	TTY       bool      `json:"tty"`
	Status    string    `json:"status"`
	ExitCode  int       `json:"exit_code"`
	StartedAt time.Time `json:"started_at,omitempty"`
}

// TerminalInfo is a PTY started in a sandbox.
type TerminalInfo struct {
	ID   string `json:"id"`
	PID  int    `json:"pid"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// PreviewInfo is a published sandbox port. URL is the authenticated /v1
// proxy URL. PublicURL includes the token once, on create, when the preview
// is not public.
type PreviewInfo struct {
	ID        string    `json:"id"`
	SandboxID string    `json:"sandbox_id"`
	Port      int       `json:"port"`
	Public    bool      `json:"public"`
	URL       string    `json:"url"`
	PublicURL string    `json:"public_url,omitempty"`
	Host      string    `json:"host,omitempty"`
	Token     string    `json:"token,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// KeyInfo is an API key without its secret.
type KeyInfo struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Prefix     string           `json:"prefix"`
	Scopes     []string         `json:"scopes"`
	Network    KeyNetworkPolicy `json:"network"`
	Registries []RegistryInfo   `json:"registries"`
	CreatedAt  *time.Time       `json:"created_at,omitempty"`
	LastUsedAt *time.Time       `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time       `json:"expires_at,omitempty"`
	RevokedAt  *time.Time       `json:"revoked_at,omitempty"`
}

// RegistryInfo is a registry a key has credentials for. Secrets are never
// returned.
type RegistryInfo struct {
	Server   string `json:"server"`
	Username string `json:"username,omitempty"`
}

// RegistryCredential is a private image registry credential. Set Username
// and Password, or IdentityToken. Server is a host such as ghcr.io;
// docker.io covers unqualified image names.
type RegistryCredential struct {
	Server        string `json:"server"`
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	IdentityToken string `json:"identity_token,omitempty"`
}

// CreatedKey is a new API key. Key is the secret and is only returned once.
type CreatedKey struct {
	KeyInfo
	Key string `json:"key"`
}

// FileEntry is file metadata.
type FileEntry struct {
	Name  string    `json:"name"`
	Path  string    `json:"path"`
	Type  string    `json:"type"`
	Size  int64     `json:"size"`
	Mode  string    `json:"mode,omitempty"`
	MTime time.Time `json:"mtime,omitempty"`
}

// FileList is a directory listing.
type FileList struct {
	Path    string      `json:"path"`
	Entries []FileEntry `json:"entries"`
}

// FileEvent is a filesystem change. Type is create, write, or remove.
type FileEvent struct {
	Type string `json:"type"`
	Path string `json:"path"`
}

// EventType is a sandbox lifecycle event type.
type EventType string

// Lifecycle event types from GET /v1/events.
const (
	EventCreated EventType = "sandbox.created"
	EventFailed  EventType = "sandbox.failed"
	EventDeleted EventType = "sandbox.deleted"
	EventExpired EventType = "sandbox.expired"
	EventPaused  EventType = "sandbox.paused"
	EventResumed EventType = "sandbox.resumed"
	// EventHibernated is an idle sandbox saved to a snapshot.
	EventHibernated EventType = "sandbox.hibernated"
	// EventNetworkUpdated is a change to a running sandbox's egress rules.
	EventNetworkUpdated EventType = "sandbox.network_updated"
	EventOOM            EventType = "sandbox.oom"
	EventLost           EventType = "sandbox.lost"
)

// Event is a sandbox lifecycle event. ID only grows; pass it as
// EventsOptions.After to resume.
type Event struct {
	ID        int64     `json:"id"`
	Type      EventType `json:"type"`
	SandboxID string    `json:"sandbox_id"`
	Time      time.Time `json:"time"`
	Data      EventData `json:"data"`
}

// EventData holds the known event fields. Status is the sandbox status after
// the event. Reason is set on expired (ttl, idle), failed, and lost (exited,
// missing) events. Raw is the whole data object, unknown fields included.
type EventData struct {
	Status    SandboxStatus     `json:"status,omitempty"`
	Backend   string            `json:"backend,omitempty"`
	Template  string            `json:"template,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	Warm      bool              `json:"warm,omitempty"`
	ExitCode  *int              `json:"exit_code,omitempty"`
	OOMKilled bool              `json:"oom_killed,omitempty"`
	Raw       json.RawMessage   `json:"-"`
}

// UnmarshalJSON fills the known fields and keeps the raw object.
func (d *EventData) UnmarshalJSON(data []byte) error {
	type plain EventData
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*d = EventData(p)
	d.Raw = append(json.RawMessage(nil), data...)
	return nil
}
