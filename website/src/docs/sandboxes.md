# Sandboxes

A sandbox is one isolated container or microVM. You create it, use it through the API, and delete it, or let the server delete it when its TTL runs out.

## Create

```bash
curl -s $SBX_URL/v1/sandboxes \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "template": "python",
    "ttl_seconds": 600,
    "idle_timeout_seconds": 120,
    "cpu": 1,
    "memory_mb": 1024,
    "labels": {"project": "demo"},
    "env": {"LOG_LEVEL": "debug"},
    "secrets": {"OPENAI_API_KEY": "sk-..."}
  }'
```

Pass a `template` or an `image`. Everything else is optional: fields you leave out come from the template, then from the server defaults.

| Field | Meaning |
|---|---|
| `template` | A [template](/docs/templates/) name. Supplies the image, resources, env, backend, and network default. |
| `image` | An OCI image. Overrides the template's image. |
| `steps` | Build steps to apply on top of the image. See [Templates and builds](/docs/templates/#build-steps). |
| `cpu` | CPU cores, fractional allowed. Default 1, max 4. |
| `memory_mb` | Memory in MiB, without swap. Default 512, max 8192. |
| `disk_mb` | Size of the writable `/workspace`. Default 1024, max 10240. |
| `pids` | Process limit. Default 256, max 4096. |
| `ttl_seconds` | Lifetime. Default 300, max 86400. |
| `idle_timeout_seconds` | Stop after this long without API activity. 0 (the default) turns it off. |
| `env` | Environment variables, returned by get and list. |
| `secrets` | Environment variables that are never returned. See [Secrets](#secrets). |
| `labels` | Key-value pairs for filtering. |
| `network` | Network policy. See [Network](/docs/network/). |
| `backend` | `docker`, `gvisor`, `kata`, or `firecracker`. See [Backends](/docs/backends/). |
| `mounts` | Volumes to attach. See [Volumes](/docs/volumes/). |

The defaults and maximums above are the server defaults. An operator can change them under `limits` in the [configuration](/docs/configuration/#limits).

A create returns `201` with the sandbox once it is running. Sandbox ids look like `sbx_` followed by 16 hex characters.

## Lifecycle

| Status | Meaning |
|---|---|
| `creating` | The server is pulling the image, building steps, or starting the container. |
| `running` | Ready for exec, files, terminals, and previews. |
| `paused` | Frozen with `POST /v1/sandboxes/{id}/pause`. Resume with `POST …/resume`. |
| `stopped` | Deleted by the API or the reaper. The row is kept for a day (`retain_stopped_seconds`), then pruned. |
| `error` | Creation failed. |
| `warm` | Started ahead of time for a template's warm pool. Hidden from normal lists. |

A sandbox that stays in `creating` for longer than 15 minutes (`creating_timeout_seconds`) is reaped.

### TTL and idle timeout

The TTL is enforced by the server, not the client. A reaper runs every 5 seconds and deletes sandboxes whose `expires_at` has passed. Extend the lifetime with:

```bash
curl -s $SBX_URL/v1/sandboxes/$ID/timeout \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"ttl_seconds": 900}'
```

`ttl_seconds` here is the new remaining lifetime from now, up to `max_ttl_seconds`.

With `idle_timeout_seconds`, the reaper also stops a sandbox that has had no activity for that long. Exec, file operations, and other calls that use the sandbox count as activity.

### Restarts

On startup the server adopts sandboxes whose containers are still running and marks the ones that disappeared as stopped. `/readyz` returns `503` until that reconcile finishes. Containers that carry sbx labels but have no row in the database are removed as orphans.

## Security defaults

Every sandbox starts with:

- no network interface except loopback (`network: none`)
- all Linux capabilities dropped and `no-new-privileges`
- uid and gid 1000
- a read-only root filesystem, with a writable tmpfs at `/workspace` of `disk_mb` and a small `/tmp`
- memory without swap and a PID limit

Commands run in `/workspace` unless you pass `cwd`. To let package managers such as `apt` write the root filesystem, the operator sets `docker.read_only_rootfs: false` and `docker.sandbox_root: true`. That is a server-wide choice. For most cases, [build steps](/docs/templates/#build-steps) are the better way to install packages.

## Exec

```bash
curl -s $SBX_URL/v1/sandboxes/$ID/exec \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"cmd":["sh","-c","echo $GREETING"],"env":{"GREETING":"hi"},"timeout_seconds":30}'
```

`cmd` is an argument array, not a shell string. Use `["sh", "-c", "…"]` for shell syntax. The SDK turns a string into `["sh", "-lc", "…"]` for you.

Options are `cwd`, `env`, `timeout_seconds` (default 60, max 3600), and `stdin` or `stdin_base64`. A command that runs past its timeout is killed and returns exit code `137`. Output over 8 MiB is cut off and the result has `truncated: true`.

`?stream=1` returns server-sent events as the command runs: `stdout` and `stderr` with a `data` field, then `exit` with `exit_code`, or `error`.

For long-running processes, interactive shells, and file watches, see [sbx-agent](/docs/agent/).

## Files

| Call | Route |
|---|---|
| List a directory | `GET /v1/sandboxes/{id}/files?path=/workspace` |
| Read a file | `GET /v1/sandboxes/{id}/files/content?path=…` |
| Write a file | `PUT /v1/sandboxes/{id}/files/content?path=…&mode=644` with the raw bytes |
| Delete | `DELETE /v1/sandboxes/{id}/files?path=…` |
| Stat | `GET /v1/sandboxes/{id}/files/stat?path=…` |
| Make a directory | `POST /v1/sandboxes/{id}/files/mkdir` with `{"path": "…"}` |
| Upload a tar or tar.gz | `POST /v1/sandboxes/{id}/files/archive?path=…` |
| Download a tar.gz | `GET /v1/sandboxes/{id}/files/archive?path=…` |

Paths are absolute. File uploads are capped at 32 MiB (`max_upload_bytes`). Archive uploads are capped at 256 MiB, before and after gunzip (`max_archive_bytes`). A body over the cap gets `413 payload_too_large` with the limit and the setting that raises it in the message.

## Labels and search

Labels are up to 32 pairs. Keys start with a letter or digit, use letters, digits, `_`, `.`, and `-`, and are at most 63 characters. The `sbx.` prefix is reserved. Values are at most 256 characters.

`GET /v1/sandboxes` takes filters that combine:

- `status`, `backend`, and `template`: exact matches
- `label=key:value`: repeatable, all must match
- `q`: a case-insensitive substring of the id, template, image, status, and label keys and values, at most 128 characters. Env values are not searched.

```bash
curl -s "$SBX_URL/v1/sandboxes?template=python&label=project:demo&q=3f2a" \
  -H "Authorization: Bearer $SBX_API_KEY"
```

A sandbox key only sees sandboxes it created. An admin key sees all of them, and can list warm pool sandboxes with `status=warm`.

## Secrets

`secrets` are environment variables that the API stores but never returns. Get and list show their names in `secret_keys`. They are set in the container and in every later exec, process, and terminal. A name cannot be in both `env` and `secrets`.

Replace the whole map on a running sandbox:

```bash
curl -s -X PUT $SBX_URL/v1/sandboxes/$ID/secrets \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"secrets": {"OPENAI_API_KEY": "sk-new"}}'
```

New execs see the new values. A shell that was already running keeps the old ones, because the container's original environment does not change.

Env and secret names must match `[A-Za-z_][A-Za-z0-9_]*`, at most 64 variables, values at most 32 KiB. Names that start with `SBX_AGENT_` are reserved.

## Quotas

Each API key can have at most 20 live sandboxes (`max_sandboxes_per_key`), and the whole server at most 100 (`max_sandboxes`). Going over either returns `429 quota_exceeded`. Each key is also limited to 1200 requests per minute (`requests_per_minute`), which returns `429 rate_limited`.
