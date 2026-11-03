package sbx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

func sandboxPath(id string, rest ...string) string {
	path := "/v1/sandboxes/" + q(id)
	for _, part := range rest {
		path += "/" + q(part)
	}
	return path
}

func filesQuery(path string) url.Values { return url.Values{"path": {path}} }

// Volumes

// CreateVolume creates a named volume that outlives sandboxes. backend may
// be empty for the server default. A name the key already uses fails with
// 409 unless ifExists is IfExistsReuse.
func (c *Client) CreateVolume(ctx context.Context, name, backend string, ifExists IfExists) (*VolumeInfo, error) {
	body := map[string]any{"name": name}
	if backend != "" {
		body["backend"] = backend
	}
	if ifExists != "" {
		body["if_exists"] = ifExists
	}
	var out VolumeInfo
	if err := c.doJSON(ctx, http.MethodPost, "/v1/volumes", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EnsureVolume returns the volume with this name, creating it if needed. It
// is safe to call again on retry.
func (c *Client) EnsureVolume(ctx context.Context, name, backend string) (*VolumeInfo, error) {
	return c.CreateVolume(ctx, name, backend, IfExistsReuse)
}

// ListVolumes lists the caller's volumes.
func (c *Client) ListVolumes(ctx context.Context) ([]VolumeInfo, error) {
	var out struct {
		Volumes []VolumeInfo `json:"volumes"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/volumes", nil, &out); err != nil {
		return nil, err
	}
	return out.Volumes, nil
}

// GetVolume gets one volume.
func (c *Client) GetVolume(ctx context.Context, id string) (*VolumeInfo, error) {
	var out VolumeInfo
	if err := c.doJSON(ctx, http.MethodGet, "/v1/volumes/"+q(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteVolume deletes a volume. It fails while a live sandbox mounts it.
func (c *Client) DeleteVolume(ctx context.Context, id string) error {
	return c.doJSON(ctx, http.MethodDelete, "/v1/volumes/"+q(id), nil, nil)
}

// Templates and builds

// CreateTemplate creates a named template. It needs an admin key.
func (c *Client) CreateTemplate(ctx context.Context, o CreateTemplateOptions) (*TemplateInfo, error) {
	body := map[string]any{"name": o.Name, "image": o.Image}
	if o.Description != "" {
		body["description"] = o.Description
	}
	if o.CPU != 0 {
		body["cpu"] = o.CPU
	}
	if o.MemoryMB != 0 {
		body["memory_mb"] = o.MemoryMB
	}
	if o.DiskMB != 0 {
		body["disk_mb"] = o.DiskMB
	}
	if len(o.Env) > 0 {
		body["env"] = o.Env
	}
	if o.Backend != "" {
		body["backend"] = o.Backend
	}
	if o.Network != "" {
		body["network"] = o.Network
	}
	if len(o.Setup) > 0 {
		body["setup"] = o.Setup
	}
	if o.SetupTimeout > 0 {
		body["setup_timeout_seconds"] = seconds(o.SetupTimeout)
	}
	if o.Warm != 0 {
		body["warm"] = o.Warm
	}
	if len(o.Steps) > 0 {
		body["steps"] = o.Steps
	}
	var out TemplateInfo
	if err := c.doJSON(ctx, http.MethodPost, "/v1/templates", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListTemplates lists built-in and saved templates.
func (c *Client) ListTemplates(ctx context.Context) ([]TemplateInfo, error) {
	var out struct {
		Templates []TemplateInfo `json:"templates"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/templates", nil, &out); err != nil {
		return nil, err
	}
	return out.Templates, nil
}

// DeleteTemplate deletes a saved template.
func (c *Client) DeleteTemplate(ctx context.Context, name string) error {
	return c.doJSON(ctx, http.MethodDelete, "/v1/templates/"+q(name), nil, nil)
}

// Build builds and caches a Template's image ahead of time. CreateSandbox
// builds on its own, so this only warms the cache. backend may be empty.
func (c *Client) Build(ctx context.Context, t *Template, backend string) (*BuildInfo, error) {
	if t == nil {
		return nil, errors.New("sbx: template must not be nil")
	}
	if t.err != nil {
		return nil, t.err
	}
	body := map[string]any{"steps": t.Steps()}
	if t.image != "" {
		body["image"] = t.image
	}
	if t.template != "" {
		body["template"] = t.template
	}
	if backend != "" {
		body["backend"] = backend
	}
	var out BuildInfo
	if err := c.doJSON(ctx, http.MethodPost, "/v1/builds", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Keys

// CreateKey creates an API key. It needs an admin key.
func (c *Client) CreateKey(ctx context.Context, name string, o CreateKeyOptions) (*CreatedKey, error) {
	body := map[string]any{"name": name}
	if len(o.Scopes) > 0 {
		body["scopes"] = o.Scopes
	}
	if o.Network != nil {
		body["network"] = *o.Network
	}
	if !o.ExpiresAt.IsZero() {
		body["expires_at"] = o.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	if o.Registries != nil {
		body["registries"] = o.Registries
	}
	var out CreatedKey
	if err := c.doJSON(ctx, http.MethodPost, "/v1/keys", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListKeys lists API keys without their secrets.
func (c *Client) ListKeys(ctx context.Context) ([]KeyInfo, error) {
	var out struct {
		Keys []KeyInfo `json:"keys"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/keys", nil, &out); err != nil {
		return nil, err
	}
	return out.Keys, nil
}

// GetKey gets one API key.
func (c *Client) GetKey(ctx context.Context, id string) (*KeyInfo, error) {
	var out KeyInfo
	if err := c.doJSON(ctx, http.MethodGet, "/v1/keys/"+q(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetKeyRegistries replaces a key's private registry credentials. An empty
// list removes them.
func (c *Client) SetKeyRegistries(ctx context.Context, id string, registries []RegistryCredential) (*KeyInfo, error) {
	if registries == nil {
		registries = []RegistryCredential{}
	}
	var out KeyInfo
	if err := c.doJSON(ctx, http.MethodPut, "/v1/keys/"+q(id)+"/registries", map[string]any{"registries": registries}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeKey revokes a key. It stops authenticating immediately.
func (c *Client) RevokeKey(ctx context.Context, id string) error {
	return c.doJSON(ctx, http.MethodDelete, "/v1/keys/"+q(id), nil, nil)
}

// ListBackends lists the server's backends and what each supports.
func (c *Client) ListBackends(ctx context.Context) ([]BackendInfo, error) {
	var out struct {
		Backends []BackendInfo `json:"backends"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/backends", nil, &out); err != nil {
		return nil, err
	}
	return out.Backends, nil
}

// Sandboxes

// CreateSandbox creates a sandbox and returns a handle for it.
func (c *Client) CreateSandbox(ctx context.Context, o CreateSandboxOptions) (*Sandbox, error) {
	if o.From != nil && o.From.err != nil {
		return nil, o.From.err
	}
	var out SandboxInfo
	if err := c.doJSON(ctx, http.MethodPost, "/v1/sandboxes", createBody(o), &out); err != nil {
		return nil, err
	}
	return c.newSandbox(out), nil
}

// GetSandbox returns a handle for an existing sandbox.
func (c *Client) GetSandbox(ctx context.Context, id string) (*Sandbox, error) {
	info, err := c.sandboxInfo(ctx, id)
	if err != nil {
		return nil, err
	}
	return c.newSandbox(*info), nil
}

// Sandbox returns a handle for id without a request. Its info fields are
// empty until Refresh.
func (c *Client) Sandbox(id string) *Sandbox {
	return c.newSandbox(SandboxInfo{ID: id})
}

func (c *Client) sandboxInfo(ctx context.Context, id string) (*SandboxInfo, error) {
	var out SandboxInfo
	if err := c.doJSON(ctx, http.MethodGet, sandboxPath(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListSandboxes lists the caller's sandboxes. Warm-pool sandboxes are left
// out unless an admin asks for StatusWarm.
func (c *Client) ListSandboxes(ctx context.Context, o ListSandboxesOptions) ([]*Sandbox, error) {
	values := url.Values{}
	if o.Status != "" {
		values.Set("status", string(o.Status))
	}
	if o.Backend != "" {
		values.Set("backend", o.Backend)
	}
	if o.Template != "" {
		values.Set("template", o.Template)
	}
	if o.Query != "" {
		values.Set("q", o.Query)
	}
	keys := make([]string, 0, len(o.Labels))
	for k := range o.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		values.Add("label", k+":"+o.Labels[k])
	}
	var out struct {
		Sandboxes []SandboxInfo `json:"sandboxes"`
	}
	if err := c.doJSON(ctx, http.MethodGet, withQuery("/v1/sandboxes", values), nil, &out); err != nil {
		return nil, err
	}
	boxes := make([]*Sandbox, len(out.Sandboxes))
	for i, info := range out.Sandboxes {
		boxes[i] = c.newSandbox(info)
	}
	return boxes, nil
}

func (c *Client) sandboxCall(ctx context.Context, method, path string, body any) (*SandboxInfo, error) {
	var out SandboxInfo
	if err := c.doJSON(ctx, method, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteSandbox stops and removes a sandbox and returns it as stopped.
func (c *Client) DeleteSandbox(ctx context.Context, id string) (*SandboxInfo, error) {
	return c.sandboxCall(ctx, http.MethodDelete, sandboxPath(id), nil)
}

// SetSecrets replaces a sandbox's secrets.
func (c *Client) SetSecrets(ctx context.Context, id string, secrets map[string]string) (*SandboxInfo, error) {
	if secrets == nil {
		secrets = map[string]string{}
	}
	return c.sandboxCall(ctx, http.MethodPut, sandboxPath(id, "secrets"), map[string]any{"secrets": secrets})
}

// SetTTL sets the remaining lifetime, rounded to whole seconds.
func (c *Client) SetTTL(ctx context.Context, id string, ttl time.Duration) (*SandboxInfo, error) {
	return c.sandboxCall(ctx, http.MethodPost, sandboxPath(id, "timeout"), map[string]any{"ttl_seconds": seconds(ttl)})
}

// Pause pauses a sandbox.
func (c *Client) Pause(ctx context.Context, id string) (*SandboxInfo, error) {
	return c.sandboxCall(ctx, http.MethodPost, sandboxPath(id, "pause"), nil)
}

// Resume resumes a paused sandbox.
func (c *Client) Resume(ctx context.Context, id string) (*SandboxInfo, error) {
	return c.sandboxCall(ctx, http.MethodPost, sandboxPath(id, "resume"), nil)
}

// Snapshot commits a running sandbox to an image, /workspace included, so
// later creates can start from it.
func (c *Client) Snapshot(ctx context.Context, id string, o SnapshotOptions) (*SnapshotInfo, error) {
	body := map[string]any{}
	if o.Template != "" {
		body["template"] = o.Template
	}
	if o.Description != "" {
		body["description"] = o.Description
	}
	var out SnapshotInfo
	if err := c.doJSON(ctx, http.MethodPost, sandboxPath(id, "snapshot"), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Stats samples CPU, memory, disk, network, and process count. It takes
// about a second, does not count as activity for the idle timeout, and
// fails with 409 for a stopped sandbox.
func (c *Client) Stats(ctx context.Context, id string) (*SandboxStats, error) {
	var out SandboxStats
	if err := c.doJSON(ctx, http.MethodGet, sandboxPath(id, "stats"), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Exec

// Exec runs a command and waits for it. Use Shell for a shell script.
func (c *Client) Exec(ctx context.Context, id string, cmd []string, opts *ExecOptions) (*ExecResult, error) {
	body, err := execBody(cmd, opts)
	if err != nil {
		return nil, err
	}
	var out ExecResult
	if err := c.doJSON(ctx, http.MethodPost, sandboxPath(id, "exec"), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Files

// ListFiles lists a directory.
func (c *Client) ListFiles(ctx context.Context, id, path string) (*FileList, error) {
	var out FileList
	if err := c.doJSON(ctx, http.MethodGet, withQuery(sandboxPath(id, "files"), filesQuery(path)), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReadFile downloads a file.
func (c *Client) ReadFile(ctx context.Context, id, path string) ([]byte, error) {
	return c.doBytes(ctx, http.MethodGet, withQuery(sandboxPath(id, "files", "content"), filesQuery(path)))
}

// OpenFile streams a file. The caller closes the reader.
func (c *Client) OpenFile(ctx context.Context, id, path string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, withQuery(sandboxPath(id, "files", "content"), filesQuery(path)), nil, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// WriteFile uploads content to path. mode 0 keeps the server default.
func (c *Client) WriteFile(ctx context.Context, id, path string, content []byte, mode fs.FileMode) error {
	return c.WriteFileFrom(ctx, id, path, bytes.NewReader(content), mode)
}

// WriteFileFrom streams r to path. mode 0 keeps the server default.
func (c *Client) WriteFileFrom(ctx context.Context, id, path string, r io.Reader, mode fs.FileMode) error {
	values := filesQuery(path)
	if mode != 0 {
		values.Set("mode", fileMode(mode))
	}
	resp, err := c.do(ctx, http.MethodPut, withQuery(sandboxPath(id, "files", "content"), values), r, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// RemoveFile deletes a file or directory.
func (c *Client) RemoveFile(ctx context.Context, id, path string) error {
	return c.doJSON(ctx, http.MethodDelete, withQuery(sandboxPath(id, "files"), filesQuery(path)), nil, nil)
}

// StatFile returns metadata for one path. It needs sbx-agent.
func (c *Client) StatFile(ctx context.Context, id, path string) (*FileEntry, error) {
	var out FileEntry
	if err := c.doJSON(ctx, http.MethodGet, withQuery(sandboxPath(id, "files", "stat"), filesQuery(path)), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Mkdir creates a directory. mode 0 keeps the server default.
func (c *Client) Mkdir(ctx context.Context, id, path string, mode fs.FileMode) error {
	body := map[string]any{"path": path}
	if mode != 0 {
		body["mode"] = fileMode(mode)
	}
	return c.doJSON(ctx, http.MethodPost, sandboxPath(id, "files", "mkdir"), body, nil)
}

// UploadArchive extracts a tar or gzip tar, streamed from r, into path.
// With sbx-agent, extraction does not use a tar binary.
func (c *Client) UploadArchive(ctx context.Context, id, path string, r io.Reader) error {
	resp, err := c.do(ctx, http.MethodPost, withQuery(sandboxPath(id, "files", "archive"), filesQuery(path)), r, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// DownloadArchive streams path as a gzip tar. The caller closes the reader.
func (c *Client) DownloadArchive(ctx context.Context, id, path string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, withQuery(sandboxPath(id, "files", "archive"), filesQuery(path)), nil, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func fileMode(mode fs.FileMode) string {
	bits := uint32(mode.Perm())
	if mode&fs.ModeSetuid != 0 {
		bits |= 0o4000
	}
	if mode&fs.ModeSetgid != 0 {
		bits |= 0o2000
	}
	if mode&fs.ModeSticky != 0 {
		bits |= 0o1000
	}
	return fmt.Sprintf("%o", bits)
}

// Processes

// StartProcess starts a background process. Follow it with StreamProcess.
func (c *Client) StartProcess(ctx context.Context, id string, cmd []string, opts *ExecOptions) (*ProcessInfo, error) {
	body, err := execBody(cmd, opts)
	if err != nil {
		return nil, err
	}
	var out ProcessInfo
	if err := c.doJSON(ctx, http.MethodPost, sandboxPath(id, "processes"), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListProcesses lists tracked processes.
func (c *Client) ListProcesses(ctx context.Context, id string) ([]ProcessInfo, error) {
	var out struct {
		Processes []ProcessInfo `json:"processes"`
	}
	if err := c.doJSON(ctx, http.MethodGet, sandboxPath(id, "processes"), nil, &out); err != nil {
		return nil, err
	}
	return out.Processes, nil
}

// GetProcess gets one process.
func (c *Client) GetProcess(ctx context.Context, id, procID string) (*ProcessInfo, error) {
	var out ProcessInfo
	if err := c.doJSON(ctx, http.MethodGet, sandboxPath(id, "processes", procID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// KillProcess kills a process.
func (c *Client) KillProcess(ctx context.Context, id, procID string) error {
	return c.doJSON(ctx, http.MethodDelete, sandboxPath(id, "processes", procID), nil, nil)
}

// SignalProcess sends a signal to a process.
func (c *Client) SignalProcess(ctx context.Context, id, procID string, signal Signal) error {
	return c.doJSON(ctx, http.MethodPost, sandboxPath(id, "processes", procID, "signal"), map[string]any{"signal": signal}, nil)
}

// WriteProcessStdin writes to a process without closing its stdin.
func (c *Client) WriteProcessStdin(ctx context.Context, id, procID string, data []byte) error {
	if data == nil {
		data = []byte{}
	}
	resp, err := c.do(ctx, http.MethodPost, sandboxPath(id, "processes", procID, "stdin"), data, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Terminals

// StartTerminal starts a PTY. Attach to it with ConnectTerminal.
func (c *Client) StartTerminal(ctx context.Context, id string, cmd []string, opts *TerminalOptions) (*TerminalInfo, error) {
	var exec *ExecOptions
	if opts != nil {
		exec = &opts.ExecOptions
	}
	body, err := execBody(cmd, exec)
	if err != nil {
		return nil, err
	}
	delete(body, "stdin_base64")
	if opts != nil && opts.Cols > 0 {
		body["cols"] = opts.Cols
	}
	if opts != nil && opts.Rows > 0 {
		body["rows"] = opts.Rows
	}
	var out TerminalInfo
	if err := c.doJSON(ctx, http.MethodPost, sandboxPath(id, "terminals"), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResizeTerminal resizes a PTY.
func (c *Client) ResizeTerminal(ctx context.Context, id, termID string, cols, rows int) error {
	return c.doJSON(ctx, http.MethodPost, sandboxPath(id, "terminals", termID, "resize"), map[string]any{"cols": cols, "rows": rows}, nil)
}

// KillTerminal kills a PTY's process.
func (c *Client) KillTerminal(ctx context.Context, id, termID string) error {
	return c.doJSON(ctx, http.MethodDelete, sandboxPath(id, "terminals", termID), nil, nil)
}

// Previews

// CreatePreview publishes a loopback port. A public preview needs no token.
func (c *Client) CreatePreview(ctx context.Context, id string, port int, public bool) (*PreviewInfo, error) {
	body := map[string]any{"port": port}
	if public {
		body["public"] = true
	}
	var out PreviewInfo
	if err := c.doJSON(ctx, http.MethodPost, sandboxPath(id, "previews"), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListPreviews lists a sandbox's previews.
func (c *Client) ListPreviews(ctx context.Context, id string) ([]PreviewInfo, error) {
	var out struct {
		Previews []PreviewInfo `json:"previews"`
	}
	if err := c.doJSON(ctx, http.MethodGet, sandboxPath(id, "previews"), nil, &out); err != nil {
		return nil, err
	}
	return out.Previews, nil
}

// DeletePreview removes a preview.
func (c *Client) DeletePreview(ctx context.Context, id, previewID string) error {
	return c.doJSON(ctx, http.MethodDelete, sandboxPath(id, "previews", previewID), nil, nil)
}

// PreviewFetch sends a request through the authenticated preview proxy and
// returns the upstream response as is, whatever its status. path is
// relative to the previewed port, with an optional query. The caller closes
// the body.
func (c *Client) PreviewFetch(ctx context.Context, id, previewID, method, path string, body io.Reader, header http.Header) (*http.Response, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+sandboxPath(id, "previews", previewID)+path, body)
	if err != nil {
		return nil, err
	}
	for name, values := range header {
		req.Header[name] = values
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	return c.http.Do(req)
}
