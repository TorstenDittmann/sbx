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

// Standard tar writers pad the archive with zeros to a full record after the
// end-of-archive marker. The agent must accept that padding instead of
// closing the stream early and failing the upload.
func TestArchiveTrailingRecordPadding(t *testing.T) {
	c, cleanup := startAgent(t, "tok")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw := tarBytes(t,
		&tar.Header{Name: "a/f", Mode: 0o644, Size: 3, ModTime: time.Unix(10, 0)},
		[]byte("hi\n"),
	)
	// 10240 is the default record size; 65536 (blocking factor 128) spans
	// several data frames.
	for _, record := range []int{10240, 65536} {
		dir := t.TempDir()
		if err := c.ExtractArchive(ctx, dir, bytes.NewReader(padRecord(raw, record)), 1<<20); err != nil {
			t.Fatalf("record %d: %v", record, err)
		}
		body, err := os.ReadFile(filepath.Join(dir, "a", "f"))
		if err != nil || string(body) != "hi\n" {
			t.Fatalf("record %d: %q %v", record, body, err)
		}
	}

	junk := append(append([]byte(nil), raw...), make([]byte, maxArchiveTrailer+1)...)
	if err := extractTar(t.TempDir(), bytes.NewReader(junk), 1<<20); err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Fatal(err)
	}
}

// git archive writes a pax global header holding the commit id before the
// first entry.
func TestArchiveGitPaxGlobalHeader(t *testing.T) {
	c, cleanup := startAgent(t, "tok")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name:       "pax_global_header",
		Typeflag:   tar.TypeXGlobalHeader,
		PAXRecords: map[string]string{"comment": "cb5479736c2f9ad5b6b0e6a6b1f1f5c3d2e1a0b9"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "src/", Typeflag: tar.TypeDir, Mode: 0o775, ModTime: time.Unix(10, 0)}); err != nil {
		t.Fatal(err)
	}
	body := []byte("package main\n")
	if err := tw.WriteHeader(&tar.Header{Name: "src/main.go", Mode: 0o664, Size: int64(len(body)), ModTime: time.Unix(10, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := c.ExtractArchive(ctx, dir, bytes.NewReader(padRecord(buf.Bytes(), 10240)), 1<<20); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "src", "main.go"))
	if err != nil || string(got) != string(body) {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "pax_global_header")); !os.IsNotExist(err) {
		t.Fatalf("global header was extracted: %v", err)
	}
}

func padRecord(raw []byte, record int) []byte {
	n := (len(raw) + record - 1) / record * record
	return append(append([]byte(nil), raw...), make([]byte, n-len(raw))...)
}
