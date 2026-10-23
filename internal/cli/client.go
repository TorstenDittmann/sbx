package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// client talks to a running sbx server for the sandbox commands.
type client struct {
	base string
	key  string
	http *http.Client
}

// apiError is the server's {"error": {...}} body.
type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("server returned %d", e.Status)
	}
	return e.Message
}

// exitError carries a sandbox command's exit code back to main.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// ExitCode reports the exit code a command asked for, if err carries one.
func ExitCode(err error) (int, bool) {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code, true
	}
	return 0, false
}

// connFlags registers --url and --key on fs. The key falls back to
// SBX_API_KEY and then to the initial_api_key file the server wrote.
func connFlags(fs *flag.FlagSet) func() (*client, error) {
	base := fs.String("url", os.Getenv("SBX_URL"), "server URL (SBX_URL)")
	key := fs.String("key", os.Getenv("SBX_API_KEY"), "API key (SBX_API_KEY)")
	return func() (*client, error) {
		u := *base
		if u == "" {
			u = "http://127.0.0.1:8080"
		}
		k := *key
		if k == "" {
			k = keyFromDataDir()
		}
		if k == "" {
			return nil, errors.New("no API key: set SBX_API_KEY or pass --key")
		}
		return &client{base: strings.TrimRight(u, "/"), key: k, http: &http.Client{}}, nil
	}
}

func keyFromDataDir() string {
	dir := os.Getenv("SBX_DATA_DIR")
	if dir == "" {
		dir = "data"
	}
	b, err := os.ReadFile(filepath.Join(dir, "initial_api_key"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (c *client) do(method, p string, q url.Values, body io.Reader, contentType string) (*http.Response, error) {
	u := c.base + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, readAPIError(resp)
	}
	return resp, nil
}

// send is do for calls whose response body is not needed.
func (c *client) send(method, p string, q url.Values, body io.Reader, contentType string) error {
	resp, err := c.do(method, p, q, body, contentType)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

func readAPIError(resp *http.Response) error {
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(raw, &body) != nil || body.Error.Message == "" {
		return &apiError{Status: resp.StatusCode, Message: strings.TrimSpace(fmt.Sprintf("%s %s", resp.Status, raw))}
	}
	return &apiError{Status: resp.StatusCode, Code: body.Error.Code, Message: body.Error.Message}
}

// json sends v as JSON (when not nil) and decodes the response into out
// (when not nil).
func (c *client) json(method, p string, q url.Values, v, out any) error {
	var body io.Reader
	ct := ""
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		body, ct = bytes.NewReader(b), "application/json"
	}
	resp, err := c.do(method, p, q, body, ct)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// sandbox is the subset of the sandbox resource the CLI prints.
type sandbox struct {
	ID        string            `json:"id"`
	Status    string            `json:"status"`
	Backend   string            `json:"backend"`
	Template  string            `json:"template"`
	Image     string            `json:"image"`
	CPU       float64           `json:"cpu"`
	MemoryMB  int               `json:"memory_mb"`
	Network   string            `json:"network"`
	Labels    map[string]string `json:"labels"`
	ExpiresAt time.Time         `json:"expires_at"`
	CreatedAt time.Time         `json:"created_at"`
}

type fileEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	Mode string `json:"mode"`
}

func sandboxPath(id string, parts ...string) string {
	p := "/v1/sandboxes/" + url.PathEscape(id)
	for _, s := range parts {
		p += "/" + s
	}
	return p
}

// remotePath makes p absolute; relative paths are under /workspace.
func remotePath(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = "/workspace/" + p
	}
	return path.Clean(p)
}

// wsURL maps the server URL to its WebSocket scheme.
func (c *client) wsURL(p string) string {
	switch {
	case strings.HasPrefix(c.base, "https://"):
		return "wss://" + strings.TrimPrefix(c.base, "https://") + p
	case strings.HasPrefix(c.base, "http://"):
		return "ws://" + strings.TrimPrefix(c.base, "http://") + p
	}
	return c.base + p
}
