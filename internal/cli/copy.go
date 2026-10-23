package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const cpUsage = "usage: sbx cp SRC DST, where one side is ID:PATH (a relative PATH is under /workspace, - is stdin or stdout)"

// splitRemote reports whether arg is ID:PATH. A prefix with a slash, or a
// single letter (a Windows drive), is a local path.
func splitRemote(arg string) (id, p string, ok bool) {
	id, p, ok = strings.Cut(arg, ":")
	if !ok || len(id) < 2 || strings.ContainsAny(id, `/\.`) {
		return "", "", false
	}
	if p == "" {
		p = "/workspace"
	}
	return id, remotePath(p), true
}

func copyCmd(args []string) error {
	fs := newFlags("cp")
	conn := connFlags(fs)
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return errors.New(cpUsage)
	}
	srcID, srcPath, srcRemote := splitRemote(pos[0])
	dstID, dstPath, dstRemote := splitRemote(pos[1])
	if srcRemote == dstRemote {
		return errors.New(cpUsage)
	}
	c, err := conn()
	if err != nil {
		return err
	}
	if dstRemote {
		return c.upload(pos[0], dstID, dstPath, strings.HasSuffix(pos[1], "/"))
	}
	return c.download(srcID, srcPath, pos[1])
}

// remoteStat finds p by listing its parent, which works with and without
// sbx-agent. ok is false when p does not exist.
func (c *client) remoteStat(id, p string) (fileEntry, bool, error) {
	if p == "/" {
		return fileEntry{Name: "/", Path: "/", Type: "dir"}, true, nil
	}
	var out struct {
		Entries []fileEntry `json:"entries"`
	}
	err := c.json(http.MethodGet, sandboxPath(id, "files"), url.Values{"path": {path.Dir(p)}}, nil, &out)
	if err != nil {
		// A parent that does not exist comes back as a driver error; a
		// missing sandbox or a bad key is reported as is.
		var ae *apiError
		if errors.As(err, &ae) && ae.Code == "driver" {
			return fileEntry{}, false, nil
		}
		return fileEntry{}, false, err
	}
	for _, e := range out.Entries {
		if e.Name == path.Base(p) {
			return e, true, nil
		}
	}
	return fileEntry{}, false, nil
}

func (c *client) upload(src, id, dst string, intoDir bool) error {
	target, isDir, err := c.uploadTarget(id, dst, intoDir)
	if err != nil {
		return err
	}
	if src == "-" {
		if isDir {
			return fmt.Errorf("%s is a directory; name the file to write", dst)
		}
		return c.putFile(id, target, stdin, 0)
	}
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if isDir {
		target = path.Join(target, filepath.Base(src))
	}
	if !st.IsDir() {
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()
		return c.putFile(id, target, f, st.Mode().Perm())
	}
	// A directory is packed under the target's own name and unpacked in
	// its parent, so the parent is the only directory that must exist.
	var buf bytes.Buffer
	if err := packDir(&buf, src, path.Base(target)); err != nil {
		return err
	}
	return c.send(http.MethodPost, sandboxPath(id, "files", "archive"), url.Values{"path": {path.Dir(target)}}, &buf, "application/gzip")
}

// uploadTarget reports whether dst is an existing directory to copy into.
func (c *client) uploadTarget(id, dst string, intoDir bool) (string, bool, error) {
	e, ok, err := c.remoteStat(id, dst)
	if err != nil {
		return "", false, err
	}
	if ok && e.Type == "dir" {
		return dst, true, nil
	}
	if intoDir {
		return "", false, fmt.Errorf("%s is not a directory in the sandbox", dst)
	}
	return dst, false, nil
}

func (c *client) putFile(id, p string, r io.Reader, mode os.FileMode) error {
	q := url.Values{"path": {p}}
	if mode != 0 {
		q.Set("mode", fmt.Sprintf("%o", mode))
	}
	return c.send(http.MethodPut, sandboxPath(id, "files", "content"), q, r, "application/octet-stream")
}

func (c *client) download(id, src, dst string) error {
	e, ok, err := c.remoteStat(id, src)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s does not exist in the sandbox", src)
	}
	if e.Type != "dir" {
		resp, err := c.do(http.MethodGet, sandboxPath(id, "files", "content"), url.Values{"path": {src}}, nil, "")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if dst == "-" {
			_, err = io.Copy(stdout, resp.Body)
			return err
		}
		if st, err := os.Stat(dst); err == nil && st.IsDir() {
			dst = filepath.Join(dst, path.Base(src))
		}
		mode := os.FileMode(0o644)
		if m, err := parseOctal(e.Mode); err == nil && m != 0 {
			mode = m
		}
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, resp.Body); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	if dst == "-" {
		return errors.New("a directory cannot be written to stdout")
	}
	if st, err := os.Stat(dst); err == nil && st.IsDir() {
		dst = filepath.Join(dst, path.Base(src))
	}
	resp, err := c.do(http.MethodGet, sandboxPath(id, "files", "archive"), url.Values{"path": {src}}, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return unpack(resp.Body, dst)
}

func parseOctal(s string) (os.FileMode, error) {
	var m uint32
	_, err := fmt.Sscanf(s, "%o", &m)
	return os.FileMode(m).Perm(), err
}

// packDir writes a gzip tar of dir with every entry under name/.
func packDir(w io.Writer, dir, name string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		hdr.Name = path.Join(name, filepath.ToSlash(rel))
		if d.IsDir() {
			hdr.Name += "/"
		}
		hdr.Uname, hdr.Gname, hdr.Uid, hdr.Gid = "", "", 0, 0
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// unpack extracts a gzip tar into dst. Entries and symlink targets that
// would land outside dst are rejected.
func unpack(r io.Reader, dst string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("invalid archive: %w", err)
	}
	defer gz.Close()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(strings.TrimPrefix(hdr.Name, "/"))
		if name == "." {
			continue
		}
		if name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("archive entry %q escapes the destination", hdr.Name)
		}
		if throughSymlink(dst, name) {
			return fmt.Errorf("archive entry %q is inside a symlinked directory", hdr.Name)
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		mode := os.FileMode(hdr.Mode).Perm()
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, mode|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			resolved := path.Clean(path.Join(path.Dir(name), hdr.Linkname))
			if path.IsAbs(hdr.Linkname) || resolved == ".." || strings.HasPrefix(resolved, "../") {
				return fmt.Errorf("archive symlink %q points outside the destination", hdr.Name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
}

// throughSymlink reports whether a parent directory of name inside dst is a
// symlink, which an archive could use to write outside dst.
func throughSymlink(dst, name string) bool {
	dir := dst
	parts := strings.Split(path.Dir(name), "/")
	for _, part := range parts {
		if part == "." {
			continue
		}
		dir = filepath.Join(dir, part)
		st, err := os.Lstat(dir)
		if err != nil {
			return false
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}
