package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// A sandbox controls the archive sbx cp extracts. Symlinks that each look
// inside the destination can chain to a path outside it.
func TestUnpackRefusesSymlinkChains(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "a", "out")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(h *tar.Header, body string) {
		h.Size = int64(len(body))
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add(&tar.Header{Name: "s1", Typeflag: tar.TypeSymlink, Linkname: "."}, "")
	add(&tar.Header{Name: "up1", Typeflag: tar.TypeSymlink, Linkname: "s1/.."}, "")
	add(&tar.Header{Name: "up2", Typeflag: tar.TypeSymlink, Linkname: "up1/.."}, "")
	add(&tar.Header{Name: "f", Typeflag: tar.TypeSymlink, Linkname: "up2/pwned.txt"}, "")
	add(&tar.Header{Name: "f", Typeflag: tar.TypeReg, Mode: 0o644}, "owned\n")
	add(&tar.Header{Name: "kept.txt", Typeflag: tar.TypeReg, Mode: 0o644}, "ok\n")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	_ = unpack(&buf, dst)
	if _, err := os.Stat(filepath.Join(root, "pwned.txt")); err == nil {
		t.Fatal("unpack wrote outside the destination")
	}
	for _, dir := range []string{root, filepath.Join(root, "a")} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.Name() != "a" && e.Name() != "out" {
				t.Fatalf("unexpected %s in %s", e.Name(), dir)
			}
		}
	}

	// A destination that already holds a symlink is not written through.
	outside := filepath.Join(root, "victim")
	if err := os.WriteFile(outside, []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst2 := filepath.Join(root, "b")
	if err := os.MkdirAll(dst2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dst2, "f")); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	gz = gzip.NewWriter(&buf)
	tw = tar.NewWriter(gz)
	add(&tar.Header{Name: "f", Typeflag: tar.TypeReg, Mode: 0o644}, "owned\n")
	_ = tw.Close()
	_ = gz.Close()
	if err := unpack(&buf, dst2); err == nil {
		t.Fatal("unpack wrote through an existing symlink")
	}
	if got, _ := os.ReadFile(outside); string(got) != "safe" {
		t.Fatalf("victim overwritten: %q", got)
	}
}
