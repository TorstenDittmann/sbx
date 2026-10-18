package id

import (
	"regexp"
	"testing"
)

func TestSandboxFormat(t *testing.T) {
	got := Sandbox()
	if !regexp.MustCompile(`^sbx_[0-9a-f]{16}$`).MatchString(got) {
		t.Fatalf("sandbox id %q", got)
	}
	if Sandbox() == got {
		t.Fatal("expected unique sandbox ids")
	}
}

func TestKeyHashStable(t *testing.T) {
	plain, hash, prefix := Key()
	if !regexp.MustCompile(`^sbx_[a-z2-7]{32}$`).MatchString(plain) {
		t.Fatalf("plaintext %q", plain)
	}
	if Hash(plain) != hash {
		t.Fatal("hash mismatch")
	}
	if prefix != plain[:12] {
		t.Fatalf("prefix %q", prefix)
	}
	other, _, _ := Key()
	if other == plain {
		t.Fatal("expected unique keys")
	}
}
