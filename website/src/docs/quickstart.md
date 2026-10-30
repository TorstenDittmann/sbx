# Quickstart

This page starts a server on your machine, creates a sandbox, and runs a command in it, first with `curl` and then with the TypeScript SDK.

You need Docker running, and your user must be able to use `/var/run/docker.sock`.

## 1. Start the server

Pick one way to run sbx. The [installation](/docs/installation/) page covers each in more detail.

**Release binary** (Linux or macOS):

```bash
curl -fsSL https://raw.githubusercontent.com/TorstenDittmann/sbx/main/install.sh | sh
sbx serve
```

**Container image**:

```bash
docker run --rm -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v sbx-data:/data \
  ghcr.io/torstendittmann/sbx:latest
```

**From source** (Go 1.26 or newer):

```bash
git clone https://github.com/TorstenDittmann/sbx
cd sbx
go build -o sbx ./cmd/sbx
go build -o sbx-agent ./cmd/sbx-agent
./sbx serve
```

Build `sbx-agent` next to `sbx`. The server looks for it there and copies it into each sandbox. Without it, exec and files still work through a shell in the image, but processes, terminals, watches, and previews do not.

## 2. Save the admin key

The first boot of a data directory prints an admin key once:

```text
SBX_API_KEY=sbx_...
```

It is also written to `data/initial_api_key` with mode `0600` (or `/data/initial_api_key` in the container). Export it:

```bash
export SBX_API_KEY=sbx_...
export SBX_URL=http://127.0.0.1:8080
```

The server listens on `127.0.0.1:8080` by default. Check that it is ready:

```bash
curl -s $SBX_URL/readyz
```

## 3. Create a sandbox

```bash
curl -s $SBX_URL/v1/sandboxes \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"template":"base","ttl_seconds":300}'
```

The response is the sandbox. Keep its `id`:

```json
{
  "id": "sbx_3f2a9c1e7b6d4a10",
  "status": "running",
  "backend": "docker",
  "template": "base",
  "image": "alpine:3.20",
  "cpu": 0.5,
  "memory_mb": 128,
  "network": "none",
  "expires_at": "2026-10-20T12:05:00Z"
}
```

`base` is `alpine:3.20` with 0.5 CPU and 128 MiB. `python` and `node` are the other built-in templates. The sandbox is deleted after `ttl_seconds` unless you extend it.

## 4. Run a command

```bash
curl -s $SBX_URL/v1/sandboxes/$ID/exec \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"cmd":["echo","hello"]}'
```

```json
{ "exit_code": 0, "stdout": "hello\n", "stderr": "", "duration_ms": 12, "truncated": false }
```

Add `?stream=1` to get the output as server-sent events while the command runs.

## 5. Clean up

```bash
curl -s -X DELETE $SBX_URL/v1/sandboxes/$ID \
  -H "Authorization: Bearer $SBX_API_KEY"
```

## The same thing in TypeScript

Install the SDK. The npm package is `@sbx.torsten.work/sbx`; the unscoped `sbx` is a different project.

```bash
npm install @sbx.torsten.work/sbx
```

The client reads `SBX_URL` and `SBX_API_KEY` from the environment.

```ts
import { Sandbox } from "@sbx.torsten.work/sbx"

const box = await Sandbox.create({ template: "python", ttl: "5m" })

await box.files.write("/workspace/main.py", "print(2 + 2)\n")
const result = await box.exec(["python", "main.py"]) // runs in /workspace
console.log(result.stdout) // 4

await box.kill()
```

From a clone of the repository, `node --experimental-strip-types examples/quickstart.ts` runs the same flow without installing anything.

## Next steps

- [Sandboxes](/docs/sandboxes/): resources, TTLs, labels, and secrets.
- [Network](/docs/network/): give a sandbox internet access.
- [Templates and builds](/docs/templates/): preinstall packages and keep warm sandboxes.
- [Deployment](/docs/deployment/): run sbx on a server behind a reverse proxy.
