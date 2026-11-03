package sbx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// DefaultBaseURL is used when neither WithBaseURL nor SBX_URL is set.
const DefaultBaseURL = "http://127.0.0.1:8080"

// Error is an error response from the API. Code is the API's error code,
// such as not_found, conflict, or unsupported.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("sbx: %s (%d %s)", e.Message, e.Status, e.Code)
}

// StatusCode returns the HTTP status of an *Error in err's chain, or 0.
func StatusCode(err error) int {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

// IsNotFound reports whether err is a 404 from the API.
func IsNotFound(err error) bool { return StatusCode(err) == http.StatusNotFound }

// IsConflict reports whether err is a 409 from the API.
func IsConflict(err error) bool { return StatusCode(err) == http.StatusConflict }

// Client is the HTTP client for one sbx server. It is safe for concurrent
// use. Most calls take a sandbox id; Sandbox wraps them for one sandbox.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL sets the server URL. It overrides SBX_URL.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) { c.baseURL = baseURL }
}

// WithAPIKey sets the API key. It overrides SBX_API_KEY.
func WithAPIKey(key string) Option {
	return func(c *Client) { c.apiKey = key }
}

// WithHTTPClient sets the HTTP client, for proxies, custom TLS, or tests.
// Do not set a Timeout on it: execs and streams can run for a long time.
// Use a context deadline per call instead.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.http = hc }
}

// NewClient returns a client. The base URL defaults to SBX_URL, then
// DefaultBaseURL, and the API key to SBX_API_KEY. A key is required.
func NewClient(opts ...Option) (*Client, error) {
	c := &Client{baseURL: os.Getenv("SBX_URL"), apiKey: os.Getenv("SBX_API_KEY")}
	for _, opt := range opts {
		opt(c)
	}
	if c.baseURL == "" {
		c.baseURL = DefaultBaseURL
	}
	c.baseURL = strings.TrimRight(c.baseURL, "/")
	if _, err := url.Parse(c.baseURL); err != nil {
		return nil, fmt.Errorf("sbx: invalid base URL: %w", err)
	}
	if c.apiKey == "" {
		return nil, errors.New("sbx: an API key is required (use WithAPIKey or set SBX_API_KEY)")
	}
	if c.http == nil {
		c.http = &http.Client{}
	}
	return c, nil
}

// BaseURL returns the server URL without a trailing slash.
func (c *Client) BaseURL() string { return c.baseURL }

// q escapes one path segment.
func q(s string) string { return url.PathEscape(s) }

func withQuery(path string, values url.Values) string {
	if len(values) == 0 {
		return path
	}
	return path + "?" + values.Encode()
}

// do sends a request and returns the response when the status is 2xx. The
// caller closes the body. body is JSON-encoded unless it is an io.Reader.
func (c *Client) do(ctx context.Context, method, path string, body any, header http.Header) (*http.Response, error) {
	var reader io.Reader
	contentType := ""
	switch b := body.(type) {
	case nil:
	case io.Reader:
		reader = b
		contentType = "application/octet-stream"
	case []byte:
		reader = bytes.NewReader(b)
		contentType = "application/octet-stream"
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			return nil, fmt.Errorf("sbx: encode request: %w", err)
		}
		reader = bytes.NewReader(buf)
		contentType = "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	for name, values := range header {
		req.Header[name] = values
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, errorFrom(resp)
	}
	return resp, nil
}

// doJSON sends a request and decodes a JSON response into out, if out is not
// nil.
func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.do(ctx, method, path, body, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("sbx: decode response: %w", err)
	}
	return nil
}

func (c *Client) doBytes(ctx context.Context, method, path string) ([]byte, error) {
	resp, err := c.do(ctx, method, path, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func errorFrom(resp *http.Response) error {
	e := &Error{Status: resp.StatusCode, Code: "driver", Message: resp.Status}
	if text := http.StatusText(resp.StatusCode); text != "" {
		e.Message = text
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if json.Unmarshal(data, &body) == nil {
		if body.Error.Code != "" {
			e.Code = body.Error.Code
		}
		if body.Error.Message != "" {
			e.Message = body.Error.Message
		}
	}
	return e
}
