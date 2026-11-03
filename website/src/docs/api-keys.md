# API keys

Every API call except health, metrics, the OpenAPI document, and public previews needs an API key. Send it as a bearer token:

```bash
curl -s $SBX_URL/v1/sandboxes -H "Authorization: Bearer sbx_..."
```

Browsers and WebSocket clients cannot set headers on every request, so the same key is accepted as the `access_token` query parameter. Request logs record only the path, never the query.

Keys start with `sbx_`. The server stores a hash and a short prefix, never the key itself, so a lost key cannot be recovered. Create a new one.

## The first key

The first boot of a data directory generates an admin key, prints it once, and writes it to `data/initial_api_key` with mode `0600`. To choose that key yourself, set `auth.bootstrap_key` or `SBX_BOOTSTRAP_KEY` (or `--bootstrap-key`) before the first boot. It is only used when the database has no keys yet.

## Scopes

| Scope | Can do |
|---|---|
| `admin` | Everything. Manages keys and templates, sees every sandbox and volume, lists warm pool sandboxes, and can run [builds](/docs/templates/#builds-and-permissions). |
| `sandbox` | Creates and uses sandboxes and volumes. Only sees what it created, or what its [project](#projects) created. Lists templates and backends. |
| `read` | Read-only. Lists and gets sandboxes, volumes, templates, and backends, reads files (except under `/proc`, `/sys`, and `/dev`, which would expose a sandbox's secrets), stats, process output, and the event stream. Cannot create, delete, exec, write files, open terminals, or reach previews. |

Each scope includes the ones below it. Give applications `sandbox` keys. A sandbox key that leaks can only touch its own sandboxes, and only up to its [quota](#quotas). Give dashboards, log shippers, and monitoring `read` keys, usually bound to a project so they have something to see.

## Create a key

Over the API, with an admin key:

```bash
curl -s $SBX_URL/v1/keys \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"ci","scopes":["sandbox"],"expires_at":"2027-01-01T00:00:00Z"}'
```

The response includes `key` once. Store it then. `scopes` defaults to `["sandbox"]`. `expires_at` is optional and must be in the future.

Or on the host, against the data directory:

```bash
sbx keys create --name ci
sbx keys create --name ops --admin --expires 720h
sbx keys create --name grafana --read-only --project acme
sbx keys create --name agent --project acme --max-sandboxes 50 --max-cpu 16 --max-memory-mb 32768
sbx keys list
```

`sbx keys list` prints each key's id, prefix, name, scopes, project (`-` for none), and state (`active`, `expired`, or `revoked`).

From the [SDK](/docs/sdk/#keys):

```ts
const client = new SbxClient()
const created = await client.createKey("ci", { scopes: ["sandbox"], expiresAt: new Date("2027-01-01") })
console.log(created.key) // shown once
```

## Expiry and revocation

A key with `expires_at` stops authenticating at that time. Revoking a key also takes effect at once, on the next request:

```bash
curl -s -X DELETE $SBX_URL/v1/keys/$KEY_ID -H "Authorization: Bearer $SBX_API_KEY"
```

```bash
sbx keys revoke --id $KEY_ID
```

Revoking twice is fine and keeps the first `revoked_at`. Sandboxes and volumes that the key owns are not deleted; an admin can inspect or delete them.

The API refuses to revoke the last usable admin key and returns `409 conflict`, so the server cannot lock its operator out. `sbx keys revoke` on the host can revoke it, because the host is the recovery path: run `sbx keys create --admin` to make another.

`GET /v1/keys` and `GET /v1/keys/{id}` return `last_used_at` (updated at most once a minute), `expires_at`, and `revoked_at`.

## Network policy per key

A key can carry a network policy for the sandboxes it creates:

```json
{
  "name": "agent",
  "network": {
    "mode": "allow",
    "max_mode": "allow",
    "allow": ["pypi.org", "files.pythonhosted.org", "github.com"],
    "egress_kbps": 5000,
    "max_connections": 64
  }
}
```

`mode` and `allow` are defaults that a sandbox may override. Add `max_mode` to make them a ceiling: a sandbox that asks for a looser mode gets `403`, and with `max_mode: allow` every allow entry on a sandbox must be one of the key's hostnames or inside one of its CIDRs. `deny` is always added to the server's deny list. See [Network](/docs/network/#inheritance) for the full rules.

## Private registries

A key can carry credentials for private image registries. sbx uses them when the key creates a sandbox, a template, or a [build](/docs/templates/#build-steps) whose image or base lives on that registry. They take precedence over the server's [`registries`](/docs/configuration/#registries) for the same server.

```json
{
  "name": "team-a",
  "registries": [
    {"server": "ghcr.io", "username": "team-a-bot", "password": "ghp_..."},
    {"server": "registry.example.com", "identity_token": "..."}
  ]
}
```

Send `registries` on create, or replace the list later. An empty list removes them:

```bash
curl -s -X PUT $SBX_URL/v1/keys/$KEY_ID/registries \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"registries":[{"server":"ghcr.io","username":"team-a-bot","password":"ghp_..."}]}'
```

Passwords and tokens are write-only. Key responses list each registry's `server` and `username` only. They are stored in the server's SQLite database as given, like sandbox secrets, so protect the data directory.

Docker keeps one image cache for every caller. So that one key's pull does not hand the image to everyone, sbx records a repository the first time a key's own credential fetches it. After that, every use of the repository, by tag or by digest, asks the registry again with the caller's credential, and a key without access gets an image error instead of the cached copy. The same check runs before a cached [build](/docs/templates/#build-steps) on that base is reused, and when a built `sbx-template:` image is named directly. While such images exist, a bare image ID is refused, because it cannot be checked. Warm pools belong to no key, so they only use the server's credentials.

## Projects

A key can belong to a project. Keys of the same project see and use each other's sandboxes and volumes, and the event stream includes all of them. Keys without a project, and keys of other projects, still see only their own.

```bash
curl -s $SBX_URL/v1/keys \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"acme-ci","project":"acme"}'
```

A project name is 1 to 64 letters, digits, `.`, `_`, or `-`, starting with a letter or digit. It is set when the key is created and cannot change. Admin keys see every project already, so they cannot be bound to one.

Pair a project with the `read` scope for a key that can watch a team's sandboxes but not change them:

```json
{"name": "acme-dashboard", "scopes": ["read"], "project": "acme"}
```

Quotas stay per key. Project membership changes what a key can see, not what it may create.

## Quotas

Every key is limited by the server: at most `max_sandboxes_per_key` live sandboxes (default 20) and `requests_per_minute` requests (default 1200). Both return `429`.

A key can also carry its own quota:

```json
{
  "name": "agent",
  "quota": {"max_sandboxes": 50, "max_cpu": 16, "max_memory_mb": 32768}
}
```

| Field | Limits | `0` or missing |
|---|---|---|
| `max_sandboxes` | Live sandboxes. Replaces `max_sandboxes_per_key` for this key, up or down. | The server's `max_sandboxes_per_key` |
| `max_cpu` | Total CPUs across live sandboxes | No total limit |
| `max_memory_mb` | Total memory across live sandboxes | No total limit |

Live means creating, running, or paused: a paused sandbox keeps its reservation. A create that would go past any limit returns `429 quota_exceeded` with the limit, what is in use, and what was asked for. Each sandbox is still held to the server's per-sandbox `max_cpu` and `max_memory_mb`, and the node to `max_sandboxes`.

Change a quota later with an admin key. Sandboxes already running are not stopped when it shrinks; the key just cannot create more until it is back under:

```bash
curl -s -X PUT $SBX_URL/v1/keys/$KEY_ID/quota \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"max_sandboxes":10,"max_cpu":8}'
```

Any key can check its own headroom:

```bash
curl -s $SBX_URL/v1/me -H "Authorization: Bearer $SBX_API_KEY"
```

```json
{
  "id": "key_...",
  "scopes": ["sandbox"],
  "project": "acme",
  "quota": {"max_sandboxes": 0, "max_cpu": 8, "max_memory_mb": 0},
  "limits": {"max_sandboxes": 20, "max_cpu": 8, "max_memory_mb": 0},
  "usage": {"sandboxes": 3, "cpu": 3, "memory_mb": 1536}
}
```

`limits.max_sandboxes` is the effective limit, with the server default filled in.

From the SDKs:

```ts
await client.createKey("agent", { project: "acme", quota: { maxSandboxes: 50, maxCpu: 16 } })
await client.setKeyQuota(keyId, { maxMemoryMb: 32768 })
const { usage, limits } = await client.me()
```

```python
client.create_key("agent", project="acme", quota=KeyQuota(max_sandboxes=50, max_cpu=16))
client.set_key_quota(key_id, {"max_memory_mb": 32768})
print(client.me().usage)
```
