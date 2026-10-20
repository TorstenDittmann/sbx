# API keys

Every API call except health, metrics, the OpenAPI document, and public previews needs an API key. Send it as a bearer token:

```bash
curl -s $SBX_URL/v1/sandboxes -H "Authorization: Bearer sbx_..."
```

Browsers and WebSocket clients cannot set headers on every request, so the same key is accepted as the `access_token` query parameter. Request logs record only the path, never the query.

Keys start with `sbx_`. The server stores a hash and a short prefix, never the key itself, so a lost key cannot be recovered. Create a new one.

## The first key

The first boot of a data directory generates an admin key, prints it once, and writes it to `data/initial_api_key` with mode `0600`. To choose that key yourself, set `auth.bootstrap_key` or `SBX_AUTH_BOOTSTRAP_KEY` (or `--bootstrap-key`) before the first boot. It is only used when the database has no keys yet.

## Scopes

| Scope | Can do |
|---|---|
| `admin` | Everything. Manages keys and templates, sees every sandbox and volume, lists warm pool sandboxes, and can run [builds](/docs/templates/#builds-and-permissions). |
| `sandbox` | Creates and uses sandboxes and volumes. Only sees what it created. Lists templates and backends. |

Give applications `sandbox` keys. A sandbox key that leaks can only touch its own sandboxes, and only up to its quota.

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
sbx keys list
```

`sbx keys list` prints each key's id, prefix, name, scopes, and state (`active`, `expired`, or `revoked`).

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

## Quotas

Each key can have at most `max_sandboxes_per_key` live sandboxes (default 20) and `requests_per_minute` requests (default 1200). Both return `429`.
