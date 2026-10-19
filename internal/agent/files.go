package agent

import (
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

func cleanAbs(p string) (string, error) {
	if p == "" || strings.ContainsRune(p, 0) || !strings.HasPrefix(p, "/") {
		return "", errors.New("path must be absolute")
	}
	c := path.Clean(p)
	if !strings.HasPrefix(c, "/") {
		return "", errors.New("path must be absolute")
	}
	return c, nil
}

func refuseRemove(p string) error {
	switch p {
	case "/", "/workspace", "/tmp":
		return errors.New("refusing to delete " + p)
	default:
		return nil
	}
}

func statInfo(p string) (FileInfo, error) {
	st, err := os.Lstat(p)
	if err != nil {
		return FileInfo{}, err
	}
	return infoFrom(p, st), nil
}

func infoFrom(p string, st os.FileInfo) FileInfo {
	kind := "file"
	mode := st.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		kind = "symlink"
	case mode.IsDir():
		kind = "dir"
	}
	return FileInfo{
		Name: path.Base(p), Path: p, Type: kind, Size: st.Size(),
		Mode: uint32(mode.Perm()), Mtime: st.ModTime().UTC().UnixMilli(),
	}
}

func listDir(dir string) ([]FileInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, infoFrom(path.Join(dir, e.Name()), info))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func writeAtomic(dest string, r io.Reader, mode uint32) error {
	if mode == 0 {
		mode = 0o644
	}
	if err := os.MkdirAll(path.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".sbx-tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(mode))
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if err := os.Chmod(tmp, os.FileMode(mode)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

type meta struct {
	size  int64
	mtime int64
	mode  os.FileMode
}

func snapshot(root string, recursive bool) (map[string]meta, error) {
	st, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	out := map[string]meta{}
	if !st.IsDir() {
		out[root] = meta{size: st.Size(), mtime: st.ModTime().UnixNano(), mode: st.Mode()}
		return out, nil
	}
	if !recursive {
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				continue
			}
			full := path.Join(root, e.Name())
			out[full] = meta{size: info.Size(), mtime: info.ModTime().UnixNano(), mode: info.Mode()}
		}
		return out, nil
	}
	count := 0
	err = walk(root, &count, 8000, out)
	return out, err
}

func walk(dir string, count *int, limit int, out map[string]meta) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		*count++
		if *count > limit {
			return errors.New("watch tree is too large")
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		full := path.Join(dir, e.Name())
		out[full] = meta{size: info.Size(), mtime: info.ModTime().UnixNano(), mode: info.Mode()}
		if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			if err := walk(full, count, limit, out); err != nil {
				return err
			}
		}
	}
	return nil
}

func diffSnapshot(prev, cur map[string]meta) []FileEvent {
	var ev []FileEvent
	for p := range cur {
		old, ok := prev[p]
		if !ok {
			ev = append(ev, FileEvent{Type: "create", Path: p})
			continue
		}
		if old.size != cur[p].size || old.mtime != cur[p].mtime || old.mode != cur[p].mode {
			ev = append(ev, FileEvent{Type: "write", Path: p})
		}
	}
	for p := range prev {
		if _, ok := cur[p]; !ok {
			ev = append(ev, FileEvent{Type: "remove", Path: p})
		}
	}
	sort.Slice(ev, func(i, j int) bool {
		if ev[i].Path == ev[j].Path {
			return ev[i].Type < ev[j].Type
		}
		return ev[i].Path < ev[j].Path
	})
	return ev
}

func readLimited(p string, max int) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, errors.New("path is a directory")
	}
	if max > 0 && st.Size() > int64(max) {
		return nil, errors.New("file exceeds the size limit")
	}
	return io.ReadAll(f)
}
