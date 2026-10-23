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

`sbx` on npm is a different project. This client is [`@sbx.torsten.work/sbx`](sdk/typescript/README.md): ESM, with types, for Node and Bun. The Python client is [`sbx-sdk`](sdk/python/README.md) (`pip install sbx-sdk`, imported as `sbx`), with no runtime dependencies, for Python 3.9+. More detail is in [docs/install.md](docs/install.md). Cutting a release is [docs/releasing.md](docs/releasing.md).

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

The same path from the TypeScript SDK (Node 22) or the Python SDK (Python 3.9+) in this repo, with no install step. The published packages are `@sbx.torsten.work/sbx` and `sbx-sdk`; see [Install](#install).

```bash
export SBX_URL=http://127.0.0.1:8080
node --experimental-strip-types examples/quickstart.ts
python3 examples/quickstart.py
```

Built-in templates are `base` (`alpine:3.20`, 0.5 CPU, 128 MiB), `python` (`python:3.12-slim`), and `node` (`node:22-bookworm-slim`). Without a template, the server defaults are 1 CPU, 512 MiB, and a 1024 MiB workspace.

A template can keep a warm pool. `warm` is how many sandboxes to start ahead of time (0 to 8). `setup` is a command that runs before a warm sandbox is offered, so the next create skips image pull and install. `network` on the template is the default when create omits it (`none` or `full`). A create that changes image, resources, network, or env does not take a pooled sandbox. Pooled sandboxes are hidden from the normal list; an admin can see them with `?status=warm`. Deleting the template deletes the pool.

A template can also carry build `steps`, like [Railway's sandbox templates](https://docs.railway.com/sandboxes#templates). Each step is one of `{"run": "npm install"}`, `{"packages": ["ffmpeg", "git"]}`, `{"env": {"NODE_ENV": "production"}}`, or `{"workdir": "/app"}`. sbx builds the steps on the image once, caches the result as `sbx-template:<hash>` keyed by the base and the steps, and starts every later sandbox from it. `env` and `workdir` only apply to later steps; pass runtime variables in create's `env`. Build outside `/workspace`, which is mounted over at runtime. `POST /v1/sandboxes` takes `steps` too (after the template's own), and `POST /v1/builds` builds ahead of time. Builds run as root in Docker's builder with network, so they need an admin key unless `builds.allow_sandbox_keys` is set.

```bash
curl -s http://127.0.0.1:8080/v1/sandboxes \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"image":"node:22-bookworm-slim","steps":[{"packages":["ffmpeg"]},{"workdir":"/app"},{"run":"npm init -y"}]}'
```

### Compose

```bash
docker compose up --build
```

The server listens on container port 8080 (`SBX_ADDR=:8080`). `./data` on the host holds SQLite and the initial API key. The compose file mounts the Docker socket; that is the host socket, not something sandboxes can see.

`sbx doctor` reports whether Docker answers, whether the `runsc` runtime is registered, whether `/dev/kvm` is writable, whether the Kata runtime class is registered, and whether the Firecracker binary and guest kernel are installed. Select a backend per sandbox with `"backend": "gvisor"`, `"backend": "kata"`, or `"backend": "firecracker"`. A missing runtime is an error, not a silent fallback to runc. `sbx setup gvisor`, `sbx setup kata`, and `sbx setup firecracker` print the host steps; add `--apply` to download pinned binaries and, for gVisor and Kata, register a Docker runtime. `GET /v1/backends` returns the same picture to API clients. A template's `backend` field is the default for sandboxes created from it.

Config lives in `sbx.yaml` (see `sbx.yaml.example`). Flags override environment variables, which override the file.

## What you get

- `POST /v1/sandboxes`, get, list, delete, pause, resume, and TTL updates. List takes exact `status`, `backend`, `template`, and repeatable `label=key:value` filters, plus `q` for a case-insensitive match on id, template, image, status, and label keys and values.
- Exec that waits, and exec as server-sent events with `?stream=1`. Optional `stdin` or `stdin_base64`.
- Background processes, interactive terminals over WebSocket, inotify file watches, and preview URLs for a service on the sandbox loopback. These need `sbx-agent` (see [docs/AGENT.md](docs/AGENT.md)).
- File list, upload, download, delete, and tar archives.
- API keys (`admin` and `sandbox` scopes). Sandbox keys cannot see each other's sandboxes. A key can carry `expires_at`, and `DELETE /v1/keys/{id}` (or `sbx keys revoke --id`) revokes it at once. The API refuses to revoke the last admin key.
- `secrets` on create, and `PUT /v1/sandboxes/{id}/secrets`. Names come back as `secret_keys`. Values are not returned by get or list. They are injected into the sandbox and into later commands. A secret name cannot also be set in `env`.
- A reaper for TTL, idle timeout, stuck creates, and orphan containers. On startup the server adopts containers that are still running and marks rows whose containers disappeared.
- Prometheus text at `/metrics`. `/healthz` is liveness. `/readyz` stays unavailable until that startup reconcile finishes.
- A node-wide sandbox cap (`max_sandboxes`, default 100) on top of the per-key quota, a per-key request rate limit, and a request body size limit.
- Secure defaults: no network, all capabilities dropped, non-root, read-only rootfs, memory without swap, PID limit. `disk_mb` is the size of the writable `/workspace` tmpfs. Firecracker uses an ext4 rootfs of that size, with `sbx-agent` as init.
- Named volumes (`POST /v1/volumes`) that outlive a sandbox. Mount one at `/workspace` to keep a project or package cache across sandboxes. `disk_mb` does not cap a volume mounted at `/workspace`. A sandbox key only sees its own volumes. Deleting a sandbox does not delete the volume; deleting a volume fails while a live sandbox mounts it. Firecracker does not attach Docker volumes.

`network: none` is the default. `network: full` (or `allow`) joins the `sbx` bridge and can reach the public internet: DNS, IPv4, IPv6 when the daemon has it, git, and package registries. Metadata and private ranges stay blocked unless the operator opts out. A server default, an API-key default, and a per-sandbox override share one policy, including domain and CIDR allow and deny lists and per-sandbox bandwidth and connection limits. Preview ports are reachable through the server on `/p/{id}/{port}/` and, with `preview_domain`, on `<port>-sbx-<id>.sbx.example.com`. The setup for Caddy, Traefik, and Dokploy is in [docs/NETWORK.md](docs/NETWORK.md).

When `sbx-agent` sits next to the `sbx` binary, or `SBX_AGENT_BIN` points at it, sandboxes start that binary and do not need a shell for exec, files, terminals, archives, or previews. The compose image ships both binaries. Without the agent, exec and files still use a shell inside the image (`sh`, `cat`, `find`, `stat`, `tar`). Firecracker always needs the agent: it is the guest init. The design notes are in [docs/DESIGN.md](docs/DESIGN.md) and [docs/AGENT.md](docs/AGENT.md). Kata setup is [docs/kata.md](docs/kata.md). Firecracker setup is [docs/firecracker.md](docs/firecracker.md).

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
(cd sdk/python && PYTHONPATH=src python3 -m unittest discover -s tests -t .)
```

Integration tests talk to a real Docker daemon and are skipped unless `SBX_INTEGRATION=1`. The live SDK tests are skipped unless `SBX_LIVE=1` (the server must already be running). CI runs unit tests with the race detector and prints the coverage total, then the Docker integration job, the SDK tests, and a website image build. Pull requests also run a GoReleaser snapshot that builds the archives and the image without publishing.
