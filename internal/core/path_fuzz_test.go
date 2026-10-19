package core

import (
	"path"
	"strings"
	"testing"
)

func FuzzCleanPath(f *testing.F) {
	f.Add("/workspace")
	f.Add("/workspace/../etc/passwd")
	f.Add("relative")
	f.Add("/tmp/a\\b")
	f.Add("/ok/./file")
	f.Fuzz(func(t *testing.T, p string) {
		got, err := cleanPath(p)
		if err != nil {
			return
		}
		if got != path.Clean(p) || !strings.HasPrefix(got, "/") || strings.ContainsRune(got, 0) {
			t.Fatalf("%q -> %q", p, got)
		}
		for _, part := range strings.Split(got, "/") {
			if part == ".." {
				t.Fatalf("%q -> %q", p, got)
			}
		}
	})
}
