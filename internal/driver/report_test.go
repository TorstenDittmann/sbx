package driver

import "testing"

func TestDescribeAndKnown(t *testing.T) {
	names := Known()
	if len(names) < 4 || names[0] != "docker" || names[len(names)-1] != "firecracker" {
		t.Fatalf("%v", names)
	}
	info := Describe("docker", Caps{
		Pause: true, IsolationLevel: "container", Exec: true, Files: true,
		Networks: []NetMode{NetNone, NetFull},
	}, true, "ready")
	if !info.Available || info.Isolation != "container" || len(info.Networks) != 2 || info.Requirements == nil {
		t.Fatalf("%+v", info)
	}
}
