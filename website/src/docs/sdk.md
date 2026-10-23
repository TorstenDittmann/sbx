# TypeScript SDK

`@sbx.torsten.work/sbx` is the TypeScript client for sbx. It is ESM, ships its types, has no runtime dependencies, and runs on Node 20+ and Bun.

```bash
npm install @sbx.torsten.work/sbx
```

```bash
bun add @sbx.torsten.work/sbx
```

The unscoped npm name `sbx` is a different project. Releases are published from CI with npm trusted publishing and carry a provenance attestation; see [Installation](/docs/installation/#typescript-sdk).

## Connecting

The client reads two environment variables:

| Variable | Default |
|---|---|
| `SBX_URL` | `http://127.0.0.1:8080` |
| `SBX_API_KEY` | Required. |

Or pass them explicitly. Every entry point takes the same connection options:

```ts
import { Sandbox, SbxClient } from "@sbx.torsten.work/sbx"

const options = { baseUrl: "https://api.example.com", apiKey: process.env.SBX_KEY }

const client = new SbxClient(options)
const box = await Sandbox.create({ template: "base", ...options })
```

`fetch` is a third option, for a custom fetch implementation (proxies, tests, or edge runtimes).

There are two layers. `Sandbox` is the usual entry point: one object per sandbox, with methods that act on it. `SbxClient` is the low-level HTTP client that covers every route, including keys, templates, volumes, and backends.

## Sandbox

### Create and connect

```ts
const box = await Sandbox.create({
  template: "python",
  ttl: "10m",
  idleTimeout: "2m",
  cpu: 1,
  memoryMb: 1024,
  env: { LOG_LEVEL: "debug" },
  secrets: { OPENAI_API_KEY: process.env.OPENAI_API_KEY! },
  labels: { project: "demo" },
  network: { mode: "allow", allow: ["pypi.org", "files.pythonhosted.org"] },
  backend: "gvisor",
})

// Later, from somewhere else:
const same = await Sandbox.connect(box.id)
```

| Option | Type | Maps to |
|---|---|---|
| `template` | `string \| Template` | `template`, or a [build](#templates-and-builds) |
| `image` | `string` | `image` |
| `steps` | `BuildStep[]` | `steps` |
| `cpu` | `number` | `cpu` |
| `memoryMb` | `number` | `memory_mb` |
| `diskMb` | `number` | `disk_mb` |
| `pids` | `number` | `pids` |
| `ttl` | `number \| string` | `ttl_seconds` |
| `idleTimeout` | `number \| string` | `idle_timeout_seconds` |
| `env` | `Record<string, string>` | `env` |
| `secrets` | `Record<string, string>` | `secrets` |
| `labels` | `Record<string, string>` | `labels` |
| `network` | `NetworkPolicy` | `network` (`mode`, `allow`, `deny`, `egressKbps`, `maxConnections`) |
| `backend` | `"docker" \| "gvisor" \| "kata" \| "firecracker"` | `backend` |
| `mounts` | `{ volumeId, path }[]` | `mounts` |

Durations accept seconds as a number or a string with a unit: `"500ms"`, `"30s"`, `"5m"`, `"1h"`. They are rounded to whole seconds; anything above 0 and below a second becomes 1. `parseDuration` is exported if you need it.

A `Sandbox` has the fields of the API object in camelCase: `id`, `status`, `backend`, `template`, `image`, `cpu`, `memoryMb`, `diskMb`, `pids`, `network`, `env`, `labels`, `secretKeys`, `idleTimeoutSeconds`, `expiresAt`, `createdAt`, `node`, and `mounts`.

### Lifecycle

```ts
await box.setTTL("30m")   // new remaining lifetime
await box.pause()
await box.resume()
await box.refresh()       // re-read status and expiry
await box.kill()          // delete
```

Follow lifecycle events instead of polling. The stream resumes after the last id when the connection drops:

```ts
const stop = new AbortController()
for await (const event of client.events({ signal: stop.signal })) {
  if (event.type === "sandbox.lost" || event.type === "sandbox.expired") {
    console.log(event.sandboxId, event.data.reason)
  }
}
```

`events({ sandbox: id })` narrows the stream to one sandbox, and `after: 0` replays what the server still keeps. Each event is `{ id, type, sandboxId, time, data }`.

### Exec

```ts
const result = await box.exec(["python", "-c", "print(2 + 2)"])
result.exitCode   // 0
result.stdout     // "4\n"
result.stderr
result.durationMs
result.truncated

// A string runs through `sh -lc`.
await box.exec("ls -la | wc -l", { cwd: "/workspace", env: { A: "1" }, timeout: "30s" })

// stdin as text or bytes
await box.exec(["wc", "-c"], { stdin: new Uint8Array([1, 2, 3]) })
```

Stream output while it runs:

```ts
for await (const event of box.execStream("npm install")) {
  if (event.type === "stdout" || event.type === "stderr") process.stdout.write(event.data)
  if (event.type === "exit") console.log("exit", event.exitCode)
  if (event.type === "error") console.error(event.code, event.message)
}
```

### Files

`box.files` wraps the file routes. Paths are absolute.

```ts
await box.files.write("/workspace/main.py", "print('hi')\n")
await box.files.write("/workspace/run.sh", "#!/bin/sh\necho ok\n", 0o755)
const text = await box.files.readText("/workspace/main.py")
const bytes = await box.files.read("/workspace/data.bin")
const listing = await box.files.list("/workspace")   // { path, entries }
const info = await box.files.stat("/workspace/main.py")
await box.files.mkdir("/workspace/out")
await box.files.remove("/workspace/out")

for await (const change of box.files.watch("/workspace", { recursive: true })) {
  console.log(change.type, change.path) // create, write, remove
}
```

Archives go through the client:

```ts
const client = new SbxClient()
await client.uploadArchive(box.id, "/workspace", tarOrTarGzBytes)
const tgz = await client.downloadArchive(box.id, "/workspace")
```

`stat`, `watch`, processes, terminals, and previews need [sbx-agent](/docs/agent/) on the server.

### Processes

```ts
const proc = await box.startProcess(["npm", "run", "dev"], { timeout: 0 })
await box.writeProcessStdin(proc.id, "rs\n")
for await (const event of box.streamProcess(proc.id)) {
  if (event.type === "stdout") process.stdout.write(event.data)
  if (event.type === "exit") break
}
await box.signalProcess(proc.id, "TERM")
const all = await box.listProcesses()
```

### Terminals

```ts
const term = await box.startTerminal(["bash"], { cols: 120, rows: 32 })
const session = box.connectTerminal(term.id)

session.onOutput((bytes) => process.stdout.write(bytes))
session.onExit((code) => console.log("exit", code))
session.send("echo hello\n")
session.resize(100, 30)
session.close()
```

`connectTerminal` needs a global `WebSocket` (Node 22+, Bun, and browsers have one). It passes the key as `access_token` in the URL.

### Previews

```ts
const preview = await box.createPreview(3000, { public: false })
preview.url        // authenticated /v1 proxy
preview.publicUrl  // shareable URL, with the token for a private preview
preview.token      // shown once

const res = await box.previewFetch(preview.id, "/api/health")
await box.listPreviews()
await box.deletePreview(preview.id)
```

## Templates and builds

`Sandbox.template()` is an immutable builder for [build steps](/docs/templates/#build-steps). Each method returns a new template:

```ts
const node = Sandbox.template("node:22-bookworm-slim")
  .withPackages("ffmpeg", "git")
  .withEnv({ NODE_ENV: "production" }) // build time only
  .workdir("/app")
  .run("npm init -y")

const withPlaywright = node.run("npx playwright install --with-deps chromium")

const info = await node.build()          // optional, warms the cache
const box = await Sandbox.create(node, { ttl: "5m" })
```

| Method | Step |
|---|---|
| `run(command)` | `{ run }`, a `/bin/sh -c` script as root |
| `withPackages(...names)` | `{ packages }` |
| `withEnv(vars)` | `{ env }`, for later steps only |
| `workdir(dir)` | `{ workdir }`, for later steps only |
| `build(options?)` | Calls `POST /v1/builds`. Returns `BuildInfo` (`id`, `image`, `base`, `steps`, `cached`, `durationMs`). |

`Sandbox.template()` with no argument starts from the `base` template. `Sandbox.template({ template: "python" })` starts from another template, including its own steps. Builds need an admin key unless the server sets `builds.allow_sandbox_keys`.

Named templates, with warm pools:

```ts
const client = new SbxClient()
await client.createTemplate({
  name: "web",
  image: "node:22-bookworm-slim",
  steps: [{ packages: ["ffmpeg"] }],
  setup: ["sh", "-c", "echo ready"],
  setupTimeout: "2m",
  warm: 2,
  network: "full",
})
const box = await Sandbox.create({ template: "web" })

await client.listTemplates()
await client.deleteTemplate("web")
await client.build({ image: "python:3.12-slim", steps: [{ run: "pip install numpy" }] })
```

## SbxClient

Every `Sandbox` method has a client equivalent that takes the sandbox id first, such as `client.exec(id, cmd)`. The client also has:

### Sandboxes

```ts
await client.createSandbox(options)    // returns SandboxInfo, not a Sandbox
await client.getSandbox(id)
await client.listSandboxes({ status: "running", template: "python", q: "demo", labels: { project: "demo" } })
await client.deleteSandbox(id)
await client.setSecrets(id, { TOKEN: "new" })
```

### Volumes

```ts
const volume = await client.createVolume("cache", "docker")
await client.listVolumes()
await client.getVolume(volume.id)
await client.deleteVolume(volume.id)
```

### Keys

Admin key required.

```ts
const created = await client.createKey("agent", {
  scopes: ["sandbox"],
  expiresAt: new Date(Date.now() + 30 * 24 * 3600 * 1000),
  network: { mode: "allow", maxMode: "allow", allow: ["pypi.org"] },
  registries: [{ server: "ghcr.io", username: "agent-bot", password: process.env.GHCR_TOKEN }],
})
created.key // shown once

await client.listKeys()
await client.getKey(created.id)
await client.setKeyRegistries(created.id, []) // replace or remove private registry credentials
await client.revokeKey(created.id) // stops working immediately
```

### Backends

```ts
const backends = await client.listBackends()
const usable = backends.filter((b) => b.available).map((b) => b.name)
```

## Errors

Failed requests throw `SbxError` with the HTTP `status`, the API's error `code`, and its `message`:

```ts
import { SbxError } from "@sbx.torsten.work/sbx"

try {
  await Sandbox.create({ template: "missing" })
} catch (err) {
  if (err instanceof SbxError && err.code === "not_found") {
    // ...
  }
  throw err
}
```

The codes are listed in the [HTTP API reference](/docs/api/#errors).

## Types

All request and response types are exported: `SandboxInfo`, `SandboxStatus`, `CreateSandboxOptions`, `ExecResult`, `ExecOptions`, `ExecEvent`, `FileEntry`, `FileEvent`, `ProcessInfo`, `TerminalInfo`, `TerminalSession`, `PreviewInfo`, `TemplateInfo`, `CreateTemplateOptions`, `BuildStep`, `BuildInfo`, `VolumeInfo`, `VolumeMount`, `NetworkPolicy`, `NetworkMode`, `KeyInfo`, `KeyNetworkPolicy`, `CreatedKey`, `BackendInfo`, and `ConnectionOptions`.
