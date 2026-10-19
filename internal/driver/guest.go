package driver

import (
	"context"
	"io"
	"time"
)

// Process is a background command tracked by the in-sandbox agent.
type Process struct {
	ID        string
	PID       int
	Cmd       []string
	Cwd       string
	TTY       bool
	Status    string
	ExitCode  int
	StartedAt time.Time
}

// FileEvent is a filesystem watch notification.
type FileEvent struct {
	Type string
	Path string
}

// Guest is the optional in-sandbox agent surface. Drivers that inject
// sbx-agent implement it. GuestEnabled is false when the binary was not
// configured; the exec and file methods then keep their engine-API behavior.
type Guest interface {
	GuestEnabled() bool
	StatFile(ctx context.Context, id, path string) (FileEntry, error)
	Mkdir(ctx context.Context, id, path string, mode uint32) error
	Watch(ctx context.Context, id, path string, recursive bool, emit func(FileEvent) error) error
	StartProcess(ctx context.Context, id string, req ExecRequest, tty bool, cols, rows int) (Process, error)
	ListProcesses(ctx context.Context, id string) ([]Process, error)
	SignalProcess(ctx context.Context, id, procID, signal string) error
	WriteProcessStdin(ctx context.Context, id, procID string, data []byte) error
	StreamProcess(ctx context.Context, id, procID string, emit func(ExecChunk) error) (ExecResult, error)
	ResizeProcess(ctx context.Context, id, procID string, cols, rows int) error
	DialPort(ctx context.Context, id string, port int) (io.ReadWriteCloser, error)
}

// Archiver packs and extracts tar archives inside the sandbox. NativeArchive
// is false when the driver has no in-sandbox agent; callers then use a tar
// binary in the image.
type Archiver interface {
	NativeArchive() bool
	ExtractArchive(ctx context.Context, id, dest string, r io.Reader, max int64) error
	PackArchive(ctx context.Context, id, dir string, max int) ([]byte, error)
}
