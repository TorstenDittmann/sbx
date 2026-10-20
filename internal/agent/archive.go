package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

const maxArchiveEntries = 100000

// maxArchiveTrailer bounds how much data after the end-of-archive marker is
// read and discarded. Writers pad to a record (10240 bytes by default, more
// with a larger blocking factor), so this leaves plenty of room.
const maxArchiveTrailer = 1 << 20

// extractTar unpacks a tar stream into dest. Paths that leave dest, or that
// pass through a symlink, are rejected.
func extractTar(dest string, r io.Reader, max int64) error {
	dest = path.Clean(dest)
	st, err := os.Lstat(dest)
	switch {
	case err == nil && st.Mode()&os.ModeSymlink != 0:
		return errors.New("destination is a symlink")
	case err == nil && !st.IsDir():
		return errors.New("destination is not a directory")
	case err != nil && !os.IsNotExist(err):
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(r)
	var written int64
	entries := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return drainTrailer(r)
		}
		if err != nil {
			return err
		}
		// git archive stores the commit id in a global header; there is
		// nothing to extract and archive/tar already applies per-file pax
		// records.
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		entries++
		if entries > maxArchiveEntries {
			return errors.New("archive has too many entries")
		}
		target, err := archivePath(dest, hdr.Name)
		if err != nil {
			return err
		}
		if err := rejectSymlinkParents(dest, target); err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := replaceNonDir(target); err != nil {
				return err
			}
			mode := tarPerm(hdr.Mode, 0o755)
			if err := os.MkdirAll(target, mode); err != nil {
				return err
			}
			if err := os.Chmod(target, mode); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if hdr.Size < 0 || (max > 0 && written+hdr.Size > max) {
				return errors.New("archive exceeds the size limit")
			}
			if err := os.MkdirAll(path.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeAtomic(target, io.LimitReader(tr, hdr.Size), uint32(tarPerm(hdr.Mode, 0o644))); err != nil {
				return err
			}
			written += hdr.Size
		case tar.TypeSymlink:
			if err := os.MkdirAll(path.Dir(target), 0o755); err != nil {
				return err
			}
			if err := replaceNonDir(target); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			src, err := archivePath(dest, hdr.Linkname)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(path.Dir(target), 0o755); err != nil {
				return err
			}
			if err := replaceNonDir(target); err != nil {
				return err
			}
			if err := os.Link(src, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported tar entry %q", hdr.Name)
		}
	}
}

// drainTrailer consumes the record padding after the end-of-archive marker so
// the sender can finish writing. More than maxArchiveTrailer bytes is
// treated as an error rather than read forever.
func drainTrailer(r io.Reader) error {
	n, err := io.Copy(io.Discard, io.LimitReader(r, maxArchiveTrailer+1))
	if err != nil {
		return err
	}
	if n > maxArchiveTrailer {
		return errors.New("archive has too much trailing data")
	}
	return nil
}

func archivePath(dest, name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) {
		return "", errors.New("invalid archive path")
	}
	clean := path.Clean("/" + name)
	dest = path.Clean(dest)
	if clean == "/" {
		return dest, nil
	}
	full := path.Join(dest, strings.TrimPrefix(clean, "/"))
	if !archiveWithin(dest, full) {
		return "", errors.New("archive path escapes the destination")
	}
	return full, nil
}

func archiveWithin(dest, full string) bool {
	dest = path.Clean(dest)
	full = path.Clean(full)
	if full == dest {
		return true
	}
	if dest == "/" {
		return strings.HasPrefix(full, "/")
	}
	return strings.HasPrefix(full, dest+"/")
}

func rejectSymlinkParents(dest, full string) error {
	dest = path.Clean(dest)
	full = path.Clean(full)
	if full == dest {
		return nil
	}
	if !archiveWithin(dest, full) {
		return errors.New("archive path escapes the destination")
	}
	rel := strings.TrimPrefix(full, dest+"/")
	if dest == "/" {
		rel = strings.TrimPrefix(full, "/")
	}
	cur := dest
	parts := strings.Split(rel, "/")
	if len(parts) == 0 {
		return nil
	}
	for _, part := range parts[:len(parts)-1] {
		cur = path.Join(cur, part)
		st, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return errors.New("archive path escapes through a symlink")
		}
	}
	return nil
}

func replaceNonDir(target string) error {
	st, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if st.IsDir() && st.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	return os.Remove(target)
}

func tarPerm(mode int64, fallback os.FileMode) os.FileMode {
	if mode == 0 {
		return fallback
	}
	return os.FileMode(mode) & os.ModePerm
}

// packGzip returns a gzip-compressed tar of the directory root.
func packGzip(root string, max int) ([]byte, error) {
	root = path.Clean(root)
	st, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("path is not a directory")
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	count := 0
	if err := writeTarTree(tw, root, root, &count); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if max > 0 && buf.Len() > max {
		return nil, errors.New("archive exceeds the size limit")
	}
	return buf.Bytes(), nil
}

func writeTarTree(tw *tar.Writer, root, dir string, count *int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		*count++
		if *count > maxArchiveEntries {
			return errors.New("archive has too many entries")
		}
		full := path.Join(dir, e.Name())
		st, err := os.Lstat(full)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(full, "/")
		if root != "/" {
			rel = strings.TrimPrefix(full, root+"/")
		}
		hdr := &tar.Header{
			Name:    rel,
			Mode:    int64(st.Mode().Perm()),
			ModTime: st.ModTime(),
		}
		switch {
		case st.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(full)
			if err != nil {
				return err
			}
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = link
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
		case st.IsDir():
			hdr.Name = rel + "/"
			hdr.Typeflag = tar.TypeDir
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if err := writeTarTree(tw, root, full, count); err != nil {
				return err
			}
		case st.Mode().IsRegular():
			hdr.Typeflag = tar.TypeReg
			hdr.Size = st.Size()
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			f, err := os.Open(full)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, f)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("unsupported file %s", rel)
		}
	}
	return nil
}

func (a *Agent) handleArchivePut(s *Stream, frame Frame) {
	var body struct {
		Path string `json:"path"`
		Max  int64  `json:"max"`
	}
	if err := json.Unmarshal(frame.Data, &body); err != nil {
		a.fail(s, err)
		return
	}
	dest, err := cleanAbs(body.Path)
	if err != nil {
		a.fail(s, err)
		return
	}
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		defer pr.Close()
		errc <- extractTar(dest, pr, body.Max)
	}()
	for {
		f, err := s.Recv(s.ctx)
		if err != nil {
			pw.CloseWithError(err)
			<-errc
			return
		}
		switch f.Type {
		case MsgData:
			if _, werr := pw.Write(f.Data); werr != nil {
				_ = pw.Close()
				cause := <-errc
				if cause == nil {
					cause = werr
				}
				a.fail(s, cause)
				return
			}
		case MsgStdinEOF:
			_ = pw.Close()
			if err := <-errc; err != nil {
				a.fail(s, err)
				return
			}
			_ = s.Send(MsgOK, []byte(`{}`))
			return
		default:
			pw.CloseWithError(errors.New("aborted"))
			<-errc
			a.fail(s, errors.New("aborted"))
			return
		}
	}
}

func (a *Agent) handleArchiveGet(s *Stream, frame Frame) {
	var body struct {
		Path string `json:"path"`
		Max  int    `json:"max"`
	}
	if err := json.Unmarshal(frame.Data, &body); err != nil {
		a.fail(s, err)
		return
	}
	dir, err := cleanAbs(body.Path)
	if err != nil {
		a.fail(s, err)
		return
	}
	data, err := packGzip(dir, body.Max)
	if err != nil {
		a.fail(s, err)
		return
	}
	for len(data) > 0 {
		n := len(data)
		if n > ChunkSize {
			n = ChunkSize
		}
		if err := s.Send(MsgData, data[:n]); err != nil {
			return
		}
		data = data[n:]
	}
	_ = s.Send(MsgOK, []byte(`{}`))
}
