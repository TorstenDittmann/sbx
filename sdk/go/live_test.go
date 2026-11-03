package sbx_test

import (
	"context"
	"os"
	"testing"
	"time"

	sbx "github.com/TorstenDittmann/sbx/sdk/go"
)

func TestLiveQuickstart(t *testing.T) {
	if os.Getenv("SBX_LIVE") != "1" {
		t.Skip("set SBX_LIVE=1 with a running server")
	}
	ctx := context.Background()
	client, err := sbx.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	box, err := client.CreateSandbox(ctx, sbx.CreateSandboxOptions{Template: "base", TTL: 3 * time.Minute, Env: map[string]string{"FOO": "bar"}})
	if err != nil {
		t.Fatal(err)
	}
	defer box.Kill(context.Background())
	result, err := box.Exec(ctx, sbx.Shell("echo $FOO"), &sbx.ExecOptions{Timeout: 20 * time.Second})
	if err != nil || result.ExitCode != 0 || result.Stdout != "bar\n" {
		t.Fatalf("%+v %v", result, err)
	}
	if err := box.Files.WriteText(ctx, "/workspace/hello.txt", "hi\n", 0); err != nil {
		t.Fatal(err)
	}
	if text, err := box.Files.ReadText(ctx, "/workspace/hello.txt"); err != nil || text != "hi\n" {
		t.Fatalf("%q %v", text, err)
	}
}
