package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

// Client is a small Docker Engine API client over a Unix socket.
// v0.1 only needs the calls the sandbox driver makes, so this stays
// independent of the moby module tree.
type Client struct {
	socket  string
	version string
	http    *http.Client

	once   sync.Once
	negErr error
}

// NewClient dials host. Accepted forms: "" (default socket), a filesystem
// path, or unix:///path.
func NewClient(host string) (*Client, error) {
	sock, err := socketPath(host)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}
	return &Client{
		socket: sock,
		http:   &http.Client{Transport: transport},
	}, nil
}

func socketPath(host string) (string, error) {
	switch {
	case host == "" || host == "unix:///var/run/docker.sock":
		return "/var/run/docker.sock", nil
	case strings.HasPrefix(host, "unix://"):
		return strings.TrimPrefix(host, "unix://"), nil
	case strings.HasPrefix(host, "/"):
		return host, nil
	default:
		return "", fmt.Errorf("only a unix docker socket is supported in v0.1 (got %q)", host)
	}
}

// StatusError is a non-2xx response from the daemon.
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("docker: status %d", e.Status)
	}
	return e.Message
}

func (c *Client) ensure() error {
	c.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.negErr = c.negotiate(ctx)
	})
	return c.negErr
}

func (c *Client) negotiate(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/version", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker version: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		APIVersion    string `json:"ApiVersion"`
		MinAPIVersion string `json:"MinAPIVersion"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("docker version: %w", err)
	}
	if body.APIVersion == "" {
		c.version = "1.44"
		return nil
	}
	c.version = body.APIVersion
	if body.MinAPIVersion != "" && versionLess(c.version, body.MinAPIVersion) {
		c.version = body.MinAPIVersion
	}
	return nil
}

func versionLess(a, b string) bool {
	ap := strings.Split(strings.TrimPrefix(a, "v"), ".")
	bp := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < 2; i++ {
		var av, bv int
		if i < len(ap) {
			fmt.Sscanf(ap[i], "%d", &av)
		}
		if i < len(bp) {
			fmt.Sscanf(bp[i], "%d", &bv)
		}
		if av != bv {
			return av < bv
		}
	}
	return false
}

func (c *Client) path(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	ver := strings.TrimPrefix(c.version, "v")
	return "/v" + ver + p
}

func (c *Client) DoJSON(ctx context.Context, method, path string, in any, out any) error {
	if err := c.ensure(); err != nil {
		return err
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+c.path(path), body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return statusErr(resp.StatusCode, raw)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func statusErr(code int, raw []byte) error {
	var payload struct {
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &payload) == nil && payload.Message != "" {
		msg = payload.Message
	}
	err := &StatusError{Status: code, Message: msg}
	if code == http.StatusNotFound {
		return fmt.Errorf("%w: %s", driver.ErrNotFound, msg)
	}
	return err
}

// Ping checks that the daemon answers.
func (c *Client) Ping(ctx context.Context) error {
	if err := c.ensure(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+c.path("/_ping"), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return statusErr(resp.StatusCode, raw)
	}
	return nil
}

// Runtime is one entry from the daemon's runtime table.
type Runtime struct {
	Name        string
	Path        string
	RuntimeType string
}

// Info is a subset of GET /info.
type Info struct {
	ServerVersion string
	Runtimes      map[string]struct{}
	RuntimeInfo   map[string]Runtime
}

func (c *Client) Info(ctx context.Context) (Info, error) {
	var raw struct {
		ServerVersion string `json:"ServerVersion"`
		Runtimes      map[string]struct {
			Path        string `json:"path"`
			RuntimeType string `json:"runtimeType"`
		} `json:"Runtimes"`
	}
	if err := c.DoJSON(ctx, http.MethodGet, "/info", nil, &raw); err != nil {
		return Info{}, err
	}
	info := Info{
		ServerVersion: raw.ServerVersion,
		Runtimes:      map[string]struct{}{},
		RuntimeInfo:   map[string]Runtime{},
	}
	for name, rt := range raw.Runtimes {
		info.Runtimes[name] = struct{}{}
		info.RuntimeInfo[name] = Runtime{Name: name, Path: rt.Path, RuntimeType: rt.RuntimeType}
	}
	return info, nil
}

// ImageMeta is the slice of image config this driver needs.
type ImageMeta struct {
	Env []string
}

func (c *Client) ImageInspect(ctx context.Context, ref string) (ImageMeta, error) {
	var raw struct {
		Config struct {
			Env []string `json:"Env"`
		} `json:"Config"`
	}
	err := c.DoJSON(ctx, http.MethodGet, "/images/"+escapeRef(ref)+"/json", nil, &raw)
	if err != nil {
		return ImageMeta{}, err
	}
	return ImageMeta{Env: raw.Config.Env}, nil
}

func escapeRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

// ImagePull pulls ref if the daemon does not already have it. The caller
// decides whether to inspect first. auth may be nil for an anonymous pull.
func (c *Client) ImagePull(ctx context.Context, ref string, auth *driver.RegistryAuth) error {
	if err := c.ensure(); err != nil {
		return err
	}
	from, tag := splitRef(ref)
	q := url.Values{}
	q.Set("fromImage", from)
	if tag != "" {
		q.Set("tag", tag)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker"+c.path("/images/create")+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	if auth != nil {
		header, err := registryAuthHeader(*auth)
		if err != nil {
			return err
		}
		req.Header.Set("X-Registry-Auth", header)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return statusErr(resp.StatusCode, raw)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return nil
		}
		if msg.Error != "" {
			return fmt.Errorf("pull %s: %s", ref, msg.Error)
		}
	}
}

// registryAuthHeader encodes auth the way the daemon reads X-Registry-Auth.
func registryAuthHeader(auth driver.RegistryAuth) (string, error) {
	server := auth.Server
	if server == driver.DockerHub {
		server = "https://index.docker.io/v1/"
	}
	raw, err := json.Marshal(struct {
		Username      string `json:"username,omitempty"`
		Password      string `json:"password,omitempty"`
		IdentityToken string `json:"identitytoken,omitempty"`
		ServerAddress string `json:"serveraddress"`
	}{auth.Username, auth.Password, auth.IdentityToken, server})
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(raw), nil
}

func (c *Client) ContainerCreate(ctx context.Context, name string, body createRequest) (string, error) {
	var resp struct {
		ID  string `json:"Id"`
		ID2 string `json:"ID"`
	}
	q := url.Values{}
	q.Set("name", name)
	if err := c.DoJSON(ctx, http.MethodPost, "/containers/create?"+q.Encode(), body, &resp); err != nil {
		return "", err
	}
	if resp.ID != "" {
		return resp.ID, nil
	}
	return resp.ID2, nil
}

func (c *Client) ContainerStart(ctx context.Context, id string) error {
	return c.DoJSON(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil)
}

func (c *Client) ContainerRemove(ctx context.Context, id string) error {
	err := c.DoJSON(ctx, http.MethodDelete, "/containers/"+id+"?force=true", nil, nil)
	if err == nil || errors.Is(err, driver.ErrNotFound) {
		return nil
	}
	return err
}

// ContainerState is the inspect subset the driver uses.
type ContainerState struct {
	ID        string
	Image     string
	Status    string
	Running   bool
	Paused    bool
	Pid       int
	IPv4      string
	IPv6      string
	Labels    map[string]string
	SandboxID string
	Env       []string
}

func (c *Client) ContainerInspect(ctx context.Context, id string) (ContainerState, error) {
	var raw struct {
		ID    string `json:"Id"`
		Image string `json:"Image"`
		State struct {
			Status  string `json:"Status"`
			Running bool   `json:"Running"`
			Paused  bool   `json:"Paused"`
			Pid     int    `json:"Pid"`
		} `json:"State"`
		Config struct {
			Image  string            `json:"Image"`
			Labels map[string]string `json:"Labels"`
			Env    []string          `json:"Env"`
		} `json:"Config"`
		NetworkSettings struct {
			Networks map[string]struct {
				IPAddress         string `json:"IPAddress"`
				GlobalIPv6Address string `json:"GlobalIPv6Address"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := c.DoJSON(ctx, http.MethodGet, "/containers/"+id+"/json", nil, &raw); err != nil {
		return ContainerState{}, err
	}
	image := raw.Config.Image
	if image == "" {
		image = raw.Image
	}
	ipv4, ipv6 := "", ""
	if n, ok := raw.NetworkSettings.Networks[sandboxNetwork]; ok {
		ipv4, ipv6 = n.IPAddress, n.GlobalIPv6Address
	}
	for _, n := range raw.NetworkSettings.Networks {
		if ipv4 == "" {
			ipv4 = n.IPAddress
		}
		if ipv6 == "" {
			ipv6 = n.GlobalIPv6Address
		}
	}
	return ContainerState{
		ID: raw.ID, Image: image, Status: raw.State.Status, Running: raw.State.Running, Paused: raw.State.Paused,
		Pid: raw.State.Pid, IPv4: ipv4, IPv6: ipv6,
		Labels: raw.Config.Labels, SandboxID: raw.Config.Labels[labelID], Env: raw.Config.Env,
	}, nil
}

func (c *Client) ContainerList(ctx context.Context) ([]ContainerState, error) {
	filters := url.Values{}
	filters.Set("all", "1")
	filters.Set("filters", `{"label":["`+labelManaged+`=true"]}`)
	var raw []struct {
		ID     string            `json:"Id"`
		Image  string            `json:"Image"`
		State  string            `json:"State"`
		Labels map[string]string `json:"Labels"`
	}
	if err := c.DoJSON(ctx, http.MethodGet, "/containers/json?"+filters.Encode(), nil, &raw); err != nil {
		return nil, err
	}
	out := make([]ContainerState, 0, len(raw))
	for _, ctn := range raw {
		out = append(out, ContainerState{
			ID: ctn.ID, Image: ctn.Image, Status: ctn.State, Labels: ctn.Labels, SandboxID: ctn.Labels[labelID],
		})
	}
	return out, nil
}

func (c *Client) ContainerPause(ctx context.Context, id string) error {
	return c.DoJSON(ctx, http.MethodPost, "/containers/"+id+"/pause", nil, nil)
}

func (c *Client) ContainerUnpause(ctx context.Context, id string) error {
	return c.DoJSON(ctx, http.MethodPost, "/containers/"+id+"/unpause", nil, nil)
}

func (c *Client) ExecCreate(ctx context.Context, id string, body execConfig) (string, error) {
	var resp struct {
		ID  string `json:"Id"`
		ID2 string `json:"ID"`
	}
	if err := c.DoJSON(ctx, http.MethodPost, "/containers/"+id+"/exec", body, &resp); err != nil {
		return "", err
	}
	if resp.ID != "" {
		return resp.ID, nil
	}
	return resp.ID2, nil
}

// ExecState is GET /exec/{id}/json.
type ExecState struct {
	ExitCode int
	Running  bool
}

func (c *Client) ExecInspect(ctx context.Context, id string) (ExecState, error) {
	var raw struct {
		ExitCode int  `json:"ExitCode"`
		Running  bool `json:"Running"`
	}
	if err := c.DoJSON(ctx, http.MethodGet, "/exec/"+id+"/json", nil, &raw); err != nil {
		return ExecState{}, err
	}
	return ExecState{ExitCode: raw.ExitCode, Running: raw.Running}, nil
}

// ExecStart hijacks the exec stream. If stdin is non-nil it is copied raw
// and the write half is closed so the process sees EOF. The returned reader
// is the multiplexed stdout/stderr stream.
func (c *Client) ExecStart(ctx context.Context, execID string, stdin io.Reader) (io.ReadCloser, error) {
	if err := c.ensure(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{"Detach": false, "Tty": false})
	if err != nil {
		return nil, err
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, "http://docker"+c.path("/exec/"+execID+"/start"), bytes.NewReader(payload))
	if err != nil {
		conn.Close()
		return nil, err
	}
	req.Host = "docker"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	req.ContentLength = int64(len(payload))
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		conn.Close()
		return nil, statusErr(resp.StatusCode, b)
	}
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	if stdin != nil {
		go func() {
			_, _ = io.Copy(conn, stdin)
			if cw, ok := conn.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
		}()
	}
	return &streamConn{Conn: conn, r: br}, nil
}

// ExecAttach hijacks an exec and leaves stdin open. Reads are demultiplexed
// stdout. Writes are raw stdin. The caller closes the stream.
func (c *Client) ExecAttach(ctx context.Context, execID string) (io.ReadWriteCloser, error) {
	if err := c.ensure(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{"Detach": false, "Tty": false})
	if err != nil {
		return nil, err
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, "http://docker"+c.path("/exec/"+execID+"/start"), bytes.NewReader(payload))
	if err != nil {
		conn.Close()
		return nil, err
	}
	req.Host = "docker"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	req.ContentLength = int64(len(payload))
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		conn.Close()
		return nil, statusErr(resp.StatusCode, b)
	}
	pr, pw := io.Pipe()
	go func() {
		_, _, err := CopyDemux(br, 0, func(stream byte, data []byte) error {
			if stream != 1 || len(data) == 0 {
				return nil
			}
			_, err := pw.Write(data)
			return err
		})
		_ = pw.CloseWithError(err)
	}()
	return &attachConn{Conn: conn, pr: pr}, nil
}

type attachConn struct {
	net.Conn
	pr *io.PipeReader
}

func (a *attachConn) Read(p []byte) (int, error) { return a.pr.Read(p) }

func (a *attachConn) Close() error {
	err := a.Conn.Close()
	_ = a.pr.Close()
	return err
}

type streamConn struct {
	net.Conn
	r    io.Reader
	once sync.Once
}

func (s *streamConn) Read(p []byte) (int, error) { return s.r.Read(p) }

func (s *streamConn) Close() error {
	var err error
	s.once.Do(func() { err = s.Conn.Close() })
	return err
}

type networkInfo struct {
	ID      string            `json:"Id"`
	Name    string            `json:"Name"`
	Options map[string]string `json:"Options"`
	IPAM    struct {
		Config []struct {
			Subnet  string `json:"Subnet"`
			Gateway string `json:"Gateway"`
		} `json:"Config"`
	} `json:"IPAM"`
}

// EnsureSandboxNetwork creates the dedicated bridge used by network=full.
// The bridge name is what the firewall matches on. When ipv6 is set and the
// daemon rejects it, the bridge is created again with IPv4 only.
func (c *Client) EnsureSandboxNetwork(ctx context.Context, ipv6 bool) (string, error) {
	info, err := c.networkInspect(ctx, sandboxNetwork)
	if err == nil {
		return validateNetwork(info)
	}
	if !errors.Is(err, driver.ErrNotFound) {
		return "", err
	}
	if err := c.createSandboxNetwork(ctx, ipv6); err != nil {
		if !ipv6 {
			return "", err
		}
		if err2 := c.createSandboxNetwork(ctx, false); err2 != nil {
			return "", err
		}
	}
	info, err = c.networkInspect(ctx, sandboxNetwork)
	if err != nil {
		return "", err
	}
	return validateNetwork(info)
}

func (c *Client) createSandboxNetwork(ctx context.Context, ipv6 bool) error {
	err := c.DoJSON(ctx, http.MethodPost, "/networks/create", sandboxNetworkSpec(ipv6), nil)
	if err == nil {
		return nil
	}
	var status *StatusError
	if errors.As(err, &status) && status.Status == http.StatusConflict {
		return nil
	}
	info, again := c.networkInspect(ctx, sandboxNetwork)
	if again == nil {
		if _, vErr := validateNetwork(info); vErr == nil {
			return nil
		}
	}
	return err
}

func (c *Client) networkInspect(ctx context.Context, name string) (networkInfo, error) {
	var info networkInfo
	if err := c.DoJSON(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil, &info); err != nil {
		return networkInfo{}, err
	}
	return info, nil
}

// VolumeMountpoint is the host path of a named volume's data directory.
func (c *Client) VolumeMountpoint(ctx context.Context, name string) (string, error) {
	var raw struct {
		Mountpoint string `json:"Mountpoint"`
	}
	if err := c.DoJSON(ctx, http.MethodGet, "/volumes/"+url.PathEscape(name), nil, &raw); err != nil {
		return "", err
	}
	return raw.Mountpoint, nil
}

// VolumeInspect reads a named volume.
func (c *Client) VolumeInspect(ctx context.Context, name string) (map[string]string, error) {
	var raw struct {
		Labels map[string]string `json:"Labels"`
	}
	if err := c.DoJSON(ctx, http.MethodGet, "/volumes/"+url.PathEscape(name), nil, &raw); err != nil {
		return nil, err
	}
	if raw.Labels == nil {
		raw.Labels = map[string]string{}
	}
	return raw.Labels, nil
}

// VolumeCreate creates a named volume. A conflict means it already exists.
func (c *Client) VolumeCreate(ctx context.Context, name string, labels map[string]string) error {
	body := map[string]any{"Name": name, "Labels": labels}
	err := c.DoJSON(ctx, http.MethodPost, "/volumes/create", body, nil)
	if err == nil {
		return nil
	}
	var status *StatusError
	if errors.As(err, &status) && status.Status == http.StatusConflict {
		return nil
	}
	return err
}

// PutArchive extracts a tar into a container path.
func (c *Client) PutArchive(ctx context.Context, id, dest string, body []byte) error {
	if err := c.ensure(); err != nil {
		return err
	}
	q := url.Values{}
	q.Set("path", dest)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://docker"+c.path("/containers/"+id+"/archive")+"?"+q.Encode(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return statusErr(resp.StatusCode, raw)
	}
	return nil
}

// ContainerWait blocks until the container exits.
func (c *Client) ContainerWait(ctx context.Context, id string) (int, error) {
	var resp struct {
		StatusCode int `json:"StatusCode"`
	}
	if err := c.DoJSON(ctx, http.MethodPost, "/containers/"+id+"/wait", nil, &resp); err != nil {
		return 0, err
	}
	return resp.StatusCode, nil
}

// ContainerLogs returns combined stdout and stderr.
func (c *Client) ContainerLogs(ctx context.Context, id string) (string, error) {
	raw, err := c.getBytes(ctx, "/containers/"+id+"/logs?stdout=1&stderr=1")
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	_, _, demuxErr := CopyDemux(bytes.NewReader(raw), 1<<20, func(_ byte, data []byte) error {
		buf.Write(data)
		return nil
	})
	if demuxErr != nil && !errors.Is(demuxErr, io.EOF) && !errors.Is(demuxErr, io.ErrUnexpectedEOF) && buf.Len() == 0 {
		return "", demuxErr
	}
	return buf.String(), nil
}

func (c *Client) getBytes(ctx context.Context, path string) ([]byte, error) {
	if err := c.ensure(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+c.path(path), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, statusErr(resp.StatusCode, raw)
	}
	return raw, nil
}
