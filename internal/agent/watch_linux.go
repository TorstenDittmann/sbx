//go:build linux

package agent

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"golang.org/x/sys/unix"
)

// errInotifyUnavailable tells watchFiles to fall back to polling.
var errInotifyUnavailable = errors.New("inotify unavailable")

// maxInotifyWatches caps directory watches. Tests lower it.
var maxInotifyWatches = 8000

// inotifyInit opens an inotify fd. Tests replace it to force the poll fallback.
var inotifyInit = func() (int, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return -1, fmt.Errorf("%w: %s", errInotifyUnavailable, err)
	}
	return fd, nil
}

const inotifyDirMask = unix.IN_CREATE | unix.IN_DELETE | unix.IN_MODIFY |
	unix.IN_MOVED_FROM | unix.IN_MOVED_TO | unix.IN_ATTRIB |
	unix.IN_DELETE_SELF | unix.IN_MOVE_SELF |
	unix.IN_ONLYDIR | unix.IN_DONT_FOLLOW

// inotifyWatch is one file-watch stream.
type inotifyWatch struct {
	fd        int
	userRoot  string
	dir       string
	file      string
	recursive bool
	wd        map[int]string
	seen      map[string]int
	known     map[string]struct{}
	skip      map[string]struct{}
	count     int
}

func (a *Agent) watchFiles(ctx context.Context, root string, recursive bool, ready func() error, emit func(FileEvent) error) error {
	err := a.inotifyWatch(ctx, root, recursive, ready, emit)
	if errors.Is(err, errInotifyUnavailable) {
		return a.pollWatch(ctx, root, recursive, ready, emit)
	}
	return err
}

func (a *Agent) inotifyWatch(ctx context.Context, root string, recursive bool, ready func() error, emit func(FileEvent) error) error {
	fd, err := inotifyInit()
	if err != nil {
		if errors.Is(err, errInotifyUnavailable) {
			return err
		}
		return fmt.Errorf("%w: %s", errInotifyUnavailable, err)
	}
	defer unix.Close(fd)
	w := &inotifyWatch{
		fd: fd, userRoot: root, recursive: recursive,
		wd: map[int]string{}, seen: map[string]int{}, skip: map[string]struct{}{},
	}
	if err := w.install(); err != nil {
		return err
	}
	if err := ready(); err != nil {
		return err
	}
	return w.loop(ctx, emit)
}

func (w *inotifyWatch) install() error {
	st, err := os.Lstat(w.userRoot)
	if err != nil {
		return err
	}
	known, err := knownPaths(w.userRoot, w.recursive)
	if err != nil {
		return err
	}
	w.known = known
	if !st.IsDir() {
		w.dir = path.Dir(w.userRoot)
		w.file = path.Base(w.userRoot)
		return w.add(w.dir, true)
	}
	w.dir = w.userRoot
	return w.addTree(w.userRoot, true)
}

func knownPaths(root string, recursive bool) (map[string]struct{}, error) {
	snap, err := snapshot(root, recursive)
	if err != nil {
		if errors.Is(err, errWatchTooLarge) {
			return map[string]struct{}{}, nil
		}
		return nil, err
	}
	out := make(map[string]struct{}, len(snap))
	for p := range snap {
		out[p] = struct{}{}
	}
	return out, nil
}

func (w *inotifyWatch) addTree(dir string, initial bool) error {
	if err := w.add(dir, initial); err != nil {
		return err
	}
	if !w.recursive {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		if err := w.addTree(path.Join(dir, e.Name()), initial); err != nil {
			return err
		}
	}
	return nil
}

func (w *inotifyWatch) add(dir string, initial bool) error {
	if _, ok := w.seen[dir]; ok {
		return nil
	}
	if w.count >= maxInotifyWatches {
		return errWatchTooLarge
	}
	wd, err := unix.InotifyAddWatch(w.fd, dir, inotifyDirMask)
	if err != nil {
		if initial && inotifyUnsupported(err) {
			return fmt.Errorf("%w: %s", errInotifyUnavailable, err)
		}
		if err == unix.ENOSPC {
			return errors.New("inotify watch limit reached")
		}
		return err
	}
	w.wd[wd] = dir
	w.seen[dir] = wd
	w.count++
	return nil
}

func inotifyUnsupported(err error) bool {
	return errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENOSPC)
}

// watchNewDir arms a directory created after the stream started and reports
// children that appeared before the watch was installed.
func (w *inotifyWatch) watchNewDir(dir string) ([]FileEvent, error) {
	if _, ok := w.seen[dir]; ok {
		return nil, nil
	}
	if err := w.add(dir, false); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []FileEvent
	for _, e := range entries {
		full := path.Join(dir, e.Name())
		if ev, ok := w.note(full, unix.IN_CREATE); ok {
			out = append(out, ev)
			w.skip[full] = struct{}{}
		}
		if e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			nested, err := w.watchNewDir(full)
			if err != nil {
				return nil, err
			}
			out = append(out, nested...)
		}
	}
	return out, nil
}

func (w *inotifyWatch) note(full string, mask uint32) (FileEvent, bool) {
	if strings.HasSuffix(path.Base(full), ".sbx-tmp") {
		return FileEvent{}, false
	}
	typ, ok := classifyInotify(mask)
	if !ok {
		return FileEvent{}, false
	}
	switch typ {
	case "create":
		if _, exists := w.known[full]; exists {
			typ = "write"
		}
		w.known[full] = struct{}{}
	case "remove":
		delete(w.known, full)
	case "write":
		w.known[full] = struct{}{}
	}
	return FileEvent{Type: typ, Path: full}, true
}

func classifyInotify(mask uint32) (string, bool) {
	switch {
	case mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0:
		return "create", true
	case mask&(unix.IN_DELETE|unix.IN_MOVED_FROM|unix.IN_DELETE_SELF|unix.IN_MOVE_SELF) != 0:
		return "remove", true
	case mask&(unix.IN_MODIFY|unix.IN_CLOSE_WRITE|unix.IN_ATTRIB) != 0:
		return "write", true
	default:
		return "", false
	}
}

func (w *inotifyWatch) loop(ctx context.Context, emit func(FileEvent) error) error {
	buf := make([]byte, 64*1024)
	for {
		if ctx.Err() != nil {
			return nil
		}
		n, err := unix.Read(w.fd, buf)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
				if err := waitInotify(ctx, w.fd); err != nil {
					if ctx.Err() != nil {
						return nil
					}
					return err
				}
				continue
			}
			return err
		}
		if n == 0 {
			continue
		}
		events, err := w.consume(buf[:n])
		if err != nil {
			return err
		}
		for _, ev := range coalesceWatch(events) {
			if err := emit(ev); err != nil {
				return err
			}
		}
	}
}

func waitInotify(ctx context.Context, fd int) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		_, err := unix.Poll(fds, 200)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if fds[0].Revents != 0 {
			return nil
		}
	}
}

func (w *inotifyWatch) consume(buf []byte) ([]FileEvent, error) {
	var out []FileEvent
	for len(buf) > 0 {
		if len(buf) < unix.SizeofInotifyEvent {
			return nil, errors.New("short inotify event")
		}
		wd := int(int32(binary.NativeEndian.Uint32(buf[0:4])))
		mask := binary.NativeEndian.Uint32(buf[4:8])
		nameLen := int(binary.NativeEndian.Uint32(buf[12:16]))
		total := unix.SizeofInotifyEvent + nameLen
		if nameLen < 0 || total < unix.SizeofInotifyEvent || total > len(buf) {
			return nil, errors.New("short inotify event")
		}
		nameBytes := buf[unix.SizeofInotifyEvent:total]
		buf = buf[total:]
		if i := indexByte(nameBytes, 0); i >= 0 {
			nameBytes = nameBytes[:i]
		}
		name := string(nameBytes)
		if mask&unix.IN_Q_OVERFLOW != 0 {
			return nil, errors.New("inotify queue overflow")
		}
		dir, ok := w.wd[wd]
		if mask&unix.IN_IGNORED != 0 && ok {
			delete(w.seen, dir)
			delete(w.wd, wd)
			if w.count > 0 {
				w.count--
			}
		}
		if !ok {
			continue
		}
		if name == "" {
			if mask&(unix.IN_DELETE_SELF|unix.IN_MOVE_SELF) != 0 {
				target := ""
				if dir == w.userRoot || (w.file != "" && dir == w.dir) {
					target = w.userRoot
				}
				if target != "" {
					if ev, keep := w.note(target, mask); keep {
						out = append(out, ev)
					}
				}
			}
			continue
		}
		if w.file != "" && name != w.file {
			continue
		}
		full := path.Join(dir, name)
		skipped := false
		if _, drop := w.skip[full]; drop && mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0 {
			delete(w.skip, full)
			skipped = true
		}
		if !skipped {
			if ev, keep := w.note(full, mask); keep {
				out = append(out, ev)
			}
		}
		if w.recursive && w.file == "" && mask&unix.IN_ISDIR != 0 && mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0 {
			extra, err := w.watchNewDir(full)
			if err != nil {
				return nil, err
			}
			out = append(out, extra...)
		}
	}
	return out, nil
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

func coalesceWatch(events []FileEvent) []FileEvent {
	if len(events) < 2 {
		return events
	}
	out := make([]FileEvent, 0, len(events))
	for _, ev := range events {
		if n := len(out); n > 0 {
			prev := out[n-1]
			if prev.Path == ev.Path && (prev.Type == ev.Type || (prev.Type == "create" && ev.Type == "write")) {
				continue
			}
		}
		out = append(out, ev)
	}
	return out
}
