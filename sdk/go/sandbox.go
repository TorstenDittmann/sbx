package sbx

import (
	"context"
	"io"
	"io/fs"
	"iter"
	"net/http"
	"time"
)

// Sandbox is a handle for one sandbox. The embedded SandboxInfo is a
// snapshot from the last call that returned it; Refresh reloads it. Calls
// are safe to make concurrently, but calls that update SandboxInfo (Refresh,
// Kill, Pause, Resume, SetTTL, SetSecrets) should not race with readers of
// its fields.
type Sandbox struct {
	SandboxInfo
	// Files wraps the file routes for this sandbox.
	Files  *Files
	client *Client
}

func (c *Client) newSandbox(info SandboxInfo) *Sandbox {
	return &Sandbox{SandboxInfo: info, Files: &Files{client: c, id: info.ID}, client: c}
}

// Client returns the client the sandbox uses.
func (s *Sandbox) Client() *Client { return s.client }

func (s *Sandbox) apply(info *SandboxInfo, err error) (*SandboxInfo, error) {
	if err != nil {
		return nil, err
	}
	s.SandboxInfo = *info
	return info, nil
}

// Refresh reloads the sandbox's info.
func (s *Sandbox) Refresh(ctx context.Context) (*SandboxInfo, error) {
	return s.apply(s.client.sandboxInfo(ctx, s.ID))
}

// Kill stops and removes the sandbox.
func (s *Sandbox) Kill(ctx context.Context) (*SandboxInfo, error) {
	return s.apply(s.client.DeleteSandbox(ctx, s.ID))
}

// Pause pauses the sandbox.
func (s *Sandbox) Pause(ctx context.Context) (*SandboxInfo, error) {
	return s.apply(s.client.Pause(ctx, s.ID))
}

// Resume resumes the sandbox.
func (s *Sandbox) Resume(ctx context.Context) (*SandboxInfo, error) {
	return s.apply(s.client.Resume(ctx, s.ID))
}

// SetTTL sets the remaining lifetime.
func (s *Sandbox) SetTTL(ctx context.Context, ttl time.Duration) (*SandboxInfo, error) {
	return s.apply(s.client.SetTTL(ctx, s.ID, ttl))
}

// SetSecrets replaces the sandbox's secrets.
func (s *Sandbox) SetSecrets(ctx context.Context, secrets map[string]string) (*SandboxInfo, error) {
	return s.apply(s.client.SetSecrets(ctx, s.ID, secrets))
}

// Snapshot commits the sandbox to an image.
func (s *Sandbox) Snapshot(ctx context.Context, o SnapshotOptions) (*SnapshotInfo, error) {
	return s.client.Snapshot(ctx, s.ID, o)
}

// Stats samples resource usage.
func (s *Sandbox) Stats(ctx context.Context) (*SandboxStats, error) {
	return s.client.Stats(ctx, s.ID)
}

// Exec runs a command and waits for it.
func (s *Sandbox) Exec(ctx context.Context, cmd []string, opts *ExecOptions) (*ExecResult, error) {
	return s.client.Exec(ctx, s.ID, cmd, opts)
}

// ExecStream runs a command and yields its output as it arrives.
func (s *Sandbox) ExecStream(ctx context.Context, cmd []string, opts *ExecOptions) iter.Seq2[ExecEvent, error] {
	return s.client.ExecStream(ctx, s.ID, cmd, opts)
}

// Events streams lifecycle events for this sandbox.
func (s *Sandbox) Events(ctx context.Context, o EventsOptions) iter.Seq2[Event, error] {
	o.Sandbox = s.ID
	return s.client.Events(ctx, o)
}

// StartProcess starts a background process.
func (s *Sandbox) StartProcess(ctx context.Context, cmd []string, opts *ExecOptions) (*ProcessInfo, error) {
	return s.client.StartProcess(ctx, s.ID, cmd, opts)
}

// ListProcesses lists tracked processes.
func (s *Sandbox) ListProcesses(ctx context.Context) ([]ProcessInfo, error) {
	return s.client.ListProcesses(ctx, s.ID)
}

// GetProcess gets one process.
func (s *Sandbox) GetProcess(ctx context.Context, procID string) (*ProcessInfo, error) {
	return s.client.GetProcess(ctx, s.ID, procID)
}

// KillProcess kills a process.
func (s *Sandbox) KillProcess(ctx context.Context, procID string) error {
	return s.client.KillProcess(ctx, s.ID, procID)
}

// SignalProcess sends a signal to a process.
func (s *Sandbox) SignalProcess(ctx context.Context, procID string, signal Signal) error {
	return s.client.SignalProcess(ctx, s.ID, procID, signal)
}

// WriteProcessStdin writes to a process without closing its stdin.
func (s *Sandbox) WriteProcessStdin(ctx context.Context, procID string, data []byte) error {
	return s.client.WriteProcessStdin(ctx, s.ID, procID, data)
}

// StreamProcess follows a background process.
func (s *Sandbox) StreamProcess(ctx context.Context, procID string) iter.Seq2[ExecEvent, error] {
	return s.client.StreamProcess(ctx, s.ID, procID)
}

// StartTerminal starts a PTY.
func (s *Sandbox) StartTerminal(ctx context.Context, cmd []string, opts *TerminalOptions) (*TerminalInfo, error) {
	return s.client.StartTerminal(ctx, s.ID, cmd, opts)
}

// ResizeTerminal resizes a PTY.
func (s *Sandbox) ResizeTerminal(ctx context.Context, termID string, cols, rows int) error {
	return s.client.ResizeTerminal(ctx, s.ID, termID, cols, rows)
}

// KillTerminal kills a PTY's process.
func (s *Sandbox) KillTerminal(ctx context.Context, termID string) error {
	return s.client.KillTerminal(ctx, s.ID, termID)
}

// ConnectTerminal attaches to a PTY over a WebSocket.
func (s *Sandbox) ConnectTerminal(ctx context.Context, termID string) (*TerminalSession, error) {
	return s.client.ConnectTerminal(ctx, s.ID, termID)
}

// CreatePreview publishes a loopback port.
func (s *Sandbox) CreatePreview(ctx context.Context, port int, public bool) (*PreviewInfo, error) {
	return s.client.CreatePreview(ctx, s.ID, port, public)
}

// ListPreviews lists the sandbox's previews.
func (s *Sandbox) ListPreviews(ctx context.Context) ([]PreviewInfo, error) {
	return s.client.ListPreviews(ctx, s.ID)
}

// DeletePreview removes a preview.
func (s *Sandbox) DeletePreview(ctx context.Context, previewID string) error {
	return s.client.DeletePreview(ctx, s.ID, previewID)
}

// PreviewFetch sends a request through the authenticated preview proxy.
func (s *Sandbox) PreviewFetch(ctx context.Context, previewID, method, path string, body io.Reader, header http.Header) (*http.Response, error) {
	return s.client.PreviewFetch(ctx, s.ID, previewID, method, path, body, header)
}

// Files wraps the file routes for one sandbox.
type Files struct {
	client *Client
	id     string
}

// List lists a directory.
func (f *Files) List(ctx context.Context, path string) (*FileList, error) {
	return f.client.ListFiles(ctx, f.id, path)
}

// Read downloads a file.
func (f *Files) Read(ctx context.Context, path string) ([]byte, error) {
	return f.client.ReadFile(ctx, f.id, path)
}

// ReadText downloads a file as a string.
func (f *Files) ReadText(ctx context.Context, path string) (string, error) {
	data, err := f.client.ReadFile(ctx, f.id, path)
	return string(data), err
}

// Open streams a file. The caller closes the reader.
func (f *Files) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return f.client.OpenFile(ctx, f.id, path)
}

// Write uploads content. mode 0 keeps the server default.
func (f *Files) Write(ctx context.Context, path string, content []byte, mode fs.FileMode) error {
	return f.client.WriteFile(ctx, f.id, path, content, mode)
}

// WriteText uploads a string. mode 0 keeps the server default.
func (f *Files) WriteText(ctx context.Context, path, content string, mode fs.FileMode) error {
	return f.client.WriteFile(ctx, f.id, path, []byte(content), mode)
}

// WriteFrom streams r to path. mode 0 keeps the server default.
func (f *Files) WriteFrom(ctx context.Context, path string, r io.Reader, mode fs.FileMode) error {
	return f.client.WriteFileFrom(ctx, f.id, path, r, mode)
}

// Remove deletes a file or directory.
func (f *Files) Remove(ctx context.Context, path string) error {
	return f.client.RemoveFile(ctx, f.id, path)
}

// Stat returns metadata for one path.
func (f *Files) Stat(ctx context.Context, path string) (*FileEntry, error) {
	return f.client.StatFile(ctx, f.id, path)
}

// Mkdir creates a directory. mode 0 keeps the server default.
func (f *Files) Mkdir(ctx context.Context, path string, mode fs.FileMode) error {
	return f.client.Mkdir(ctx, f.id, path, mode)
}

// Watch yields filesystem changes under path.
func (f *Files) Watch(ctx context.Context, path string, recursive bool) iter.Seq2[FileEvent, error] {
	return f.client.Watch(ctx, f.id, path, recursive)
}

// UploadArchive extracts a tar or gzip tar into path.
func (f *Files) UploadArchive(ctx context.Context, path string, r io.Reader) error {
	return f.client.UploadArchive(ctx, f.id, path, r)
}

// DownloadArchive streams path as a gzip tar. The caller closes the reader.
func (f *Files) DownloadArchive(ctx context.Context, path string) (io.ReadCloser, error) {
	return f.client.DownloadArchive(ctx, f.id, path)
}
