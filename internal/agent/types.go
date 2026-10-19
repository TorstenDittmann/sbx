package agent

// HelloOK is the server's handshake reply.
type HelloOK struct {
	Version int      `json:"version"`
	Caps    []string `json:"caps"`
}

// ExecSpec starts a process.
type ExecSpec struct {
	Cmd            []string          `json:"cmd"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	TTY            bool              `json:"tty,omitempty"`
	Cols           int               `json:"cols,omitempty"`
	Rows           int               `json:"rows,omitempty"`
	Background     bool              `json:"background,omitempty"`
	MaxOutput      int               `json:"max_output,omitempty"`
}

// ExitResult is the final frame of an exec or attach.
type ExitResult struct {
	ExitCode   int   `json:"exit_code"`
	Truncated  bool  `json:"truncated"`
	DurationMs int64 `json:"duration_ms"`
}

// ProcessInfo describes one process the agent is tracking.
type ProcessInfo struct {
	ID        string   `json:"id"`
	PID       int      `json:"pid"`
	Cmd       []string `json:"cmd"`
	Cwd       string   `json:"cwd,omitempty"`
	TTY       bool     `json:"tty"`
	Status    string   `json:"status"`
	ExitCode  int      `json:"exit_code"`
	StartedAt int64    `json:"started_at"`
}

// FileInfo is a file or directory entry.
type FileInfo struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Type  string `json:"type"`
	Size  int64  `json:"size"`
	Mode  uint32 `json:"mode"`
	Mtime int64  `json:"mtime"`
}

// FileEvent is one watch notification.
type FileEvent struct {
	Type string `json:"type"`
	Path string `json:"path"`
}

// ExecOutput is a finished foreground command.
type ExecOutput struct {
	ExitCode  int
	Truncated bool
	Duration  int64
}
