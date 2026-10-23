package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

// buildLogTail is how much build output an error message keeps.
const buildLogTail = 4096

// HasImage reports whether the daemon already has ref.
func (d *Driver) HasImage(ctx context.Context, ref string) (bool, error) {
	_, err := d.client.ImageInspect(ctx, ref)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, driver.ErrNotFound) {
		return false, nil
	}
	return false, err
}

// BuildImage builds spec with the daemon's classic builder and tags it.
func (d *Driver) BuildImage(ctx context.Context, spec driver.BuildSpec) error {
	if spec.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	dockerfile, err := renderDockerfile(spec)
	if err != nil {
		return fmt.Errorf("%w: %s", driver.ErrImage, err.Error())
	}
	// Pull the base here, with its credential, so the builder finds it locally
	// and never pulls it anonymously.
	if err := d.ensureImage(ctx, spec.Base, spec.Registry, spec.ForcePull); err != nil {
		return err
	}
	if err := d.client.ImageBuild(ctx, spec.Tag, dockerfile); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%w: build timed out after %ds", driver.ErrImage, spec.TimeoutSeconds)
		}
		return fmt.Errorf("%w: %s", driver.ErrImage, err.Error())
	}
	return nil
}

// PullImage pulls ref with auth whether or not the daemon has it.
func (d *Driver) PullImage(ctx context.Context, ref string, auth *driver.RegistryAuth) error {
	return d.ensureImage(ctx, ref, auth, true)
}

// renderDockerfile writes every step in exec form. JSON quoting keeps a step
// on one Dockerfile line, so a script cannot add instructions of its own.
func renderDockerfile(spec driver.BuildSpec) ([]byte, error) {
	if spec.Base == "" || strings.ContainsAny(spec.Base, " \t\r\n") {
		return nil, fmt.Errorf("invalid base image %q", spec.Base)
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "FROM %s\nUSER root\n", spec.Base)
	for _, script := range spec.Run {
		argv, err := json.Marshal([]string{"/bin/sh", "-c", script})
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "RUN %s\n", argv)
	}
	return b.Bytes(), nil
}

// ImageBuild sends a context that holds only a Dockerfile and tags the result.
func (c *Client) ImageBuild(ctx context.Context, tag string, dockerfile []byte) error {
	if err := c.ensure(); err != nil {
		return err
	}
	var ctxTar bytes.Buffer
	tw := tar.NewWriter(&ctxTar)
	if err := tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(dockerfile)), ModTime: time.Unix(0, 0)}); err != nil {
		return err
	}
	if _, err := tw.Write(dockerfile); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	q := url.Values{}
	q.Set("t", tag)
	q.Set("rm", "1")
	q.Set("forcerm", "1")
	q.Set("labels", `{"sbx.build":"1"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker"+c.path("/build")+"?"+q.Encode(), &ctxTar)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return statusErr(resp.StatusCode, raw)
	}
	return readBuildStream(resp.Body)
}

// readBuildStream drains the daemon's JSON progress lines. The first error
// line fails the build, with the output of the failing step for context.
func readBuildStream(r io.Reader) error {
	dec := json.NewDecoder(r)
	var tail []byte
	for {
		var msg struct {
			Stream string `json:"stream"`
			Error  string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if strings.HasPrefix(msg.Stream, "Step ") || strings.HasPrefix(msg.Stream, " ---> ") {
			// Builder chatter. A new step starts a fresh tail.
			if strings.HasPrefix(msg.Stream, " ---> Running in ") {
				tail = tail[:0]
			}
			continue
		}
		if msg.Stream != "" {
			tail = append(tail, ansiRE.ReplaceAllString(msg.Stream, "")...)
			if len(tail) > buildLogTail {
				tail = tail[len(tail)-buildLogTail:]
			}
		}
		if msg.Error != "" {
			reason := msg.Error
			if m := exitRE.FindStringSubmatch(msg.Error); m != nil {
				reason = "build step exited with code " + m[1]
			}
			out := strings.TrimSpace(string(tail))
			if out == "" {
				return errors.New(reason)
			}
			return fmt.Errorf("%s:\n%s", reason, out)
		}
	}
}

var (
	exitRE = regexp.MustCompile(`returned a non-zero code: (\d+)`)
	ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")
)
