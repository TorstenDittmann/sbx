package firecracker

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/docker"
)

// RootSpec is one ext4 image build.
type RootSpec struct {
	Image  string
	Agent  string
	Dest   string
	DiskMB int
	Guest  []byte
	// Registry and ForcePull are SandboxSpec's, for the OCI image.
	Registry  *driver.RegistryAuth
	ForcePull bool
}

func (d *Driver) buildRoot(ctx context.Context, spec RootSpec) error {
	if path, ok := strings.CutPrefix(spec.Image, "ext4:"); ok {
		if err := d.checkHostImage(path); err != nil {
			return err
		}
	}
	if d.opt.Build != nil {
		return d.opt.Build(ctx, spec)
	}
	return buildRootfs(ctx, d.opt.DockerHost, filepath.Join(d.opt.DataDir, "firecracker", "cache"), spec)
}

func buildRootfs(ctx context.Context, dockerHost, cacheDir string, spec RootSpec) error {
	if spec.DiskMB < 16 {
		return fmt.Errorf("%w: disk_mb must be at least 16", driver.ErrConfig)
	}
	if err := os.MkdirAll(filepath.Dir(spec.Dest), 0o755); err != nil {
		return err
	}
	if path, ok := strings.CutPrefix(spec.Image, "ext4:"); ok {
		if err := copyFile(path, spec.Dest); err != nil {
			return fmt.Errorf("%w: %s", driver.ErrImage, err)
		}
		return inject(ctx, spec.Dest, spec.Guest)
	}
	if spec.ForcePull {
		// The ext4 cache is keyed by image name, so check the registry first.
		cli, err := docker.NewClient(dockerHost)
		if err != nil {
			return err
		}
		if err := cli.ImagePull(ctx, spec.Image, spec.Registry); err != nil {
			return fmt.Errorf("%w: %v", driver.ErrImage, err)
		}
	}
	sum, err := fileSHA(spec.Agent)
	if err != nil {
		return fmt.Errorf("%w: agent: %v", driver.ErrImage, err)
	}
	key := cacheKey(spec.Image, sum, spec.DiskMB)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	cached := filepath.Join(cacheDir, key+".ext4")
	if _, err := os.Stat(cached); err != nil {
		if err := buildOCI(ctx, dockerHost, spec, cached); err != nil {
			_ = os.Remove(cached)
			return err
		}
	}
	if err := copyFile(cached, spec.Dest); err != nil {
		return err
	}
	return inject(ctx, spec.Dest, spec.Guest)
}

// checkHostImage refuses an ext4: base inside the data directory, where
// every sandbox's disk and the rootfs cache live. Core already limits ext4:
// to admin keys; this keeps one sandbox from booting another's disk.
func (d *Driver) checkHostImage(path string) error {
	if d.opt.DataDir == "" {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("%w: %s", driver.ErrImage, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return fmt.Errorf("%w: %s", driver.ErrImage, err)
	}
	data, err := filepath.EvalSymlinks(d.opt.DataDir)
	if err != nil {
		data = d.opt.DataDir
	}
	if data, err = filepath.Abs(data); err != nil {
		return fmt.Errorf("%w: %s", driver.ErrImage, err)
	}
	if resolved == data || strings.HasPrefix(resolved, data+string(os.PathSeparator)) {
		return fmt.Errorf("%w: an ext4: image cannot be inside the data directory", driver.ErrImage)
	}
	return nil
}

func cacheKey(image, agentSum string, diskMB int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\n%s\n%d", image, agentSum, diskMB)))
	return hex.EncodeToString(h[:])
}

func buildOCI(ctx context.Context, dockerHost string, spec RootSpec, dest string) error {
	cli, err := docker.NewClient(dockerHost)
	if err != nil {
		return err
	}
	body, err := cli.ExportFilesystem(ctx, spec.Image, spec.Registry)
	if err != nil {
		return fmt.Errorf("%w: %v", driver.ErrImage, err)
	}
	defer body.Close()
	tarPath := dest + ".tar"
	f, err := os.Create(tarPath)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tarPath)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tarPath)
		return closeErr
	}
	defer os.Remove(tarPath)
	if err := makeExt4(ctx, dest, spec.DiskMB); err != nil {
		return err
	}
	return withMount(ctx, dest, func(mnt string) error {
		tf, err := os.Open(tarPath)
		if err != nil {
			return err
		}
		defer tf.Close()
		if err := extractTar(tf, mnt); err != nil {
			return err
		}
		// The image decides what is on this mount, symlinks with absolute
		// targets included, and the server writes here as root. Every write
		// below refuses to follow a link, or it would land on the host.
		agent, err := os.Open(spec.Agent)
		if err != nil {
			return err
		}
		defer agent.Close()
		if err := writeInRoot(mnt, "opt/sbx/sbx-agent", agent, 0o755); err != nil {
			return err
		}
		return mkdirInRoot(mnt, "workspace")
	})
}

func makeExt4(ctx context.Context, path string, mb int) error {
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		return fmt.Errorf("%w: mkfs.ext4 is required to build a rootfs (%v)", driver.ErrRuntimeUnavailable, err)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := f.Truncate(int64(mb) << 20); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// Pin features the Firecracker 6.1 guest kernel mounts. Newer mkfs defaults
	// (metadata_csum_seed, orphan_file) are not in every CI vmlinux.
	out, err := exec.CommandContext(ctx, "mkfs.ext4", "-F", "-L", "sbx", "-O", "^metadata_csum_seed,^orphan_file", path).CombinedOutput()
	if err != nil {
		out, err = exec.CommandContext(ctx, "mkfs.ext4", "-F", "-L", "sbx", path).CombinedOutput()
	}
	if err != nil {
		return fmt.Errorf("mkfs.ext4: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func withMount(ctx context.Context, disk string, fn func(string) error) error {
	mnt, err := os.MkdirTemp("", "sbx-mnt-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mnt)
	cmd := exec.CommandContext(ctx, "mount", "-o", "loop", disk, mnt)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: mount loop: %v (%s)", driver.ErrRuntimeUnavailable, err, strings.TrimSpace(string(out)))
	}
	defer exec.Command("umount", mnt).Run()
	return fn(mnt)
}

func inject(ctx context.Context, disk string, guest []byte) error {
	if len(guest) == 0 {
		return nil
	}
	return withMount(ctx, disk, func(mnt string) error {
		if err := writeInRoot(mnt, "opt/sbx/guest.json", bytes.NewReader(guest), 0o600); err != nil {
			return err
		}
		// Images often ship this path as a symlink to a runtime file that is
		// not in the export. writeInRoot replaces the link, never its target.
		return writeInRoot(mnt, "etc/resolv.conf", strings.NewReader("nameserver 1.1.1.1\nnameserver 8.8.8.8\n"), 0o644)
	})
}

// mkdirInRoot creates rel and its parents under root. It fails instead of
// following a symlink, so a path the image controls cannot leave root.
func mkdirInRoot(root, rel string) error {
	_, err := parentInRoot(root, filepath.Join(rel, "x"))
	return err
}

// parentInRoot creates the parent directories of rel under root and returns
// the full path of rel. A parent that is a symlink or not a directory is an
// error: on a mounted image it could point anywhere on the host.
func parentInRoot(root, rel string) (string, error) {
	rel = filepath.Clean(rel)
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: path %q leaves the image root", driver.ErrImage, rel)
	}
	cur := root
	dir := filepath.Dir(rel)
	if dir != "." {
		for _, part := range strings.Split(dir, string(os.PathSeparator)) {
			cur = filepath.Join(cur, part)
			st, err := os.Lstat(cur)
			if os.IsNotExist(err) {
				if err := os.Mkdir(cur, 0o755); err != nil {
					return "", err
				}
				continue
			}
			if err != nil {
				return "", err
			}
			if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
				return "", fmt.Errorf("%w: image path /%s is a symlink or not a directory", driver.ErrImage, strings.TrimPrefix(cur, root+string(os.PathSeparator)))
			}
		}
	}
	return filepath.Join(root, rel), nil
}

// writeInRoot writes r to rel under root. An existing symlink at rel is
// removed rather than followed, and O_NOFOLLOW closes the gap after that.
func writeInRoot(root, rel string, r io.Reader, mode os.FileMode) error {
	target, err := parentInRoot(root, rel)
	if err != nil {
		return err
	}
	if st, err := os.Lstat(target); err == nil && !st.Mode().IsRegular() {
		if st.IsDir() {
			return fmt.Errorf("%w: image path /%s is a directory", driver.ErrImage, rel)
		}
		if err := os.Remove(target); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	// OpenFile applies mode only to a new file.
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func extractTar(r io.Reader, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(r)
	var links [][2]string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target, ok := tarPath(dest, hdr.Name)
		if !ok {
			continue
		}
		rel := strings.TrimPrefix(target, dest+string(os.PathSeparator))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := mkdirInRoot(dest, rel); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := writeInRoot(dest, rel, tr, os.FileMode(hdr.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if _, err := parentInRoot(dest, rel); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			source, ok := tarPath(dest, hdr.Linkname)
			if !ok {
				continue
			}
			if _, err := parentInRoot(dest, rel); err != nil {
				return err
			}
			links = append(links, [2]string{source, target})
		}
	}
	for _, pair := range links {
		// Check again at link time: a later entry may have swapped a parent
		// for a symlink.
		if _, err := parentInRoot(dest, strings.TrimPrefix(pair[0], dest+string(os.PathSeparator))); err != nil {
			return err
		}
		if _, err := parentInRoot(dest, strings.TrimPrefix(pair[1], dest+string(os.PathSeparator))); err != nil {
			return err
		}
		if st, err := os.Lstat(pair[0]); err != nil || !st.Mode().IsRegular() {
			continue
		}
		_ = os.Remove(pair[1])
		if err := os.Link(pair[0], pair[1]); err != nil {
			return err
		}
	}
	return nil
}

func tarPath(dest, name string) (string, bool) {
	name = filepath.Clean(name)
	if name == "." || strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
		return "", false
	}
	target := filepath.Join(dest, name)
	if !strings.HasPrefix(target, dest+string(os.PathSeparator)) && target != dest {
		return "", false
	}
	return target, true
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
