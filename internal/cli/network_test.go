package cli

import (
	"strings"
	"testing"
)

func TestNetworkCommand(t *testing.T) {
	fd, out, _ := liveServer(t)
	if err := Run([]string{"create", "base", "--allow", "example.com,203.0.113.0/24", "--deny", "203.0.113.9", "--max-connections", "16"}); err != nil {
		t.Fatal(err)
	}
	sbID := strings.TrimSpace(out.String())
	spec, ok := fd.Spec(sbID)
	if !ok || len(spec.Policy.Allow) != 2 || len(spec.Policy.Deny) != 1 || spec.Policy.MaxConns != 16 {
		t.Fatalf("create policy = %+v", spec.Policy)
	}

	out.buf.Reset()
	if err := Run([]string{"network", sbID}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"allow", "example.com, 203.0.113.0/24", "203.0.113.9", "16", "unlimited"} {
		if !strings.Contains(got, want) {
			t.Fatalf("show is missing %q:\n%s", want, got)
		}
	}

	out.buf.Reset()
	if err := Run([]string{"network", sbID, "--allow", "api.example.com", "--clear-deny", "--egress-kbps", "512"}); err != nil {
		t.Fatal(err)
	}
	spec, _ = fd.Spec(sbID)
	if len(spec.Policy.Allow) != 1 || spec.Policy.Allow[0] != "api.example.com" || len(spec.Policy.Deny) != 0 || spec.Policy.EgressKbps != 512 || spec.Policy.MaxConns != 16 {
		t.Fatalf("updated policy = %+v", spec.Policy)
	}
	if !strings.Contains(out.String(), "512 kbit/s") {
		t.Fatalf("update output:\n%s", out.String())
	}

	if err := Run([]string{"network", sbID, "--mode", "full"}); err != nil {
		t.Fatal(err)
	}
	spec, _ = fd.Spec(sbID)
	if len(spec.Policy.Allow) != 0 {
		t.Fatalf("full should drop the allow list: %+v", spec.Policy)
	}
	if err := Run([]string{"network", sbID, "--allow", "a.example", "--clear-allow"}); err == nil {
		t.Fatal("conflicting flags accepted")
	}
	if err := Run([]string{"network"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("no id: %v", err)
	}
}
