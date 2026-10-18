# sbx

sbx is a single-binary sandbox server. Point it at Docker, create a sandbox, run a command. Network is off unless you opt in, the TTL is enforced by the server, and the API stays the same when you later select gVisor.

MIT licensed. Module: `github.com/TorstenDittmann/sbx`.

## Quickstart

Docker must be running, and your user must be able to use `/var/run/docker.sock`.

```bash
go build -o sbx ./cmd/sbx
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

The same path from the TypeScript SDK (Node 22, no install step):

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
- Exec that waits, and exec as server-sent events with `?stream=1`.
- File list, upload, download, delete, and tar archives.
- API keys (`admin` and `sandbox` scopes). Sandbox keys cannot see each other's sandboxes.
- A reaper for TTL, idle timeout, stuck creates, and orphan containers.
- Prometheus text at `/metrics`, OpenAPI at `/openapi.yaml`.
- Secure defaults: no network, all capabilities dropped, non-root, read-only rootfs, memory without swap, PID limit. `disk_mb` is the size of the writable `/workspace` tmpfs.

`network: full` attaches the sandbox to a dedicated bridge (`sbx`, interface `sbx0`, `172.30.0.0/16`). Before the container starts, sbx installs an `SBX-EGRESS` chain on `DOCKER-USER` and `INPUT` that rejects traffic from that bridge to cloud metadata and to private or link-local ranges:

- `169.254.0.0/16` (AWS, GCP, Azure, and other metadata services on `169.254.169.254`)
- RFC1918 (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`)
- Alibaba Cloud metadata `100.100.100.200/32`
- IPv6 link-local `fe80::/10`, unique-local `fc00::/7` (including `fd00:ec2::254`), and `::1`

Public addresses stay reachable. The bridge has inter-container communication disabled. If the rules cannot be installed, `network: full` fails closed and the sandbox is not created. The server applies them with the host `iptables` when it can, and otherwise with a short-lived privileged helper that enters the host network namespace through the mounted Docker socket. Egress allow-lists are still rejected in this version.

Images need a shell and the usual tools (`sh`, `cat`, `find`, `stat`, `tar`). Exec timeouts are enforced by a shell wrapper, so a `timeout` binary is not required. The design notes and the roadmap are in [docs/DESIGN.md](docs/DESIGN.md).

## Deploying the website with Dokploy

The landing page is `website/`. Its image listens on port **80**, has no environment variables, and does not need credentials. In Dokploy:

| Setting | Value |
|---|---|
| Build path | `website` |
| Dockerfile path | `website/Dockerfile` |
| Container port | `80` |
| Environment | none |

`website/docker-compose.yml` maps that port to `8088` for a local check. Details, including `/healthz`, are in [docs/website.md](docs/website.md).

## Develop

```bash
go test ./...
SBX_INTEGRATION=1 go test -count=1 -timeout 20m ./internal/driver/docker/ ./test/e2e/
npm test --prefix sdk/typescript
```

Integration tests talk to a real Docker daemon and are skipped unless `SBX_INTEGRATION=1`. The live SDK test is skipped unless `SBX_LIVE=1` (the server must already be running). CI runs unit tests with the race detector and prints the coverage total, then the Docker integration job, the SDK tests, and a website image build.
