package docker

import (
	"reflect"
	"testing"
)

func TestSnapshotConfigDropsSandboxState(t *testing.T) {
	base := ImageMeta{
		Env:        []string{"PATH=/bin", "LANG=C"},
		Labels:     map[string]string{"org.opencontainers.image.title": "node"},
		Entrypoint: []string{"docker-entrypoint.sh"},
		Cmd:        []string{"node"},
		User:       "node",
		WorkingDir: "/srv",
	}
	ctn := ContainerState{
		Env:    []string{"PATH=/bin", "LANG=C", "HOME=/workspace", "TOKEN=s3cret", agentTokenEnv + "=abc"},
		Labels: map[string]string{labelManaged: "true", labelID: "sbx_1", "team": "a"},
	}
	cfg := snapshotConfig(base, ctn, "/var/lib/sbx-snapshot/snap_1")
	wantEnv := []string{"PATH=/bin", "LANG=C", "HOME=", agentTokenEnv + "=", "TOKEN="}
	if !reflect.DeepEqual(cfg.Env, wantEnv) {
		t.Fatalf("env %q", cfg.Env)
	}
	wantLabels := map[string]string{
		"org.opencontainers.image.title": "node", labelManaged: "", labelID: "", "team": "",
		labelSnapshotWorkspace: "/var/lib/sbx-snapshot/snap_1",
	}
	if !reflect.DeepEqual(cfg.Labels, wantLabels) {
		t.Fatalf("labels %v", cfg.Labels)
	}
	if cfg.User != "node" || cfg.WorkingDir != "/srv" || cfg.Cmd[0] != "node" || cfg.Entrypoint[0] != "docker-entrypoint.sh" {
		t.Fatalf("%+v", cfg)
	}
	// A snapshot without a workspace copy clears the label it inherited.
	base.Labels = cfg.Labels
	if got := snapshotConfig(base, ctn, "").Labels[labelSnapshotWorkspace]; got != "" {
		t.Fatalf("seed %q", got)
	}
}
