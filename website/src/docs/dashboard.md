# Dashboard

`sbx serve` ships a small web UI at `/ui/`. Open `http://127.0.0.1:8080/ui/`, or just `http://127.0.0.1:8080/` in a browser, which redirects there.

The dashboard is a few static files embedded in the binary. It has no server-side session and no build step. You sign in with an API key, and every button is a normal `/v1` call made with that key, so the dashboard can do exactly what the key can do and nothing more.

## Signing in

Paste an API key. By default the browser keeps it for the tab (`sessionStorage`) and forgets it when the tab closes. Tick **Remember on this device** to keep it in `localStorage` instead. **Sign out** removes it.

An admin key sees every sandbox and the **API keys** page. A sandbox key sees only its own sandboxes, and the keys page is hidden.

## Sandboxes

The list refreshes every few seconds. Search matches id, template, image, status, and labels, the same as `GET /v1/sandboxes?q=`. **New sandbox** creates one from a template or an image, with a TTL and a network mode.

Open a sandbox for three tabs:

- **Overview** shows resources, network, expiry, labels, env, secret names, and mounts. Pause, resume, set a new TTL, or delete from the header.
- **Terminal** opens an interactive shell (`bash` when the image has it, otherwise `sh`) over the terminal WebSocket. Leaving the tab kills the shell. **New session** starts another.
- **Logs** lists background processes and streams the stdout and stderr of the one you pick. **Run in background** starts a command with `sh -c` and follows it.

Terminals and logs need [sbx-agent](/docs/agent).

## API keys

With an admin key, create a key with a name, a scope, and an optional expiry. The new key is shown once. The list shows each key's prefix, scopes, last use, expiry, and whether it is active, expired, or revoked. **Revoke** takes effect at once. The server still refuses to revoke the last admin key.

## Security notes

- Pages are served with a strict Content Security Policy: scripts only from the server itself, no framing, and network calls only to the same origin.
- API calls send the key in the `Authorization` header. The terminal WebSocket cannot set headers, so it passes the key as `access_token`. Request logs record only the path, never the query.
- The dashboard adds no new API routes and no new permissions.

## Turning it off

```yaml
dashboard: false
```

or `SBX_DASHBOARD=false`. `/ui/` then returns `404` and `/` always answers with JSON.
