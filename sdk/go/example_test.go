package sbx_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	sbx "github.com/TorstenDittmann/sbx/sdk/go"
)

func Example() {
	ctx := context.Background()
	client, err := sbx.NewClient() // SBX_URL and SBX_API_KEY
	if err != nil {
		log.Fatal(err)
	}
	box, err := client.CreateSandbox(ctx, sbx.CreateSandboxOptions{Template: "base", TTL: 5 * time.Minute})
	if err != nil {
		log.Fatal(err)
	}
	defer box.Kill(context.Background())

	result, err := box.Exec(ctx, []string{"echo", "hello"}, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(result.Stdout)
}

func ExampleClient_ExecStream() {
	ctx := context.Background()
	client, _ := sbx.NewClient()
	box := client.Sandbox("sbx_0123456789abcdef")
	for ev, err := range box.ExecStream(ctx, sbx.Shell("pip install requests"), &sbx.ExecOptions{Timeout: 2 * time.Minute}) {
		if err != nil {
			log.Fatal(err)
		}
		switch ev.Type {
		case sbx.ExecStdout, sbx.ExecStderr:
			fmt.Print(ev.Data)
		case sbx.ExecExit:
			fmt.Println("exit", ev.ExitCode)
		}
	}
}

func ExampleTemplate() {
	ctx := context.Background()
	client, _ := sbx.NewClient()
	base := sbx.FromImage("node:22-bookworm-slim").
		WithPackages("ffmpeg", "git").
		WithEnv(map[string]string{"NODE_ENV": "production"}). // build time only
		Workdir("/app").
		Run("npm init -y")
	box, err := client.CreateSandbox(ctx, sbx.CreateSandboxOptions{From: base, TTL: 5 * time.Minute})
	if err != nil {
		log.Fatal(err)
	}
	defer box.Kill(context.Background())
}

func ExampleClient_Events() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, _ := sbx.NewClient()
	for ev, err := range client.Events(ctx, sbx.EventsOptions{}) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(ev.ID, ev.Type, ev.SandboxID, ev.Data.Reason)
	}
}

func ExampleClient_ConnectTerminal() {
	ctx := context.Background()
	client, _ := sbx.NewClient()
	box := client.Sandbox("sbx_0123456789abcdef")
	term, err := box.StartTerminal(ctx, []string{"sh"}, &sbx.TerminalOptions{Cols: 120, Rows: 32})
	if err != nil {
		log.Fatal(err)
	}
	session, err := box.ConnectTerminal(ctx, term.ID)
	if err != nil {
		log.Fatal(err)
	}
	defer session.Close()
	session.Write([]byte("echo hello; exit\n"))
	buf := make([]byte, 4096)
	for {
		n, err := session.Read(buf)
		os.Stdout.Write(buf[:n])
		if err != nil {
			break
		}
	}
	code, _ := session.ExitCode()
	fmt.Println("exit", code)
}
