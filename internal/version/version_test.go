package version

import "testing"

func TestFormat(t *testing.T) {
	version, commit, date := Version, Commit, Date
	t.Cleanup(func() {
		Version, Commit, Date = version, commit, date
	})

	Version, Commit, Date = "1.2.3", "abc123", "2026-01-02T03:04:05Z"
	got := Format()
	want := "sbx 1.2.3\ncommit abc123\nbuilt 2026-01-02T03:04:05Z\n"
	if got != want {
		t.Fatalf("format:\n%s", got)
	}

	Commit, Date = "", ""
	if got := Format(); got != "sbx 1.2.3\n" {
		t.Fatalf("format without build info:\n%s", got)
	}
}

func TestDefaultFormat(t *testing.T) {
	if Version == "" {
		t.Fatal("empty version")
	}
	prefix := "sbx " + Version + "\n"
	got := Format()
	if len(got) < len(prefix) || got[:len(prefix)] != prefix {
		t.Fatalf("default format:\n%s", got)
	}
}
