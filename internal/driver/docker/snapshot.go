package docker

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

const (
	// labelSnapshotWorkspace names the directory in a snapshot image that
	// holds the /workspace copy. Create seeds /workspace from it.
	labelSnapshotWorkspace = "sbx.snapshot.workspace"
	snapshotSeedRoot       = "/var/lib/sbx-snapshot"
	// snapshotStderrTail is how much stderr a failed snapshot exec keeps.
	snapshotStderrTail = 4096
)

var _ driver.Snapshotter = (*Driver)(nil)

// commitConfig is the image config sent with POST /commit. The daemon fills
// every empty field, and appends env and labels, from the container.
type commitConfig struct {
	Env        []string          `json:"Env"`
	Labels     map[string]string `json:"Labels"`
	Entrypoint []string          `json:"Entrypoint"`
	Cmd        []string          `json:"Cmd"`
	User       string            `json:"User"`
	WorkingDir string            `json:"WorkingDir"`
}

// Snapshot commits the container to spec.Tag. /workspace is a tmpfs, which
// commit skips, so with spec.Workspace it is streamed out with tar and added
// on top of the commit as a layer that Create copies back.
func (d *Driver) Snapshot(ctx context.Context, id string, spec driver.SnapshotSpec) error {
	if !d.Capabilities().Snapshot {
		return fmt.Errorf("%w: %s cannot snapshot a sandbox", driver.ErrRuntimeUnavailable, d.name)
	}
	_, tag := splitRef(spec.Tag)
	if tag == "" {
		return fmt.Errorf("%w: snapshot tag %q has no tag part", driver.ErrConfig, spec.Tag)
	}
	base, err := d.client.ImageInspect(ctx, spec.Base)
	if err != nil {
		return err
	}
	ctn, err := d.client.ContainerInspect(ctx, id)
	if err != nil {
		return err
	}
	seed := ""
	if spec.Workspace {
		seed = snapshotSeedRoot + "/" + tag
	}
	cfg := snapshotConfig(base, ctn, seed)
	// A sandbox started from a snapshot carries its parent's seed in the
	// image. It is dropped so files the sandbox deleted from /workspace do
	// not stay readable there, and so forks of forks do not grow.
	inherited := base.Labels[labelSnapshotWorkspace] != ""
	if !spec.Workspace && !inherited {
		_, err := d.client.Commit(ctx, id, spec.Tag, cfg)
		return snapshotErr(err)
	}
	imageID, err := d.client.Commit(ctx, id, "", cfg)
	if err != nil {
		return snapshotErr(err)
	}
	var df strings.Builder
	fmt.Fprintf(&df, "FROM %s\n", imageID)
	if inherited {
		fmt.Fprintf(&df, "USER root\nRUN rm -rf %s\n", snapshotSeedRoot)
		if base.User != "" && !strings.ContainsAny(base.User, " \t\r\n") {
			fmt.Fprintf(&df, "USER %s\n", base.User)
		}
	}
	if !spec.Workspace {
		return snapshotErr(d.client.ImageBuild(ctx, spec.Tag, []byte(df.String())))
	}
	fmt.Fprintf(&df, "COPY --chown=%d:%d workspace/ %s/\n", d.opt.UID, d.opt.GID, seed)
	dockerfile := df.String()
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(d.writeSnapshotContext(ctx, id, []byte(dockerfile), pw))
	}()
	err = d.client.ImageBuildContext(ctx, spec.Tag, pr)
	_ = pr.CloseWithError(errors.New("build finished"))
	return snapshotErr(err)
}

func snapshotErr(err error) error {
	if err == nil || errors.Is(err, driver.ErrNotFound) {
		return err
	}
	return fmt.Errorf("%w: snapshot: %s", driver.ErrImage, err.Error())
}

// snapshotConfig resets the committed config to the base image's, so the
// sandbox entrypoint, labels, env, and secrets do not carry over. Commit
// merges in every env key and label the container has, and Docker has no
// way to unset one, so keys the base lacks are kept with empty values.
func snapshotConfig(base ImageMeta, ctn ContainerState, seed string) commitConfig {
	env := []string{}
	have := map[string]bool{}
	for _, item := range base.Env {
		k, _, _ := strings.Cut(item, "=")
		have[k] = true
		env = append(env, item)
	}
	var blank []string
	for _, item := range ctn.Env {
		k, _, _ := strings.Cut(item, "=")
		if k != "" && !have[k] {
			have[k] = true
			blank = append(blank, k+"=")
		}
	}
	sort.Strings(blank)
	labels := map[string]string{}
	for k, v := range base.Labels {
		labels[k] = v
	}
	for k := range ctn.Labels {
		if _, ok := labels[k]; !ok {
			labels[k] = ""
		}
	}
	labels[labelSnapshotWorkspace] = seed
	// Image collection only looks at images with the build label.
	labels["sbx.build"] = "1"
	return commitConfig{
		Env: append(env, blank...), Labels: labels, Entrypoint: base.Entrypoint, Cmd: base.Cmd,
		User: base.User, WorkingDir: base.WorkingDir,
	}
}

// writeSnapshotContext writes a build context to w: the Dockerfile, then
// the container's /workspace under workspace/.
func (d *Driver) writeSnapshotContext(ctx context.Context, id string, dockerfile []byte, w io.Writer) error {
	tw := tar.NewWriter(w)
	epoch := time.Unix(0, 0)
	if err := tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(dockerfile)), ModTime: epoch}); err != nil {
		return err
	}
	if _, err := tw.Write(dockerfile); err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: "workspace/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: epoch}); err != nil {
		return err
	}
	err := d.shellStream(ctx, id, d.user(), "exec tar -C /workspace -cf - .", nil, func(r io.Reader) error {
		tr := tar.NewReader(r)
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			name := strings.TrimPrefix(path.Clean("/"+hdr.Name), "/")
			if name == "" {
				continue
			}
			hdr.Name = "workspace/" + name
			if hdr.Typeflag == tar.TypeDir {
				hdr.Name += "/"
			}
			if hdr.Typeflag == tar.TypeLink {
				hdr.Linkname = "workspace/" + strings.TrimPrefix(path.Clean("/"+hdr.Linkname), "/")
			}
			hdr.Format = tar.FormatUnknown
			hdr.Uname, hdr.Gname = "", ""
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if _, err := io.Copy(tw, tr); err != nil {
				return err
			}
		}
	})
	if err != nil {
		return fmt.Errorf("copy /workspace: %w", err)
	}
	return tw.Close()
}

// seedWorkspace copies a snapshot's /workspace back into a new sandbox.
func (d *Driver) seedWorkspace(ctx context.Context, id, seed string) error {
	return d.shellStream(ctx, id, d.user(), `cp -a "$1"/. /workspace/`, []string{seed}, nil)
}

func (d *Driver) user() string {
	return fmt.Sprintf("%d:%d", d.opt.UID, d.opt.GID)
}

// shellStream runs a script with docker exec, bypassing the agent, and hands
// its stdout to read. A non-zero exit fails with the tail of stderr.
func (d *Driver) shellStream(ctx context.Context, id, user, script string, args []string, read func(io.Reader) error) error {
	cmd := append([]string{"/bin/sh", "-c", script, "sh"}, args...)
	execID, err := d.client.ExecCreate(ctx, id, execConfig{
		AttachStdout: true, AttachStderr: true, Cmd: cmd, WorkingDir: "/", User: user,
	})
	if err != nil {
		return err
	}
	stream, err := d.client.ExecStart(ctx, execID, nil)
	if err != nil {
		return err
	}
	defer stream.Close()
	pr, pw := io.Pipe()
	readErr := make(chan error, 1)
	go func() {
		var err error
		if read != nil {
			err = read(pr)
		}
		// Drain so the demuxer never blocks on a reader that stopped early.
		_, _ = io.Copy(io.Discard, pr)
		readErr <- err
	}()
	var stderr []byte
	_, _, copyErr := CopyDemux(stream, 0, func(s byte, data []byte) error {
		if s == 2 {
			stderr = append(stderr, data...)
			if len(stderr) > snapshotStderrTail {
				stderr = stderr[len(stderr)-snapshotStderrTail:]
			}
			return nil
		}
		_, err := pw.Write(data)
		return err
	})
	pw.CloseWithError(copyErr)
	if err := <-readErr; err != nil {
		return err
	}
	if copyErr != nil {
		return copyErr
	}
	state, err := d.waitExec(context.Background(), execID)
	if err != nil {
		return err
	}
	if state.ExitCode != 0 {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = "no output"
		}
		return fmt.Errorf("%s exited with code %d: %s", strings.Fields(script)[0], state.ExitCode, msg)
	}
	return nil
}

// Commit creates an image from a container and returns its id. ref may be
// empty for an untagged image.
func (c *Client) Commit(ctx context.Context, id, ref string, cfg commitConfig) (string, error) {
	q := url.Values{}
	q.Set("container", id)
	q.Set("pause", "1")
	if ref != "" {
		repo, tag := splitRef(ref)
		q.Set("repo", repo)
		q.Set("tag", tag)
	}
	var resp struct {
		ID string `json:"Id"`
	}
	if err := c.DoJSON(ctx, http.MethodPost, "/commit?"+q.Encode(), cfg, &resp); err != nil {
		return "", err
	}
	if resp.ID == "" {
		return "", errors.New("docker commit returned no image id")
	}
	return resp.ID, nil
}

func mountsWorkspace(mounts []driver.Mount) bool {
	for _, m := range mounts {
		if m.Target == "/workspace" {
			return true
		}
	}
	return false
}
