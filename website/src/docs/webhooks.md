# Webhooks

A webhook sends sandbox [lifecycle events](/docs/api/#lifecycle-events) to a URL you run, as signed `POST` requests. Use one when a long-lived `GET /v1/events` stream is awkward: a serverless function, a job queue, or a service that should react when a sandbox expires or is lost.

## Register a URL

```bash
curl -s $SBX_URL/v1/webhooks \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"url":"https://example.com/sbx-events","events":["sandbox.expired","sandbox.lost"]}'
```

```json
{
  "id": "wh_3f9a1c7e5b2d4a60",
  "url": "https://example.com/sbx-events",
  "description": "",
  "events": ["sandbox.expired", "sandbox.lost"],
  "enabled": true,
  "secret": "whsec_Zm9vYmFyYmF6cXV4cXV1eGNvcmdlZ3JhdWx0Z2FycGx5",
  "created_at": "2026-11-03T12:00:00Z",
  "updated_at": "2026-11-03T12:00:00Z",
  "last_attempt_at": null,
  "last_success_at": null,
  "last_status": 0,
  "last_error": "",
  "failures": 0
}
```

Store `secret` now: only this response and `POST /v1/webhooks/{id}/rotate-secret` return it. Leave out `events` to get every type.

A webhook gets the events a stream for its key would: a sandbox key's webhook hears about that key's sandboxes, an admin key's about all of them. It starts with the next event; nothing earlier is replayed. Revoking or expiring the key stops its webhooks.

`POST /v1/webhooks/{id}/test` sends a `webhook.test` event at once and returns `{"ok", "status_code", "error", "duration_ms"}`, so you can check the URL and your signature check before real events arrive.

## What arrives

The body is the same event object the stream sends:

```json
{"id":42,"type":"sandbox.expired","sandbox_id":"sbx_1a2b3c4d5e6f7a8b","time":"2026-11-03T12:05:00Z","data":{"status":"stopped","reason":"ttl","backend":"docker"}}
```

With these headers, following [Standard Webhooks](https://www.standardwebhooks.com/):

| Header | Value |
|---|---|
| `webhook-id` | `evt_<event id>`. The same on every retry, so use it to drop duplicates. |
| `webhook-timestamp` | Unix seconds when this attempt was signed. |
| `webhook-signature` | `v1,` and the base64 HMAC-SHA256 of `<webhook-id>.<webhook-timestamp>.<body>`. |
| `User-Agent` | `sbx-webhooks/<version>` |

The HMAC key is the base64-decoded part of the secret after `whsec_`.

## Verify the signature

Check the signature against the raw body, before parsing it, and reject old timestamps so a captured request cannot be replayed. Any Standard Webhooks library works; by hand it is a few lines.

```ts
import { createHmac, timingSafeEqual } from "node:crypto";

export function verify(secret: string, headers: Headers, body: string): boolean {
	const id = headers.get("webhook-id") ?? "";
	const ts = headers.get("webhook-timestamp") ?? "";
	if (Math.abs(Date.now() / 1000 - Number(ts)) > 300) return false;
	const key = Buffer.from(secret.replace(/^whsec_/, ""), "base64");
	const want = Buffer.from(createHmac("sha256", key).update(`${id}.${ts}.${body}`).digest("base64"));
	return (headers.get("webhook-signature") ?? "").split(" ").some((part) => {
		const got = Buffer.from(part.replace(/^v1,/, ""));
		return got.length === want.length && timingSafeEqual(got, want);
	});
}
```

```python
import base64, hashlib, hmac, time

def verify(secret: str, headers, body: bytes) -> bool:
    msg_id = headers["webhook-id"]
    ts = headers["webhook-timestamp"]
    if abs(time.time() - int(ts)) > 300:
        return False
    key = base64.b64decode(secret.removeprefix("whsec_"))
    want = base64.b64encode(hmac.new(key, f"{msg_id}.{ts}.".encode() + body, hashlib.sha256).digest()).decode()
    return any(hmac.compare_digest(part.removeprefix("v1,"), want) for part in headers["webhook-signature"].split())
```

## Delivery and retries

- Events for one webhook arrive in order, one at a time. Any `2xx` answer is a success; answer quickly and do slow work afterwards.
- A failed event (another status, a timeout after `webhooks.timeout_seconds`, or a connection error) is retried after 5 seconds, 30 seconds, 2 minutes, 10 minutes, 30 minutes, 1 hour, and 2 hours. Later events wait behind it.
- If the last retry fails too, sbx drops that event and disables the webhook. `GET /v1/webhooks/{id}` shows `enabled: false` and the reason in `last_error`. `PATCH` it with `{"enabled": true}` to resume after the dropped event; events that arrived meanwhile are sent while they are still kept (`retain_stopped_seconds`).
- Delivery is at least once. A server restart can resend an event that was already accepted.
- Redirects are not followed; a `3xx` counts as a failure.

`GET /v1/webhooks/{id}` also reports `last_attempt_at`, `last_success_at`, `last_status`, and `failures` since the last success.

## Network rules

Deliveries leave from the sbx server, not from a sandbox. To keep a webhook from reaching services next to the server, sbx refuses private, loopback, and link-local addresses, checked on the address it actually connects to, so a DNS name cannot point there either. Cloud metadata addresses are always refused. An operator who wants webhooks on an internal network sets [`webhooks.allow_private`](/docs/configuration/#webhooks), or lists the range in `network.private_allow`. Proxy environment variables are not used for deliveries.
