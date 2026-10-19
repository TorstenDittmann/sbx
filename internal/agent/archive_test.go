package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArchiveRoundTripAndRejects(t *testing.T) {
	src := t.TempDir()
	if err := os.Mkdir(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(src, "sub", "link")); err != nil {
		t.Fatal(err)
	}
	packed, err := packGzip(src, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	names := tarNames(t, packed)
	if !names["sub/"] || !names["sub/a.txt"] || !names["sub/link"] {
		t.Fatalf("%v", names)
	}

	dest := t.TempDir()
	if err := extractTar(dest, gzipReader(t, packed), 1<<20); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dest, "sub", "a.txt"))
	if err != nil || string(body) != "hello" {
		t.Fatalf("%q %v", body, err)
	}
	link, err := os.Readlink(filepath.Join(dest, "sub", "link"))
	if err != nil || link != "a.txt" {
		t.Fatalf("link %q %v", link, err)
	}

	if _, err := packGzip(filepath.Join(src, "sub", "a.txt"), 1<<20); err == nil {
		t.Fatal("expected a file path to fail")
	}
	if _, err := packGzip(src, 8); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatal(err)
	}

	parent := t.TempDir()
	contained := filepath.Join(parent, "dest")
	if err := os.Mkdir(contained, 0o755); err != nil {
		t.Fatal(err)
	}
	escape := tarBytes(t, &tar.Header{Name: "../outside.txt", Mode: 0o644, Size: 1}, []byte("x"))
	if err := extractTar(contained, bytes.NewReader(escape), 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(parent, "outside.txt")); !os.IsNotExist(err) {
		t.Fatal("archive wrote outside the destination")
	}
	inside, err := os.ReadFile(filepath.Join(contained, "outside.txt"))
	if err != nil || string(inside) != "x" {
		t.Fatalf("%q %v", inside, err)
	}
	if _, err := archivePath(contained, "a\x00b"); err == nil {
		t.Fatal("expected a NUL path to fail")
	}

	linked := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(linked, "jump")); err != nil {
		t.Fatal(err)
	}
	through := tarBytes(t, &tar.Header{Name: "jump/evil.txt", Mode: 0o644, Size: 1}, []byte("z"))
	if err := extractTar(linked, bytes.NewReader(through), 1<<20); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatal(err)
	}

	fifo := tarBytes(t, &tar.Header{Name: "pipe", Mode: 0o644, Typeflag: tar.TypeFifo}, nil)
	if err := extractTar(t.TempDir(), bytes.NewReader(fifo), 1<<20); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatal(err)
	}

	big := tarBytes(t, &tar.Header{Name: "big.txt", Mode: 0o644, Size: 8}, []byte("01234567"))
	if err := extractTar(t.TempDir(), bytes.NewReader(big), 4); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatal(err)
	}

	sockDir := t.TempDir()
	ln, err := net.Listen("unix", filepath.Join(sockDir, "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if _, err := packGzip(sockDir, 1<<20); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatal(err)
	}
}

func TestArchiveProtocol(t *testing.T) {
	dir := t.TempDir()
	c, cleanup := startAgent(t, "tok")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw := tarBytes(t,
		&tar.Header{Name: "note.txt", Mode: 0o644, Size: 4, ModTime: time.Unix(10, 0)},
		[]byte("note"),
	)
	if err := c.ExtractArchive(ctx, dir, bytes.NewReader(raw), 1<<20); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "note.txt"))
	if err != nil || string(body) != "note" {
		t.Fatalf("%q %v", body, err)
	}
	packed, err := c.PackArchive(ctx, dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	names := tarNames(t, packed)
	if !names["note.txt"] {
		t.Fatalf("%v", names)
	}
	if err := c.ExtractArchive(ctx, dir, bytes.NewReader([]byte("not a tar")), 1<<20); err == nil {
		t.Fatal("expected invalid tar")
	}
	if _, err := c.PackArchive(ctx, filepath.Join(dir, "missing"), 1<<20); err == nil {
		t.Fatal("expected missing directory")
	}
}

func TestHardLinkWithinArchive(t *testing.T) {
	dest := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("same")
	if err := tw.WriteHeader(&tar.Header{Name: "a.txt", Mode: 0o644, Size: int64(len(body)), ModTime: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "b.txt", Typeflag: tar.TypeLink, Linkname: "a.txt", Mode: 0o644, ModTime: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractTar(dest, bytes.NewReader(buf.Bytes()), 1<<20); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "b.txt"))
	if err != nil || string(got) != "same" {
		t.Fatalf("%q %v", got, err)
	}
}

func tarBytes(t *testing.T, hdr *tar.Header, body []byte) []byte {
	t.Helper()
	if hdr.ModTime.IsZero() {
		hdr.ModTime = time.Unix(0, 0)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if len(body) > 0 {
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tarNames(t *testing.T, gz []byte) map[string]bool {
	t.Helper()
	tr := tar.NewReader(gzipReader(t, gz))
	out := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = true
	}
}

func gzipReader(t *testing.T, gz []byte) io.Reader {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}
