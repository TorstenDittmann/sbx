# Go SDK

Go client for [sbx](https://github.com/TorstenDittmann/sbx), a single-binary sandbox server.

It covers the same API as the [TypeScript](/docs/sdk/) and [Python](/docs/python-sdk/) SDKs with Go conventions: every call takes a `context.Context`, options are structs, and streams are range-over-func iterators. It uses only the standard library and needs Go 1.23+.

```bash
go get github.com/TorstenDittmann/sbx/sdk/go
```

The import path ends in `/go`; the package name is `sbx`.

```go
import sbx "github.com/TorstenDittmann/sbx/sdk/go"

client, err := sbx.NewClient() // SBX_URL, SBX_API_KEY
if err != nil {
	log.Fatal(err)
}
box, err := client.CreateSandbox(ctx, sbx.CreateSandboxOptions{Template: "base", TTL: 5 * time.Minute})
if err != nil {
	log.Fatal(err)
}
defer box.Kill(context.Background())

result, err := box.Exec(ctx, []string{"echo", "hello"}, nil)
fmt.Print(result.Stdout)
```

`SBX_URL` defaults to `http://127.0.0.1:8080`. `SBX_API_KEY` is required unless you pass `sbx.WithAPIKey`. `sbx.WithBaseURL` and `sbx.WithHTTPClient` set the rest. Do not give the HTTP client a `Timeout`, since execs and streams run long; put a deadline on the context instead.

Durations are `time.Duration` and are rounded to whole seconds, so anything above 0 and below a second becomes 1. A zero value is omitted and the server default applies. `ExecOptions.Timeout: sbx.NoTimeout` sends 0, which lets a background process run until it exits.

Commands are `[]string` and run as is. `sbx.Shell("ls | wc -l")` wraps a script in `sh -lc`.

## Errors

API errors are `*sbx.Error` with `Status`, `Code` (`not_found`, `conflict`, `unsupported`, ...), and `Message`. `sbx.IsNotFound`, `sbx.IsConflict`, and `sbx.StatusCode` check them through wrapped errors.

```go
if _, err := client.GetSandbox(ctx, id); sbx.IsNotFound(err) {
	// gone
}
```

## Sandboxes

`CreateSandbox`, `GetSandbox`, and `ListSandboxes` return `*sbx.Sandbox`, which embeds `SandboxInfo` (`box.ID`, `box.Status`, `box.ExpiresAt`, ...) and wraps every per-sandbox call. `client.Sandbox(id)` makes a handle without a request. `Kill`, `Pause`, `Resume`, `SetTTL`, `SetSecrets`, and `Refresh` update the embedded info. Each call also exists on `Client` with the sandbox id as its first argument.

```go
box, err := client.CreateSandbox(ctx, sbx.CreateSandboxOptions{
	Image:   "python:3.12-slim",
	Labels:  map[string]string{"job": "42"},
	Secrets: map[string]string{"TOKEN": token}, // names come back in box.SecretKeys
	Network: &sbx.NetworkPolicy{Mode: sbx.NetworkAllow, Allow: []string{"pypi.org", "files.pythonhosted.org"}},
	Mounts:  []sbx.VolumeMount{{VolumeID: vol.ID, Path: "/workspace"}},
})
boxes, err := client.ListSandboxes(ctx, sbx.ListSandboxesOptions{Labels: map[string]string{"job": "42"}})
stats, err := box.Stats(ctx) // CPU, memory, disk, network, pids
snap, err := box.Snapshot(ctx, sbx.SnapshotOptions{Template: "after-setup"})
```

## Exec and streams

```go
box.Exec(ctx, sbx.Shell("ls -la | wc -l"), &sbx.ExecOptions{Cwd: "/workspace", Env: map[string]string{"A": "1"}, Timeout: 30 * time.Second})
box.Exec(ctx, []string{"wc", "-c"}, &sbx.ExecOptions{Stdin: []byte{1, 2, 3}})

for ev, err := range box.ExecStream(ctx, sbx.Shell("pip install requests"), nil) {
	if err != nil {
		return err
	}
	switch ev.Type {
	case sbx.ExecStdout, sbx.ExecStderr:
		fmt.Print(ev.Data)
	case sbx.ExecExit:
		fmt.Println("exit", ev.ExitCode)
	}
}
```

`ExecStream`, `StreamProcess`, `Files.Watch`, and `Events` return `iter.Seq2[T, error]`. Breaking out of the loop or cancelling the context closes the connection. An `error` exec event (such as a timeout) is yielded as an `ExecEvent` with `Code` and `Message`; a failed request or a dropped stream is yielded as an error.

`client.Events` yields lifecycle events (`sandbox.created`, `sandbox.expired`, `sandbox.oom`, `sandbox.lost`, ...) instead of polling. It reconnects after a dropped connection and resumes after the last id. Set `After` to resume from an id you stored (a pointer to 0 replays what the server keeps), `Sandbox` for one sandbox, and `NoReconnect` to stop at the first drop. `box.Events` is the same for one sandbox.

## Files

```go
box.Files.WriteText(ctx, "/workspace/run.sh", "#!/bin/sh\necho ok\n", 0o755)
text, err := box.Files.ReadText(ctx, "/workspace/run.sh")

f, _ := os.Open("src.tar.gz")
box.Files.UploadArchive(ctx, "/workspace", f) // tar or gzip tar, streamed

rc, err := box.Files.DownloadArchive(ctx, "/workspace") // gzip tar
defer rc.Close()

for ev, err := range box.Files.Watch(ctx, "/workspace", true) {
	fmt.Println(ev.Type, ev.Path) // create, write, remove
}
```

`Open` and `WriteFrom` stream large files instead of buffering them. With `sbx-agent` on the server, archives do not need a `tar` binary in the image.

## Processes, terminals, previews

```go
proc, err := box.StartProcess(ctx, []string{"python", "-m", "http.server", "3000"}, &sbx.ExecOptions{Timeout: sbx.NoTimeout})
preview, err := box.CreatePreview(ctx, 3000, false)
resp, err := box.PreviewFetch(ctx, preview.ID, http.MethodGet, "/", nil, nil)
box.SignalProcess(ctx, proc.ID, sbx.SIGTERM)

term, err := box.StartTerminal(ctx, []string{"sh"}, &sbx.TerminalOptions{Cols: 120, Rows: 32})
session, err := box.ConnectTerminal(ctx, term.ID)
defer session.Close()
session.Write([]byte("echo hello; exit\n"))
io.Copy(os.Stdout, session) // ends when the shell exits
code, _ := session.ExitCode()
```

`TerminalSession` is an `io.ReadWriteCloser` over the terminal WebSocket, so `io.Copy` works both ways. `Resize` changes the PTY size. The WebSocket client is built in and sends the key in the `Authorization` header; it does not go through HTTP proxies.

## Templates

`sbx.FromImage(image)` starts a build. Each method returns a new template, so one base can branch into variants. sbx builds the steps once, caches them by content hash, and later creates start from the cached image. `client.Build` only warms the cache.

```go
base := sbx.FromImage("node:22-bookworm-slim").
	WithPackages("ffmpeg", "git").
	WithEnv(map[string]string{"NODE_ENV": "production"}). // build time only
	Workdir("/app").
	Run("npm init -y")

client.Build(ctx, base, "") // optional
box, err := client.CreateSandbox(ctx, sbx.CreateSandboxOptions{From: base, TTL: 5 * time.Minute})
```

`sbx.FromTemplate("")` starts from the `base` template, and `sbx.FromTemplate("python")` from another one. An invalid step (an empty command, no packages) sets `Err()` on the template, and `CreateSandbox` and `Build` return it. Builds need an admin key unless the server sets `builds.allow_sandbox_keys`. `client.CreateTemplate` keeps a named template with steps, setup commands, and a warm pool.

## Volumes and keys

```go
vol, err := client.EnsureVolume(ctx, "cache", "") // same volume on retry
key, err := client.CreateKey(ctx, "ci", sbx.CreateKeyOptions{
	Scopes:     []string{"sandbox"},
	Network:    &sbx.KeyNetworkPolicy{NetworkPolicy: sbx.NetworkPolicy{Mode: sbx.NetworkNone}, MaxMode: sbx.NetworkAllow},
	Registries: []sbx.RegistryCredential{{Server: "ghcr.io", Username: "me", Password: token}},
})
fmt.Println(key.Key) // shown once
```

## Releases

The SDK is its own Go module, so it is versioned with tags prefixed by its directory: `sdk/go/v0.1.0`. `go get github.com/TorstenDittmann/sbx/sdk/go@v0.1.0` then resolves that tag. No publish step is needed.
