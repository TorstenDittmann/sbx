// Package driver is the sandbox backend interface.
//
// API, auth, quotas, and the TTL reaper sit above this package and do not
// care how a sandbox is actually isolated. v0.1 ships a Docker driver and a
// gVisor driver (Docker with the runsc runtime). Later backends — Kata,
// Firecracker, Kubernetes — implement the same interface. Those backends can
// satisfy exec and files by dialing an in-guest agent; the Docker driver does
// the work through the engine API because both runc and runsc already expose
// exec and a writable workspace.
package driver

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound means the backend has no such sandbox.
var ErrNotFound = errors.New("not found")

// ErrRuntimeUnavailable means the requested runtime is not registered.
var ErrRuntimeUnavailable = errors.New("runtime unavailable")

// ErrImage means the backend could not resolve or pull an image.
var ErrImage = errors.New("image unavailable")

// State is the lifecycle state reported by a driver.
type State string

const (
	StateRunning State = "running"
	StatePaused  State = "paused"
	StateStopped State = "stopped"
	StateUnknown State = "unknown"
)

// NetMode is the sandbox network policy.
type NetMode string

const (
	NetNone NetMode = "none"
	NetFull NetMode = "full"
)

// NetPolicy is the egress policy for one sandbox on a connected network.
// Allow and Deny hold domains, IPs, and CIDRs. The driver resolves domains.
type NetPolicy struct {
	Allow         []string
	Deny          []string
	BlockPrivate  bool
	BlockMetadata bool
	PrivateAllow  []string
	DNS           []string
	EgressKbps    int
	MaxConns      int
}

// Caps describes optional backend features.
type Caps struct {
	Pause          bool
	Snapshot       bool
	NetPolicy      bool
	IsolationLevel string
}

// Mount attaches a named volume at a path inside the sandbox.
type Mount struct {
	Source string
	Target string
}

// SandboxSpec is the desired sandbox. ID is assigned by sbx, not the backend.
type SandboxSpec struct {
	ID       string
	Image    string
	CPU      float64
	MemoryMB int
	DiskMB   int
	PIDs     int
	Env      map[string]string
	Network  NetMode
	Policy   NetPolicy
	Labels   map[string]string
	Workdir  string
	Mounts   []Mount
}

// VolumeOps creates and deletes named volumes that outlive a sandbox.
type VolumeOps interface {
	CreateVolume(ctx context.Context, name string) error
	DeleteVolume(ctx context.Context, name string) error
}

// Handle is returned once the sandbox is running.
type Handle struct {
	ID          string
	RuntimeName string
}

// Status is a point-in-time view of one sandbox.
type Status struct {
	ID        string
	SandboxID string
	State     State
	Image     string
}

// ExecRequest runs a process inside a sandbox. Cmd is an argv, not a shell line.
type ExecRequest struct {
	Cmd            []string
	Cwd            string
	Env            map[string]string
	TimeoutSeconds int
	Stdin          io.Reader
}

// ExecResult is a finished process. Stdout and Stderr are empty when the
// caller consumed the stream via ExecStream.
type ExecResult struct {
	ExitCode  int
	Stdout    []byte
	Stderr    []byte
	Duration  time.Duration
	Truncated bool
}

// StreamKind identifies one chunk of an exec stream.
type StreamKind string

const (
	StreamStdout StreamKind = "stdout"
	StreamStderr StreamKind = "stderr"
)

// ExecChunk is one piece of multiplexed output.
type ExecChunk struct {
	Kind StreamKind
	Data []byte
}

// FileType is a directory listing entry kind.
type FileType string

const (
	FileFile FileType = "file"
	FileDir  FileType = "dir"
	FileLink FileType = "symlink"
)

// FileEntry is one directory entry.
type FileEntry struct {
	Name    string
	Path    string
	Type    FileType
	Size    int64
	Mode    uint32
	ModTime time.Time
}

// NetEnforcer installs and refreshes per-sandbox egress rules.
type NetEnforcer interface {
	ApplyNetPolicy(ctx context.Context, containerID string, spec SandboxSpec) error
	DropNetPolicy(ctx context.Context, containerID string) error
	RefreshNetPolicies(ctx context.Context) error
}

// Driver manages sandboxes for one isolation backend.
type Driver interface {
	Name() string
	Capabilities() Caps
	Ping(ctx context.Context) error
	EnsureImage(ctx context.Context, ref string) error
	Create(ctx context.Context, spec SandboxSpec) (Handle, error)
	Delete(ctx context.Context, id string) error
	Inspect(ctx context.Context, id string) (Status, error)
	List(ctx context.Context) ([]Status, error)
	Pause(ctx context.Context, id string) error
	Resume(ctx context.Context, id string) error
	Exec(ctx context.Context, id string, req ExecRequest) (ExecResult, error)
	ExecStream(ctx context.Context, id string, req ExecRequest, emit func(ExecChunk) error) (ExecResult, error)
	ListFiles(ctx context.Context, id, path string) ([]FileEntry, error)
	ReadFile(ctx context.Context, id, path string, max int) ([]byte, error)
	WriteFile(ctx context.Context, id, path string, content []byte, mode uint32) error
	RemoveFile(ctx context.Context, id, path string) error
}
