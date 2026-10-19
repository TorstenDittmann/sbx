package firecracker

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
}

func (d *Driver) buildRoot(ctx context.Context, spec RootSpec) error {
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

func cacheKey(image, agentSum string, diskMB int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\n%s\n%d", image, agentSum, diskMB)))
	return hex.EncodeToString(h[:])
}

func buildOCI(ctx context.Context, dockerHost string, spec RootSpec, dest string) error {
	cli, err := docker.NewClient(dockerHost)
	if err != nil {
		return err
	}
	body, err := cli.ExportFilesystem(ctx, spec.Image)
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
		agentDest := filepath.Join(mnt, "opt", "sbx", "sbx-agent")
		if err := os.MkdirAll(filepath.Dir(agentDest), 0o755); err != nil {
			return err
		}
		if err := copyFile(spec.Agent, agentDest); err != nil {
			return err
		}
		if err := os.Chmod(agentDest, 0o755); err != nil {
			return err
		}
		return os.MkdirAll(filepath.Join(mnt, "workspace"), 0o755)
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
	cmd := exec.CommandContext(ctx, "mkfs.ext4", "-F", "-L", "sbx", path)
	out, err := cmd.CombinedOutput()
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
		dir := filepath.Join(mnt, "opt", "sbx")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "guest.json"), guest, 0o600); err != nil {
			return err
		}
		resolv := filepath.Join(mnt, "etc", "resolv.conf")
		if err := os.MkdirAll(filepath.Dir(resolv), 0o755); err != nil {
			return err
		}
		// Images often ship this path as a symlink to a runtime file that is not in the export.
		_ = os.Remove(resolv)
		return os.WriteFile(resolv, []byte("nameserver 1.1.1.1\nnameserver 8.8.8.8\n"), 0o644)
	})
}

func extractTar(r io.Reader, dest string) error {
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
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)|0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
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
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			links = append(links, [2]string{source, target})
		}
	}
	for _, pair := range links {
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
