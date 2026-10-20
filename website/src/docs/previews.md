# Previews

A preview gives you a URL for a port that a process inside the sandbox listens on, such as a dev server or a web app an agent just built. The server reaches the port through `sbx-agent` on the sandbox's loopback, so previews work even with `network: none`.

Previews need [sbx-agent](/docs/agent/). Without it the route returns `unsupported`.

## Create a preview

Start something that listens, then publish its port:

```bash
curl -s $SBX_URL/v1/sandboxes/$ID/processes \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"cmd":["python","-m","http.server","3000"],"timeout_seconds":0}'

curl -s $SBX_URL/v1/sandboxes/$ID/previews \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"port":3000}'
```

```json
{
  "id": "prv_0a1b2c3d4e5f6a7b",
  "sandbox_id": "sbx_3f2a9c1e7b6d4a10",
  "port": 3000,
  "public": false,
  "url": "https://api.example.com/v1/sandboxes/sbx_3f2a9c1e7b6d4a10/previews/prv_0a1b2c3d4e5f6a7b/",
  "public_url": "https://3000-sbx-3f2a9c1e7b6d4a10.sbx.example.com/?token=pvt_…",
  "host": "3000-sbx-3f2a9c1e7b6d4a10.sbx.example.com",
  "token": "pvt_…"
}
```

| Field | Meaning |
|---|---|
| `url` | The authenticated proxy under `/v1/…`. Needs an API key that owns the sandbox. |
| `public_url` | The URL to share. `/p/{sandbox_id}/{port}/` on the API host, or `https://{port}-sbx-{16 hex}.{preview_domain}/` when the operator sets a preview domain. For a private preview it includes the token once. |
| `host` | The preview hostname, when `preview_domain` is set. |
| `token` | `pvt_` plus 24 random bytes. Shown only in this response. |

`GET /v1/sandboxes/{id}/previews` lists previews and `DELETE /v1/sandboxes/{id}/previews/{previewId}` removes one. Previews are stored in SQLite (tokens only as hashes), so URLs and tokens keep working across a server restart for sandboxes that were adopted. A preview is deleted with its sandbox.

## Who can open it

A request to a preview is let through when any of these holds:

- The preview is `public` and the operator has not turned off `network.allow_public_previews`.
- It carries the preview token, as `?token=` or the `X-Sbx-Preview-Token` header.
- It carries an API key that owns the sandbox.

Create a public preview with `{"port": 3000, "public": true}`. A token is still returned, so you can keep using it if the operator later disables public previews.

The `token` and `access_token` query parameters are stripped before the request reaches your app.

### The preview cookie

Browsers load a page's scripts, styles, and XHRs without the `?token=` that was on the first URL. So the first request that carries `?token=` gets an HttpOnly, `SameSite=Lax` cookie named `sbx_preview`, scoped to that one preview. For GET and HEAD requests the server also redirects to the same URL without the token, so the token does not stay in the address bar or the browser history. Every later request from that browser is authorized by the cookie.

### Cookies and isolation

How the app's own cookies are handled depends on the URL form:

- **Host previews** (`preview_domain` set): each preview has its own hostname, so the browser keeps cookies apart. sbx forwards the browser's cookies to the app, minus `sbx_preview`.
- **Path previews** (`/p/…`): every sandbox shares the API server's origin, so one sandbox's page could read another's cookies. sbx drops all cookies on path previews.

Use `preview_domain` for apps that need sessions or that you do not trust.

## Preview domains

To serve previews on their own hostnames, point a wildcard DNS record such as `*.sbx.example.com` at your reverse proxy and set:

```yaml
network:
  preview_domain: sbx.example.com
  public_url: https://api.example.com
```

`public_url` is the base used for links in JSON responses. Bind the server on `:8080`, not only `127.0.0.1`, when a proxy in another container publishes it. The proxy has to pass `Host` and `Upgrade`, and talk HTTP/1.1 to sbx so WebSockets work; terminate TLS and HTTP/2 at the proxy. [Deployment](/docs/deployment/#reverse-proxy) has Caddy and Traefik configs.

### `X-Forwarded-Host`

sbx picks the preview from the `Host` header. It ignores `X-Forwarded-Host` unless `network.trust_forwarded_host` (`SBX_TRUST_FORWARDED_HOST`) is `true`. Turn that on only behind a proxy that sets or strips the header, otherwise any client could pick a preview host by sending it. The Caddy and Traefik configs in these docs pass `Host` and need no setting.

## Limits

Each preview allows up to 64 concurrent streams (`network.preview_max_streams`), tightened by the sandbox's `max_connections`. The proxy also stays under the sandbox's `egress_kbps`. WebSocket upgrades are spliced through; other hop-by-hop headers are stripped.

## In the SDK

```ts
await box.startProcess(["python", "-m", "http.server", "3000"], { timeout: 0 })
const preview = await box.createPreview(3000)
console.log(preview.publicUrl, preview.token)

const response = await box.previewFetch(preview.id, "/index.html")
```

`previewFetch` goes through the authenticated `/v1` proxy with your API key.
