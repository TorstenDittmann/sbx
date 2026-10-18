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
                 │  Engine API
                 ▼
            sandbox container
```

Later backends (Kata, Firecracker, Kubernetes) and a controller/node split implement the same driver surface. They are not in v0.1, and there are no stub packages for them.

### v0.1 decisions

- The command is `sbx serve`. `sbx server` is an alias. `controller` and `node` are not implemented.
- The default backend is `docker`. `gvisor` is the same driver with runtime `runsc`. If that runtime is missing, create returns HTTP 400 `unsupported`. Startup fails only when the configured default backend is gVisor and `runsc` is absent.
- `api/openapi.yaml` is the contract. Handlers are written by hand to match it. Code generation is deferred.
- There is no in-guest agent. Exec and files use the Engine API. The Docker archive endpoints refuse a read-only rootfs and do not see tmpfs mounts, so file transfer is `cat`, `find`, and `tar` inside an exec. Images need `sh`, `cat`, `mkdir`, `chmod`, `rm`, `find`, `stat`, and `tar`. Distroless images will not work until an agent exists.
- The workspace disk limit is the size of a tmpfs on `/workspace` (uid/gid 1000, mode 755). `/tmp` is a smaller tmpfs. The root filesystem is read-only by default, so the limit is real.
- `network: none` is the default. `network: full` joins a dedicated bridge and rejects metadata, link-local, and private destinations (see the security baseline). An egress allow-list is rejected with HTTP 400 `unsupported`.
- State is SQLite at `$data_dir/sbx.db` (WAL, one connection). Postgres is a cluster concern.
- The landing page is the static site in `website/`, deployed with Dokploy. See [website.md](website.md). There is no GitHub Pages workflow.

### Driver interface

`internal/driver.Driver` is the extension point: ping, ensure image, create, delete, inspect, list, pause, resume, exec, exec stream, and file list/read/write/remove. v0.1 Docker and gVisor satisfy files and exec directly. A later driver can satisfy the same methods with an in-guest agent.

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
GET    /v1/sandboxes?status=&backend=&label=k:v
GET    /v1/sandboxes/{id}
DELETE /v1/sandboxes/{id}                 idempotent stop
POST   /v1/sandboxes/{id}/timeout         {"ttl_seconds": 600}
POST   /v1/sandboxes/{id}/pause
POST   /v1/sandboxes/{id}/resume
POST   /v1/sandboxes/{id}/exec            wait
POST   /v1/sandboxes/{id}/exec?stream=1   SSE: stdout, stderr, exit, error

GET    /v1/sandboxes/{id}/files?path=
GET    /v1/sandboxes/{id}/files/content?path=
PUT    /v1/sandboxes/{id}/files/content?path=&mode=644
DELETE /v1/sandboxes/{id}/files?path=
POST   /v1/sandboxes/{id}/files/archive?path=
GET    /v1/sandboxes/{id}/files/archive?path=

GET    /v1/templates
POST   /v1/templates                      admin
DELETE /v1/templates/{name}               admin; built-ins return 403
POST   /v1/keys                           admin
GET    /v1/keys                           admin

GET    /  /healthz  /metrics  /openapi.yaml
```

Create accepts `template` or `image`, resource fields, `ttl_seconds`, `idle_timeout_seconds`, `env`, `network` (`none` or `full`), `backend` (`docker` or `gvisor`), and `labels`. Unknown JSON fields are rejected. Built-in templates, replaced on every start, are `base` (`alpine:3.20`), `python` (`python:3.12-slim`), and `node` (`node:22-bookworm-slim`).

Errors are `{"error":{"code","message"}}`. Create is 201. The sandbox `node` field is always `"local"`.

Defaults: bind `127.0.0.1:8080` (compose sets `SBX_ADDR=:8080`), TTL 300s (max 86400), idle timeout off, 1 CPU, 512 MiB, 1024 MiB workspace, 256 pids, 20 sandboxes per key, exec timeout 60s (max 3600), output cap 8 MiB, upload cap 32 MiB. The reaper runs every 5s and stops expired sandboxes, idle sandboxes, creates stuck longer than 15 minutes, and orphan containers labeled `sbx.managed=true`. Stopped rows are pruned after a day.

## 6. SDK

`sdk/typescript` is `@sbx/sdk`. It has no runtime dependencies. `Sandbox.create` reads `SBX_URL` and `SBX_API_KEY`. Durations such as `"5m"` are converted to seconds in the SDK; the server wants integers. A string command is run as `sh -lc`. Python and Go SDKs are later.

## 7. Layout

```
cmd/sbx/                 binary
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

Config file, then environment, then flags. See `sbx.yaml.example`. Useful variables: `SBX_ADDR`, `SBX_DATA_DIR`, `SBX_BACKEND`, `SBX_DOCKER_HOST`, `SBX_GVISOR_RUNTIME`, `SBX_READ_ONLY_ROOTFS`, `SBX_BOOTSTRAP_KEY`, `SBX_LOG_LEVEL`.

The website is separate. [Deploying the website with Dokploy](website.md#deploying-the-website-with-dokploy) covers its build path, Dockerfile, and port.

## 9. Security baseline

Sandboxes are created with `CapDrop: ["ALL"]`, `no-new-privileges`, a non-root user, memory equal to memory+swap (no swap), a PID limit, private IPC, read-only rootfs, and `network=none` unless `full` is requested. The Docker socket is not mounted into sandboxes. API keys are hashed. TTLs and the per-key sandbox quota are enforced in the server, not left to the caller.

`network: full` is an explicit opt-in to public egress. It is not an allow-list. Sandboxes on that mode join the `sbx` bridge (`sbx0`, `172.30.0.0/16`, ICC off, IPv6 off). sbx keeps an `SBX-EGRESS` chain jumped from `DOCKER-USER` and from `INPUT` for packets that arrive on `sbx0`. The chain rejects:

- `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`
- `169.254.0.0/16`, which covers `169.254.169.254`
- `100.100.100.200/32` (Alibaba Cloud metadata; the rest of `100.64.0.0/10` is left open)
- when `ip6tables` has a `DOCKER-USER` chain: `fe80::/10`, `fc00::/7`, and `::1/128`

The rules are installed once per process, before the first `network: full` container is created. A root server runs `iptables` directly. A non-root server (the usual Compose setup, which only mounts `/var/run/docker.sock`) starts a privileged `alpine:3.20` helper on the host network and host PID namespace and applies the host's `iptables` via `nsenter`. If both paths fail, create returns an error and does not start the container. There is no switch to disable the filter.

## 10. After v0.1

In rough order, none of these are stubbed in the tree:

1. An in-guest agent so exec, PTY, and files work on images without a shell, and on VM backends.
2. Egress allow-lists and preview ports. Metadata and private-range blocking for `network: full` is already on.
3. Python and Go SDKs. OpenAPI code generation, if it earns its keep.
4. `sbx controller` and `sbx node`, Postgres, and a scheduler. A Kubernetes driver.
5. Kata and Firecracker.

## 11. Resolved questions

- The project name is sbx, in `github.com/TorstenDittmann/sbx`.
- The license is MIT.
- The default backend is Docker. gVisor is opt-in.
- E2B API compatibility is out of scope.
