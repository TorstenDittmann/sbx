package sbx

import (
	"encoding/base64"
	"errors"
	"time"
)

// NoTimeout, as ExecOptions.Timeout, sends a timeout of 0, which lets a
// background process run until it exits or the sandbox stops.
const NoTimeout time.Duration = -1

// seconds converts a duration to the whole seconds the API takes. Anything
// above 0 and below a second becomes 1.
func seconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	if d < time.Second {
		return 1
	}
	return int64(d / time.Second)
}

// Shell returns a command that runs script through sh -lc, so pipes,
// globs, and variables work.
func Shell(script string) []string {
	return []string{"sh", "-lc", script}
}

// ExecOptions apply to Exec, ExecStream, StartProcess, and StartTerminal.
type ExecOptions struct {
	Cwd string
	Env map[string]string
	// Timeout is rounded to whole seconds. 0 uses the server default and
	// NoTimeout disables it for background processes.
	Timeout time.Duration
	// Stdin is sent to the command, then stdin is closed. Terminals ignore it.
	Stdin []byte
}

func execBody(cmd []string, opts *ExecOptions) (map[string]any, error) {
	if len(cmd) == 0 {
		return nil, errors.New("sbx: cmd must not be empty")
	}
	body := map[string]any{"cmd": cmd}
	if opts == nil {
		return body, nil
	}
	if opts.Cwd != "" {
		body["cwd"] = opts.Cwd
	}
	if len(opts.Env) > 0 {
		body["env"] = opts.Env
	}
	switch {
	case opts.Timeout == NoTimeout:
		body["timeout_seconds"] = 0
	case opts.Timeout > 0:
		body["timeout_seconds"] = seconds(opts.Timeout)
	}
	if opts.Stdin != nil {
		body["stdin_base64"] = base64.StdEncoding.EncodeToString(opts.Stdin)
	}
	return body, nil
}

// CreateSandboxOptions configures a new sandbox. Zero values are omitted,
// so the template and server defaults apply.
type CreateSandboxOptions struct {
	// Template is a template name. From is a Template builder; its steps are
	// built once, then served from cache, before the create.
	Template string
	From     *Template
	// Steps run after the template's own steps and From's steps.
	Steps       []BuildStep
	Image       string
	CPU         float64
	MemoryMB    int
	DiskMB      int
	PIDs        int
	TTL         time.Duration
	IdleTimeout time.Duration
	Env         map[string]string
	Network     *NetworkPolicy
	Backend     string
	Labels      map[string]string
	Mounts      []VolumeMount
	// Secrets are injected into the sandbox and its commands but never
	// returned. Their names appear in SandboxInfo.SecretKeys.
	Secrets map[string]string
}

func createBody(o CreateSandboxOptions) map[string]any {
	body := map[string]any{}
	var steps []BuildStep
	if o.From != nil {
		if o.From.template != "" {
			body["template"] = o.From.template
		}
		if o.From.image != "" {
			body["image"] = o.From.image
		}
		steps = append(steps, o.From.steps...)
	}
	if o.Template != "" {
		body["template"] = o.Template
	}
	if o.Image != "" {
		body["image"] = o.Image
	}
	steps = append(steps, o.Steps...)
	if len(steps) > 0 {
		body["steps"] = steps
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
	if o.PIDs != 0 {
		body["pids"] = o.PIDs
	}
	if o.TTL > 0 {
		body["ttl_seconds"] = seconds(o.TTL)
	}
	if o.IdleTimeout > 0 {
		body["idle_timeout_seconds"] = seconds(o.IdleTimeout)
	}
	if len(o.Env) > 0 {
		body["env"] = o.Env
	}
	if o.Network != nil {
		body["network"] = *o.Network
	}
	if o.Backend != "" {
		body["backend"] = o.Backend
	}
	if len(o.Labels) > 0 {
		body["labels"] = o.Labels
	}
	if len(o.Mounts) > 0 {
		body["mounts"] = o.Mounts
	}
	if len(o.Secrets) > 0 {
		body["secrets"] = o.Secrets
	}
	return body
}

// ListSandboxesOptions filters ListSandboxes. Labels must all match exactly.
// Query is a case-insensitive substring of the id, template, image, status,
// or label text.
type ListSandboxesOptions struct {
	Status   SandboxStatus
	Backend  string
	Template string
	Query    string
	Labels   map[string]string
}

// CreateTemplateOptions configures a named template. Steps are built when
// the template is created, so a failing step fails the call. Warm keeps up
// to 8 ready sandboxes.
type CreateTemplateOptions struct {
	Name         string
	Image        string
	Description  string
	CPU          float64
	MemoryMB     int
	DiskMB       int
	Env          map[string]string
	Backend      string
	Network      NetworkMode
	Setup        []string
	SetupTimeout time.Duration
	Warm         int
	Steps        []BuildStep
}

// CreateKeyOptions configures a new API key. Scopes are admin or sandbox.
type CreateKeyOptions struct {
	Scopes     []string
	Network    *KeyNetworkPolicy
	ExpiresAt  time.Time
	Registries []RegistryCredential
}

// SnapshotOptions configures a snapshot. Template also saves the image as a
// template with that name, which needs an admin key.
type SnapshotOptions struct {
	Template    string
	Description string
}

// TerminalOptions configures StartTerminal.
type TerminalOptions struct {
	ExecOptions
	Cols int
	Rows int
}

// Signal is a signal for SignalProcess.
type Signal string

// Signals the API accepts.
const (
	SIGTERM Signal = "TERM"
	SIGKILL Signal = "KILL"
	SIGINT  Signal = "INT"
	SIGHUP  Signal = "HUP"
	SIGQUIT Signal = "QUIT"
)

// IfExists is what CreateVolume does when the key already has a volume with
// that name.
type IfExists string

// IfExists values. Reuse returns the existing volume.
const (
	IfExistsError IfExists = "error"
	IfExistsReuse IfExists = "reuse"
)
