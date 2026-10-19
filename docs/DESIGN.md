# sbx

Self-hosted sandboxes. One Go binary and one command to start. The same API is meant to grow from a laptop with Docker to a cluster, without a rewrite.

Confirmed for this repository:

- Name: **sbx**
- License: **MIT**
- Module and repo: `github.com/TorstenDittmann/sbx`
- Language: **Go**
- There is no E2B-compatible API and no plan to add one.

## 1. Where it sits

| Project | Isolation | Self-host story | Gap sbx fills |
|---|---|---|---|
| E2B infra | Firecracker | Terraform + Nomad + Consul. Multi-node is a paid offering | Heavy to operate, and it only does Firecracker |
| Daytona | runc containers | Compose with Postgres, Redis, MinIO, and more. Public repo archived June 2026 | Many moving parts |
| microsandbox | libkrun microVM | An embeddable library, no server | Not a multi-tenant API |
| Arrakis | Cloud Hypervisor | Single host, VM-only, AGPL | No container tier, AGPL |
| Modal | gVisor | Closed SaaS | Not self-hostable |
| k8s agent-sandbox / OpenShell | RuntimeClass | You need Kubernetes first | Too much for a small team |

The position is the SQLite of sandboxes: a single static binary in front of Docker, with a driver interface so gVisor, Kata, Firecracker, and Kubernetes can be added later as backends rather than as new products. The license is MIT.

## 2. Why Go

Docker, containerd, gVisor, Kata, and Kubernetes are Go-shaped ecosystems. A `CGO_ENABLED=0` binary cross-compiles cleanly and fits a small image. The work is I/O: exec streams, file transfer, and a reaper. v0.1 talks to the Docker Engine API with a small Unix-socket client instead of pulling in the moby module tree.

## 3. Architecture

```
        curl / TypeScript SDK
                 │  HTTP + API key
                 ▼
 ┌──────────── sbx serve ─────────────┐
 │  REST /v1  ·  auth  ·  quotas      │
 │  TTL reaper  ·  templates          │
 │  SQLite (single node)              │
 │  driver.Driver                     │
 │    docker (runc)                   │
 │    gvisor (docker + runsc)         │
 └────────────────────────────────────┘
                 │  framed session
                 │  (docker exec dial today,
                 │   TCP later)
                 ▼
            sbx-agent inside the sandbox
```

Later backends (Kata, Firecracker, Kubernetes) and a controller/node split implement the same driver surface. They are not in v0.1, and there are no stub packages for them.

### v0.1 decisions

- The command is `sbx serve`. `sbx server` is an alias. `controller` and `node` are not implemented.
- The default backend is `docker`. `gvisor` is the same driver with runtime `runsc`. If that runtime is missing, create returns HTTP 400 `unsupported`. Startup fails only when the configured default backend is gVisor and `runsc` is absent.
- `api/openapi.yaml` is the contract. Handlers are written by hand to match it. Code generation is deferred.
- v0.1 had no in-guest agent. v0.2 runs `sbx-agent` inside the sandbox when the binary is configured. Exec, files, processes, terminals, watches, archives, and previews then go through one framed session. See [AGENT.md](AGENT.md). With no binary configured, exec and files stay on the engine API, archives still call `tar`, and the new routes return `unsupported`.
- The workspace disk limit is the size of a tmpfs on `/workspace` (uid/gid 1000, mode 755). `/tmp` is a smaller tmpfs. The root filesystem is read-only by default, so the limit is real. A named volume mounted at `/workspace` replaces that tmpfs. `disk_mb` then does not apply to the persisted workspace. Paths under `/workspace/` are rejected. Other absolute paths (for example `/cache`) are extra mounts. At most eight mounts. `/`, `/tmp`, `/opt`, `/opt/sbx`, `/proc`, `/sys`, `/dev`, and `/etc` are reserved.
- Volumes are Docker volumes named by sbx (`sbx-` plus the volume id). A fresh volume is chowned to the sandbox uid (1000 when the server is configured as root) without walking existing files, so a project stays owned by the same uid across sandboxes. The caller never chooses a host path.
- `network: none` is the default. `full` and `allow` join a dedicated bridge. Metadata and private ranges stay blocked unless the operator opts out. Allow and deny lists, bandwidth, and connection limits are in [NETWORK.md](NETWORK.md).
- State is SQLite at `$data_dir/sbx.db` (WAL, one connection). Postgres is a cluster concern.
- The landing page is the static site in `website/`, deployed with Dokploy. See [website.md](website.md). There is no GitHub Pages workflow.

### Driver interface

`internal/driver.Driver` is the extension point: ping, ensure image, create, delete, inspect, list, pause, resume, exec, exec stream, and file list/read/write/remove. `driver.Guest` is the optional agent surface (stat, mkdir, watch, processes, PTY, dial). Docker implements it when `sbx-agent` is configured.

## 4. Backend ladder

| Tier | Driver | Isolation | In v0.1 |
|---|---|---|---|
| 0 | `docker` | Namespaces, cgroups, seccomp. Shared kernel | Yes. Default |
| 1 | `gvisor` | runsc, no KVM | Yes, when `runsc` is registered |
| 2 | `kata` | Lightweight VM, OCI image unchanged | No |
| 3 | `firecracker` | MicroVM | No |
| — | `kubernetes` | Whatever RuntimeClass the cluster has | No |

gVisor has syscall-compat gaps. That tradeoff is why it stays optional.

## 5. HTTP API

Authenticate with `Authorization: Bearer sbx_<key>`. Keys are stored as sha256. Scopes are `admin` (implies everything) and `sandbox`. A sandbox key only sees sandboxes it created; other ids are 404. The first boot of an empty data directory generates an admin key, logs `SBX_API_KEY` once, and writes `$data_dir/initial_api_key` mode 0600. `SBX_BOOTSTRAP_KEY` pins that first key instead.

```
POST   /v1/sandboxes
GET    /v1/sandboxes?status=&backend=&template=&label=k:v&q=
GET    /v1/sandboxes/{id}
DELETE /v1/sandboxes/{id}                 idempotent stop
POST   /v1/sandboxes/{id}/timeout         {"ttl_seconds": 600}
POST   /v1/sandboxes/{id}/pause
POST   /v1/sandboxes/{id}/resume
POST   /v1/sandboxes/{id}/exec            wait
POST   /v1/sandboxes/{id}/exec?stream=1   SSE: stdout, stderr, exit, error
POST   /v1/sandboxes/{id}/processes      background command; timeout 0 means none
GET    /v1/sandboxes/{id}/processes
GET    /v1/sandboxes/{id}/processes/{procId}/stream
POST   /v1/sandboxes/{id}/terminals      PTY; WebSocket at .../ws
GET    /v1/sandboxes/{id}/files/watch     SSE
POST   /v1/sandboxes/{id}/previews       loopback port, then proxy under .../{previewId}/

GET    /v1/sandboxes/{id}/files?path=
GET    /v1/sandboxes/{id}/files/content?path=
PUT    /v1/sandboxes/{id}/files/content?path=&mode=644
DELETE /v1/sandboxes/{id}/files?path=
POST   /v1/sandboxes/{id}/files/archive?path=
GET    /v1/sandboxes/{id}/files/archive?path=

POST   /v1/volumes
GET    /v1/volumes
GET    /v1/volumes/{id}
DELETE /v1/volumes/{id}                   409 while a live sandbox mounts it

GET    /v1/backends
GET    /v1/templates
POST   /v1/templates                      admin
DELETE /v1/templates/{name}               admin; built-ins return 403
POST   /v1/keys                           admin
GET    /v1/keys                           admin

GET    /  /healthz  /metrics  /openapi.yaml
```

Create accepts `template` or `image`, resource fields, `ttl_seconds`, `idle_timeout_seconds`, `env`, `secrets`, `network` (`none`, `full`, or `allow`, plus allow, deny, and limits), `backend` (`docker` or `gvisor`), `labels`, and `mounts` (`volume_id`, `path`). Unknown JSON fields are rejected. A volume's backend must match the sandbox. Volume responses do not include the Docker volume name. `secrets` are stored apart from `env`. Get and list return `secret_keys` and never the values. Exec and new processes merge those values into the command environment, with the request's `env` winning on a duplicate name. `PUT /v1/sandboxes/{id}/secrets` replaces the map. A template may pin `backend`. Create uses the request backend, then the template backend, then the server default. Built-in templates, replaced on every start, are `base` (`alpine:3.20`), `python` (`python:3.12-slim`), and `node` (`node:22-bookworm-slim`).

A user template may set `warm` (0–8), `setup` (an argv run once before the sandbox is offered), `setup_timeout_seconds`, and `network`. The reaper keeps that many sandboxes in status `warm`, owned by `sbx-system`, so they do not use a key's quota and they are not expired by TTL. Create claims the oldest match when the request does not override image, cpu, memory, disk, pids, backend, network, env, secrets, or mounts. Otherwise sbx starts a new sandbox and runs `setup` then. Startup and the reaper refill the pool. `GET /v1/sandboxes` hides `warm` unless the caller is an admin and asks for `status=warm`.

`GET /v1/backends` lists the ladder (`docker`, `gvisor`, `kata`, `firecracker`) with `available` and the features that driver implements. Kata and Firecracker are reported as not enabled until those drivers are configured. `sbx doctor` checks KVM, the Kata shim, and a Firecracker kernel under the data directory. `sbx setup gvisor|kata|firecracker` prints the install plan; `--apply` downloads pinned assets and, for the container runtimes, merges a Docker `runtimes` entry without restarting the daemon.

Errors are `{"error":{"code","message"}}`. Create is 201. The sandbox `node` field is always `"local"`.

`q` matches a case-insensitive substring of the sandbox id, template, image, status, or any label key or value. `label` stays an exact key:value match, and every filter is combined. `q` is at most 128 characters. Env values are not part of the search.

Defaults: bind `127.0.0.1:8080` (compose sets `SBX_ADDR=:8080`), TTL 300s (max 86400), idle timeout off, 1 CPU, 512 MiB, 1024 MiB workspace, 256 pids, 20 sandboxes per key, exec timeout 60s (max 3600), output cap 8 MiB, upload cap 32 MiB. The reaper runs every 5s and stops expired sandboxes, idle sandboxes, creates stuck longer than 15 minutes, and orphan containers labeled `sbx.managed=true`. Stopped rows are pruned after a day.

## 6. SDK

`sdk/typescript` is `@sbx/sdk`. It has no runtime dependencies and uses `fetch` plus the global `WebSocket`. `Sandbox.create` reads `SBX_URL` and `SBX_API_KEY` when `process` exists. Durations such as `"5m"` are converted to seconds in the SDK; the server wants integers. A string command is run as `sh -lc`. The SDK also covers exec streaming, processes, PTY sessions, file watches, and previews. Tests run on Node and Bun. Python and Go SDKs are later.

## 7. Layout

```
cmd/sbx/                 server binary
cmd/sbx-agent/           in-sandbox binary
api/openapi.yaml         contract, embedded at /openapi.yaml
internal/api/            HTTP
internal/core/           lifecycle, reaper, templates
internal/store/          SQLite
internal/driver/         interface, docker, fake
sdk/typescript/          SDK
examples/quickstart.ts
website/                 static site + Dockerfile
deploy/compose/          server compose
deploy/systemd/          unit file
test/e2e/                HTTP quickstart against Docker
docs/                    this file
```

## 8. Running it

Binary:

```bash
go build -o sbx ./cmd/sbx
./sbx serve
```

Compose (publishes 8080, mounts `./data` and the Docker socket):

```bash
docker compose up --build
```

Inside that container the listen address has to be `:8080`. Binding only `127.0.0.1` would hide the port from the published mapping.

`sbx doctor` checks the socket and warns when `runsc` is missing. `sbx keys create --name NAME` mints another key. `sbx healthcheck` hits `/healthz`, mapping `:8080` and `0.0.0.0` to `127.0.0.1`.

Config file, then environment, then flags. See `sbx.yaml.example`. Useful variables: `SBX_ADDR`, `SBX_DATA_DIR`, `SBX_BACKEND`, `SBX_DOCKER_HOST`, `SBX_GVISOR_RUNTIME`, `SBX_READ_ONLY_ROOTFS`, `SBX_BOOTSTRAP_KEY`, `SBX_LOG_LEVEL`, `SBX_AGENT_BIN`.

Startup lists containers, adopts ones that still match a sandbox row, marks missing ones stopped with error `lost`, and deletes containers that have no live row. `/readyz` is 503 until that pass finishes. `/healthz` only checks that the process and Docker answer. `max_sandboxes` caps the node. `requests_per_minute` caps each API key. Request bodies larger than `max_upload_bytes` are rejected.

The website is separate. [Deploying the website with Dokploy](website.md#deploying-the-website-with-dokploy) covers its build path, Dockerfile, and port.

## 9. Security baseline

Sandboxes are created with `CapDrop: ["ALL"]`, `no-new-privileges`, a non-root user, memory equal to memory+swap (no swap), a PID limit, private IPC, read-only rootfs, and `network=none` unless `full` is requested. The Docker socket is not mounted into sandboxes. API keys are hashed. TTLs and the per-key sandbox quota are enforced in the server, not left to the caller.

`network: full` and `network: allow` are explicit opt-ins to public egress. Sandboxes on those modes join the `sbx` bridge (`sbx0`, `172.30.0.0/16`, ICC off). IPv6 is added on a new bridge when `network.ipv6` is `on` or `auto` and the daemon has IPv6. sbx keeps an `SBX-EGRESS` chain jumped from `DOCKER-USER` and from `INPUT` for packets that arrive on `sbx0`. Per-sandbox chains run first. The baseline chain rejects:

- `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`
- `169.254.0.0/16`, which covers `169.254.169.254`
- `100.100.100.200/32` (Alibaba Cloud metadata; the rest of `100.64.0.0/10` is left open)
- when `ip6tables` has a `DOCKER-USER` chain: `fe80::/10`, `fc00::/7`, and `::1/128`

The baseline is installed once per process, before the first networked container is created. A root server runs `iptables` directly. A non-root server (the usual Compose setup, which only mounts `/var/run/docker.sock`) starts a privileged `alpine:3.20` helper on the host network and host PID namespace and applies the host's `iptables` via `nsenter`. If both paths fail, create returns an error and does not start the container. Operators can set `block_private` and `block_metadata` to false. See [NETWORK.md](NETWORK.md).

## 10. After v0.1

In rough order, none of these are stubbed in the tree:

1. The in-guest agent shipped in v0.2 for Docker and gVisor. Still open: vsock, and dialers for Kata, Firecracker, and Kubernetes. Archives are packed in the agent when it is configured. Watches use inotify, with a poll fallback when inotify is unavailable.
2. Egress allow-lists, public preview hosts, and bandwidth limits shipped with the network work. Metadata and private-range blocking stay on by default.
3. Python and Go SDKs. OpenAPI code generation, if it earns its keep.
4. `sbx controller` and `sbx node`, Postgres, and a scheduler. A Kubernetes driver.
5. Kata and Firecracker.

## 11. Resolved questions

- The project name is sbx, in `github.com/TorstenDittmann/sbx`.
- The license is MIT.
- The default backend is Docker. gVisor is opt-in.
- E2B API compatibility is out of scope.
