# sbx

sbx is a single-binary sandbox server. Point it at Docker, create a sandbox, run a command. Network is off unless you opt in, the TTL is enforced by the server, and the API stays the same when you later select gVisor.

MIT licensed. Module: `github.com/TorstenDittmann/sbx`.

## Install

A tag `vX.Y.Z` publishes the binaries, the `ghcr.io/torstendittmann/sbx` image, and the npm package. The commands below are that release. Building from source is the quickstart under them.

```bash
curl -fsSL https://raw.githubusercontent.com/TorstenDittmann/sbx/main/install.sh | sh
sbx version
```

The script selects linux or darwin and amd64 or arm64, then checks `checksums.txt` from the release. Linux gets `sbx` and `sbx-agent`. macOS gets the darwin `sbx` binary plus the linux `sbx-agent` of the same architecture, because the agent executes inside the sandbox container. `SBX_VERSION=v0.2.0` pins a release. `SBX_INSTALL_DIR` chooses the directory (`/usr/local/bin` when it is writable, otherwise `~/.local/bin`).

```bash
docker run --rm -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v sbx-data:/data \
  ghcr.io/torstendittmann/sbx:latest
```

The image listens on port 8080, keeps SQLite in `/data`, and carries `sbx-agent` so injection works without a separate download. Tags are the version (`0.2.0`), the git tag (`v0.2.0`), and `latest` for a final release.

```bash
npm install @sbx.torsten.work/sbx
bun add @sbx.torsten.work/sbx
```

`sbx` on npm is a different project. This client is [`@sbx.torsten.work/sbx`](sdk/typescript/README.md): ESM, with types, for Node and Bun. More detail is in [docs/install.md](docs/install.md). Cutting a release is [docs/releasing.md](docs/releasing.md).

## Quickstart

Docker must be running, and your user must be able to use `/var/run/docker.sock`.

```bash
go build -o sbx ./cmd/sbx
go build -o sbx-agent ./cmd/sbx-agent
./sbx serve
```

The first boot prints an admin key once:

```text
SBX_API_KEY=sbx_...
```

It is also written to `data/initial_api_key` (mode 0600).

```bash
export SBX_API_KEY=sbx_...   # the value just printed

curl -s http://127.0.0.1:8080/v1/sandboxes \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"template":"base","ttl_seconds":300}'
```

Take `id` from the response, then:

```bash
curl -s http://127.0.0.1:8080/v1/sandboxes/$ID/exec \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"cmd":["echo","hello"]}'
```

The same path from the TypeScript SDK in this repo (Node 22, no install step). The published package is `@sbx.torsten.work/sbx`; see [Install](#install).

```bash
export SBX_URL=http://127.0.0.1:8080
node --experimental-strip-types examples/quickstart.ts
```

Built-in templates are `base` (`alpine:3.20`, 0.5 CPU, 128 MiB), `python` (`python:3.12-slim`), and `node` (`node:22-bookworm-slim`). Without a template, the server defaults are 1 CPU, 512 MiB, and a 1024 MiB workspace.

### Compose

```bash
docker compose up --build
```

The server listens on container port 8080 (`SBX_ADDR=:8080`). `./data` on the host holds SQLite and the initial API key. The compose file mounts the Docker socket; that is the host socket, not something sandboxes can see.

`sbx doctor` reports whether Docker answers and whether the `runsc` runtime is registered. Select gVisor per sandbox with `"backend": "gvisor"`, or set `SBX_BACKEND=gvisor` after `runsc` is installed. A missing runtime is an error.

Config lives in `sbx.yaml` (see `sbx.yaml.example`). Flags override environment variables, which override the file.

## What you get

- `POST /v1/sandboxes`, get, list, delete, pause, resume, and TTL updates.
- Exec that waits, and exec as server-sent events with `?stream=1`. Optional `stdin` or `stdin_base64`.
- Background processes, interactive terminals over WebSocket, file watches, and preview URLs for a service on the sandbox loopback. These need `sbx-agent` (see [docs/AGENT.md](docs/AGENT.md)).
- File list, upload, download, delete, and tar archives.
- API keys (`admin` and `sandbox` scopes). Sandbox keys cannot see each other's sandboxes.
- A reaper for TTL, idle timeout, stuck creates, and orphan containers. On startup the server adopts containers that are still running and marks rows whose containers disappeared.
- Prometheus text at `/metrics`. `/healthz` is liveness. `/readyz` stays unavailable until that startup reconcile finishes.
- A node-wide sandbox cap (`max_sandboxes`, default 100) on top of the per-key quota, a per-key request rate limit, and a request body size limit.
- Secure defaults: no network, all capabilities dropped, non-root, read-only rootfs, memory without swap, PID limit. `disk_mb` is the size of the writable `/workspace` tmpfs.

`network: none` is the default. `network: full` (or `allow`) joins the `sbx` bridge and can reach the public internet: DNS, IPv4, IPv6 when the daemon has it, git, and package registries. Metadata and private ranges stay blocked unless the operator opts out. A server default, an API-key default, and a per-sandbox override share one policy, including domain and CIDR allow and deny lists and per-sandbox bandwidth and connection limits. Preview ports are reachable through the server on `/p/{id}/{port}/` and, with `preview_domain`, on `<port>-sbx-<id>.sbx.example.com`. The setup for Caddy, Traefik, and Dokploy is in [docs/NETWORK.md](docs/NETWORK.md).

When `sbx-agent` sits next to the `sbx` binary, or `SBX_AGENT_BIN` points at it, sandboxes start that binary and do not need a shell for exec, files, terminals, or previews. The compose image ships both binaries. Without the agent, exec and files still use a shell inside the image (`sh`, `cat`, `find`, `stat`, `tar`). Archives use `tar` either way. The design notes are in [docs/DESIGN.md](docs/DESIGN.md) and [docs/AGENT.md](docs/AGENT.md).

## Deploying the website with Dokploy

The landing page is `website/`. Its image listens on port **80**, has no environment variables, and does not need credentials. In Dokploy:

| Setting | Value |
|---|---|
| Build path | `/website` |
| Dockerfile | `Dockerfile` (`website/Dockerfile` from the repository root) |
| Docker context | `.` |
| Container port | `80` |
| Environment | none |

`website/docker-compose.yml` maps that port to `8088` for a local check. Details, including `/healthz`, are in [docs/website.md](docs/website.md).

## Develop

```bash
go test ./...
SBX_INTEGRATION=1 go test -count=1 -timeout 40m ./internal/driver/docker/ ./test/e2e/
npm test --prefix sdk/typescript
bun test --cwd sdk/typescript test
```

Integration tests talk to a real Docker daemon and are skipped unless `SBX_INTEGRATION=1`. The live SDK test is skipped unless `SBX_LIVE=1` (the server must already be running). CI runs unit tests with the race detector and prints the coverage total, then the Docker integration job, the SDK tests, and a website image build. Pull requests also run a GoReleaser snapshot that builds the archives and the image without publishing.
