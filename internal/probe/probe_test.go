package probe

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKVMDoesNotPanic(t *testing.T) {
	ok, detail := KVM()
	if detail == "" {
		t.Fatal("expected a detail string")
	}
	if ok && detail == "" {
		t.Fatal(detail)
	}
}

func TestWhichAndFile(t *testing.T) {
	if _, ok := Which(""); ok {
		t.Fatal("empty name")
	}
	if _, ok := Which("sbx-not-a-real-binary"); ok {
		t.Fatal("unexpected hit")
	}
	if _, ok := Which("has space"); ok {
		t.Fatal("space")
	}
	goBin, ok := Which("go")
	if !ok || goBin == "" {
		t.Fatal("go should be on PATH in tests")
	}
	dir := t.TempDir()
	if File(dir) {
		t.Fatal("directory is not a file")
	}
	path := filepath.Join(dir, "k")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !File(path) {
		t.Fatal("expected file")
	}
	if File("") || File(filepath.Join(dir, "missing")) {
		t.Fatal("missing file")
	}
}
