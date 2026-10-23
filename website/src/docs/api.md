# HTTP API

The server also serves this contract as OpenAPI 3.1 at [`/openapi.yaml`](https://github.com/TorstenDittmann/sbx/blob/main/api/openapi.yaml). This page walks through it by resource.

## Basics

- **Base URL**: `http://127.0.0.1:8080` by default, or your `public_url`.
- **Auth**: `Authorization: Bearer sbx_…` on every `/v1` route. Browsers and WebSocket clients can pass `?access_token=sbx_…` instead. See [API keys](/docs/api-keys/).
- **Scopes**: routes marked **admin** need an admin key. Everything else accepts a sandbox key, which only sees its own sandboxes and volumes.
- **Bodies**: JSON, except file and archive uploads, which are raw bytes (`application/octet-stream`).
- **Durations** are whole seconds (`ttl_seconds`, `timeout_seconds`). Times are RFC 3339 in UTC.
- **IDs**: sandboxes `sbx_<16 hex>`, volumes `vol_<16 hex>`, previews `prv_<16 hex>`, keys `key_<16 hex>`.

### Errors

Errors have one shape:

```json
{ "error": { "code": "not_found", "message": "sandbox not found" } }
```

| Status | Code | When |
|---|---|---|
| 400 | `invalid` | The request failed validation. |
| 400 | `unsupported` | The backend or agent cannot do this (for example terminals without `sbx-agent`, or a backend that is not installed). |
| 401 | `unauthorized` | Missing, unknown, expired, or revoked key. |
| 403 | `forbidden` | The key's scope or network ceiling does not allow it, or a build without build permission. |
| 404 | `not_found` | No such resource, or it belongs to another key. |
| 409 | `conflict` | A name is taken, a volume is in use, or the last admin key would be revoked. |
| 413 | `payload_too_large` | The body is over the limit. |
| 429 | `quota_exceeded` | Sandbox quota per key or per server reached. |
| 429 | `rate_limited` | Too many requests for this key. |
| 502 | `driver` | The backend failed. |
| 504 | `timeout` | The backend did not answer in time. |
| 500 | `internal` | Something else went wrong. |

### Streams

Streaming routes use server-sent events. Exec and process streams send `stdout` and `stderr` events with `{"data": "…"}`, then `exit` with `{"exit_code", "duration_ms", "truncated"}`, or `error` with `{"code", "message"}`.

## Service

These routes need no key.

| Route | Returns |
|---|---|
| `GET /` | `{"name", "version", "health", "openapi", "dashboard"}`. A browser (`Accept: text/html`) is redirected to `/ui/`. |
| `GET /ui/` | The [dashboard](/docs/dashboard). Static files; sign-in uses an API key. |
| `GET /healthz` | `{"status", "version", "backend", "docker"}`. `503` when the backend does not answer. |
| `GET /readyz` | Same body. `503` until startup reconcile finishes. |
| `GET /metrics` | Prometheus text. |
| `GET /openapi.yaml` | The OpenAPI document. |

## Sandboxes

### Create a sandbox

`POST /v1/sandboxes` returns `201` with a [Sandbox](#the-sandbox-object).

| Field | Type | Notes |
|---|---|---|
| `template` | string | Template name. |
| `image` | string | OCI image. One of `template` or `image` is required. |
| `steps` | BuildStep[] | Run after the template's steps. Needs build permission. See [Templates and builds](/docs/templates/#build-steps). |
| `cpu` | number | Cores. |
| `memory_mb` | integer | |
| `disk_mb` | integer | Size of the `/workspace` tmpfs. |
| `pids` | integer | |
| `ttl_seconds` | integer | Lifetime. |
| `idle_timeout_seconds` | integer | `0` turns it off. |
| `env` | object | String values. |
| `secrets` | object | Never returned. Names appear in `secret_keys`. |
| `labels` | object | String values. |
| `network` | [Network](#network-object) | |
| `backend` | string | `docker`, `gvisor`, `kata`, or `firecracker`. |
| `mounts` | Mount[] | Up to 8 of `{"volume_id", "path"}`. |

#### Network object

| Field | Type | Notes |
|---|---|---|
| `mode` | string | `none`, `full`, or `allow`. |
| `allow` | string[] | Hostnames, IPs, or CIDRs. An empty array clears an inherited list. |
| `deny` | string[] | Added to the server and key deny lists. |
| `egress_kbps` | integer | `0` inherits. |
| `max_connections` | integer | `0` inherits. |

See [Network](/docs/network/) for how these combine.

### The sandbox object

```json
{
  "id": "sbx_3f2a9c1e7b6d4a10",
  "status": "running",
  "backend": "docker",
  "template": "python",
  "image": "python:3.12-slim",
  "cpu": 1,
  "memory_mb": 512,
  "disk_mb": 1024,
  "pids": 256,
  "network": "allow",
  "egress_allow": ["pypi.org"],
  "egress_deny": [],
  "egress_kbps": 0,
  "max_connections": 0,
  "mounts": [{ "volume_id": "vol_8b1d2e4f6a0c3b5d", "path": "/workspace" }],
  "env": { "LOG_LEVEL": "debug" },
  "labels": { "project": "demo" },
  "secret_keys": ["OPENAI_API_KEY"],
  "idle_timeout_seconds": 0,
  "expires_at": "2026-10-20T12:10:00Z",
  "created_at": "2026-10-20T12:00:00Z",
  "node": "host-1"
}
```

`status` is `creating`, `running`, `paused`, `stopped`, `error`, or `warm`.

### List sandboxes

`GET /v1/sandboxes` returns `{"sandboxes": [...]}`.

| Query | Notes |
|---|---|
| `status` | Exact. `warm` is admin only. |
| `backend` | Exact. |
| `template` | Exact. |
| `label` | `key:value`. Repeatable; all must match. |
| `q` | Case-insensitive substring of id, template, image, status, and label keys and values. Up to 128 characters. |

### Get, delete, and change a sandbox

| Route | Body | Returns |
|---|---|---|
| `GET /v1/sandboxes/{id}` | | Sandbox |
| `DELETE /v1/sandboxes/{id}` | | The stopped Sandbox |
| `POST /v1/sandboxes/{id}/timeout` | `{"ttl_seconds": 900}` | Sandbox with the new `expires_at` |
| `PUT /v1/sandboxes/{id}/secrets` | `{"secrets": {"NAME": "value"}}` | Sandbox. Replaces the whole map. |
| `POST /v1/sandboxes/{id}/pause` | | Paused Sandbox |
| `POST /v1/sandboxes/{id}/resume` | | Running Sandbox |

## Exec

### Run a command

`POST /v1/sandboxes/{id}/exec`

| Field | Type | Notes |
|---|---|---|
| `cmd` | string[] | Required. 1 to 64 arguments. Not a shell string. |
| `cwd` | string | Default `/workspace`. |
| `env` | object | Added for this command. |
| `timeout_seconds` | integer | Default 60, max 3600. A timeout exits `137`. |
| `stdin` | string | UTF-8 input. |
| `stdin_base64` | string | Binary input. Not with `stdin`. |

Returns:

```json
{ "exit_code": 0, "stdout": "hello\n", "stderr": "", "duration_ms": 12, "truncated": false }
```

With `?stream=1` the response is a server-sent event stream instead.

## Files

All file routes take `?path=` with an absolute path inside the sandbox.

| Route | Body | Returns |
|---|---|---|
| `GET /v1/sandboxes/{id}/files` | | `{"path", "entries": [FileEntry]}` |
| `DELETE /v1/sandboxes/{id}/files` | | `204` |
| `GET /v1/sandboxes/{id}/files/content` | | Raw bytes |
| `PUT /v1/sandboxes/{id}/files/content` | Raw bytes. Optional `&mode=644`. | `204` |
| `GET /v1/sandboxes/{id}/files/stat` | | FileEntry. Needs the agent. |
| `POST /v1/sandboxes/{id}/files/mkdir` | `{"path", "mode"}` | `204` |
| `GET /v1/sandboxes/{id}/files/archive` | | gzip tar of the directory |
| `POST /v1/sandboxes/{id}/files/archive` | tar or gzip tar | `204` |
| `GET /v1/sandboxes/{id}/files/watch` | `&recursive=1` | Event stream. Needs the agent. |

A FileEntry is `{"name", "path", "type", "size", "mode", "mtime"}`, where `type` is `file`, `dir`, or `symlink`. The watch stream sends `ready`, then `change` events with `{"type", "path"}`, where `type` is `create`, `write`, or `remove`.

## Processes

Need [sbx-agent](/docs/agent/).

| Route | Body | Returns |
|---|---|---|
| `POST /v1/sandboxes/{id}/processes` | Same as exec. `timeout_seconds: 0` means none. | `201` Process |
| `GET /v1/sandboxes/{id}/processes` | | `{"processes": [Process]}` |
| `GET /v1/sandboxes/{id}/processes/{procId}` | | Process |
| `DELETE /v1/sandboxes/{id}/processes/{procId}` | | `204` |
| `POST …/processes/{procId}/signal` | `{"signal": "TERM"}` | `204`. `TERM`, `KILL`, `INT`, `HUP`, or `QUIT`. |
| `POST …/processes/{procId}/stdin` | Raw bytes | `204`. Stdin stays open. |
| `GET …/processes/{procId}/stream` | | Event stream, including buffered output. |

A Process is `{"id", "pid", "cmd", "cwd", "tty", "status", "exit_code", "started_at"}`, where `status` is `running` or `exited`.

## Terminals

Need [sbx-agent](/docs/agent/#terminals).

| Route | Body | Returns |
|---|---|---|
| `POST /v1/sandboxes/{id}/terminals` | `{"cmd", "cwd", "env", "cols", "rows"}` | `201` `{"id", "pid", "cols", "rows"}` |
| `POST …/terminals/{termId}/resize` | `{"cols", "rows"}` | `204` |
| `DELETE …/terminals/{termId}` | | `204` |
| `GET …/terminals/{termId}/ws` | | WebSocket upgrade |

On the WebSocket, binary frames are bytes in both directions. Text frames are JSON: `{"type":"resize","cols":80,"rows":24}` from the client, `{"type":"exit","exit_code":0}` from the server.

## Previews

Need [sbx-agent](/docs/agent/). See [Previews](/docs/previews/).

| Route | Body | Returns |
|---|---|---|
| `POST /v1/sandboxes/{id}/previews` | `{"port": 3000, "public": false}` | `201` Preview, with `token` once |
| `GET /v1/sandboxes/{id}/previews` | | `{"previews": [Preview]}` |
| `DELETE /v1/sandboxes/{id}/previews/{previewId}` | | `204` |
| `ANY /v1/sandboxes/{id}/previews/{previewId}/…` | | Proxied to the port, with an API key |
| `ANY /p/{sandboxId}/{port}/…` | | Proxied to the port, public or with a token |

A Preview is `{"id", "sandbox_id", "port", "public", "url", "public_url", "host", "token", "created_at"}`. The public routes also serve `Host: {port}-sbx-{16 hex}.{preview_domain}`. A non-public preview without a credential returns `401`.

## Templates and builds

| Route | Scope | Body | Returns |
|---|---|---|---|
| `GET /v1/templates` | | | `{"templates": [Template]}` |
| `POST /v1/templates` | **admin** | See below | `201` Template |
| `DELETE /v1/templates/{name}` | **admin** | | `204`. Built-ins return `403`. |
| `POST /v1/builds` | build permission | `{"image", "template", "steps", "backend"}` | `201` built, or `200` cached |

`POST /v1/templates` takes `name` and `image` (both required), `description`, `cpu`, `memory_mb`, `disk_mb`, `env`, `backend`, `network` (`none` or `full`), `setup` (a command array), `setup_timeout_seconds`, `warm` (0 to 8), and `steps` (up to 64). Steps are built during the request, so a failing step fails it. A taken name returns `409`.

A BuildStep sets exactly one of `run` (string), `packages` (string array), `env` (object), or `workdir` (string).

A Build is `{"id", "image", "base", "steps", "cached", "duration_ms"}`. `image` is `sbx-template:<id>`, or the base itself when no step runs a command.

Build permission means an admin key, or any key when `builds.allow_sandbox_keys` is true. Without it, `POST /v1/builds`, a create with `steps`, and a create that sets `image` on a template with steps return `403`.

## Volumes

| Route | Body | Returns |
|---|---|---|
| `POST /v1/volumes` | `{"name", "backend", "if_exists"}` | `201` Volume, or `200` when `if_exists` is `reuse` and the volume exists |
| `GET /v1/volumes` | | `{"volumes": [Volume]}` |
| `GET /v1/volumes/{id}` | | Volume |
| `DELETE /v1/volumes/{id}` | | `204`, or `409` while a live sandbox mounts it |

A Volume is `{"id", "name", "backend", "created_at"}`. `backend` is `docker`, `gvisor`, or `kata`.

## Backends

`GET /v1/backends` returns `{"backends": [Backend]}`, one per backend, with `name`, `available`, `detail`, `isolation`, the booleans `pause`, `snapshot`, `exec`, `pty`, `files`, `watch`, and `preview`, `networks`, and `requirements`. See [Backends](/docs/backends/#what-this-host-can-run).

## API keys

All **admin**.

| Route | Body | Returns |
|---|---|---|
| `POST /v1/keys` | `{"name", "scopes", "expires_at", "network"}` | `201` Key with `key` once |
| `GET /v1/keys` | | `{"keys": [Key]}` |
| `GET /v1/keys/{id}` | | Key |
| `DELETE /v1/keys/{id}` | | `204`, or `409` for the last usable admin key |

A Key is `{"id", "name", "prefix", "scopes", "created_at", "last_used_at", "expires_at", "revoked_at", "network"}`. `network` takes `mode`, `max_mode`, `allow`, `deny`, `egress_kbps`, and `max_connections`. See [API keys](/docs/api-keys/#network-policy-per-key).
