# Sandbox network

sbx sandboxes start with no network. An operator who wants outbound internet, for example an AI coding agent, sets the server default to `full`. Each API key and each sandbox can still narrow that, and a key with `max_mode` sets a ceiling its sandboxes cannot raise.

## Modes

| Mode | Meaning |
|---|---|
| `none` | No interface except loopback. This is the default, so existing callers stay offline. |
| `full` | Public egress on the `sbx` bridge (`sbx0`, `172.30.0.0/16`). |
| `allow` | Same bridge as `full`, and a required allow list. Everything else is rejected. |

Inheritance, most specific first:

- Mode: sandbox, else the API key, else `network.default_mode`.
- Ceiling: a key's `mode` and `allow` are only defaults. To make them binding, create the key with `network.max_mode` (`none`, `allow`, or `full`). A sandbox that asks for a looser mode gets 403, a looser server default is narrowed to `max_mode`, and with `max_mode: allow` every sandbox allow entry must be one of the key's hostnames or fall inside one of its CIDRs. A warm pooled sandbox is only handed to a key whose effective network matches the pool's exactly.
- Allow list: sandbox if it sent `allow` (an empty array clears it), else the key, else `network.egress_allow`.
- Deny list: the union of server, key, and sandbox. A sandbox cannot remove a deny.
- `egress_kbps` and `max_connections`: the tightest positive value. `0` means inherit, and all zeros means unlimited.

`allow` with an empty allow list is rejected. Wildcard names such as `*.example.com` are rejected. The packet filter only sees addresses, so list each hostname. Names are resolved when the sandbox is created and refreshed by the reaper.

## What stays blocked

With the defaults, traffic from the bridge is rejected to:

- `169.254.0.0/16` and Alibaba metadata `100.100.100.200/32`
- RFC1918 (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`)
- IPv6 link-local `fe80::/10`, unique-local `fc00::/7`, and `::1`

`network.private_allow` is a list of CIDRs that skip the private-range reject. It cannot open metadata. The only way to reach metadata is `network.block_metadata: false`. `network.block_private: false` turns the private-range reject off. Both blocks default to on. Per-sandbox rules run before this baseline, then return into it, so a sandbox allow list cannot punch through the blocks. The reaper checks the baseline every 30 seconds and reinstalls it when a firewalld reload, a `dockerd` restart, or a flush removed it, and logs a warning when it does.

If the firewall cannot be installed, `full` and `allow` fail and the container is removed.

## DNS, IPv4, and IPv6

`network.dns` is written into the container resolvers. The default is `1.1.1.1` and `8.8.8.8`, with `timeout:2`, `attempts:3`, and `rotate`. IPv6 resolvers are omitted from the default because they stall when the host has no IPv6 route.

`network.ipv6` is `auto` (follow the daemon), `on`, or `off`. When IPv6 is on, a new `sbx` bridge also gets `fd16:5b00::/64`. An existing bridge is left as it is. Outbound IPv6 still needs a route on the host. gVisor (`runsc`) uses this same Docker network namespace when it is installed with `runsc install -- --network=host`. That flag selects the Linux stack in the namespace Docker created. The default gVisor netstack does not, and DNS from the sandbox fails. The bridge and the filter apply either way.

## Package managers and root

The default sandbox is uid 1000 with a read-only rootfs. `npm --prefix`, `pip --target`, and `GOPATH`/`GOCACHE` on `/workspace` work when the image already contains the tool. `apk` and `apt` need to write the rootfs. Set both of these:

```yaml
docker:
  read_only_rootfs: false
  sandbox_root: true
```

`sandbox_root` runs the container as uid 0 and adds `CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `SETGID`, and `SETUID` so `apt` can drop to its sandbox user. Every other capability stays dropped, and `no-new-privileges` stays on. It stays off unless the operator turns it on.

## Limits

`egress_kbps` installs a token-bucket qdisc on the host veth (`tc`). `max_connections` is an iptables `connlimit`. The host needs `tc` and `nsenter` for the bandwidth qdisc. A failure is fail-closed. Preview streams are also capped (`network.preview_max_streams`, default 64, tightened by the sandbox connection limit) and the preview proxy sleeps to stay under the same byte rate.

## Previews from the internet

Creating a preview returns:

- `url`, the API-key proxy under `/v1/sandboxes/{id}/previews/{previewId}/`
- `public_url`, either `/p/{sandboxId}/{port}/` or `https://<port>-sbx-<16 hex>.<preview_domain>/` when a domain is set
- `token`, shown once (`pvt_` plus 24 random bytes)
- `host`, when `preview_domain` is set

Previews are stored in SQLite (the token only as a hash), so URLs and tokens keep working across a server restart for sandboxes that were adopted. A preview is removed with its sandbox.

A preview is reachable when it is `public` (and `allow_public_previews` is still true), or the caller sends the token (`?token=` or `X-Sbx-Preview-Token`), or the caller sends an API key that owns the sandbox. The token and `access_token` query parameters are stripped before the request is forwarded. Hop headers are stripped except on a WebSocket upgrade, which is spliced.

The first request that carries `?token=` gets an HttpOnly `sbx_preview` cookie scoped to that preview and, for GET and HEAD, a redirect to the same URL without the token, so the page's scripts, styles, and XHRs are authorized too. Host previews (`preview_domain`) forward the browser's own cookies to the app, minus `sbx_preview`. Path previews (`/p/...`) drop all cookies, because every sandbox shares the server's origin there and one sandbox could read another's cookies. Use `preview_domain` for apps that need sessions or that you do not trust.

`X-Forwarded-Host` is ignored when picking a preview host unless `network.trust_forwarded_host` (`SBX_TRUST_FORWARDED_HOST`) is true. Turn it on only behind a proxy that sets that header. The proxies in this document pass `Host`, which needs no setting.

The server does not speak HTTP/2 to the sandbox. Terminate TLS and HTTP/2 at the proxy and use HTTP/1.1 upstream so `Upgrade: websocket` still works. The proxy must pass `Host` and `Upgrade`.

```yaml
network:
  default_mode: full
  preview_domain: sbx.example.com
  public_url: https://api.example.com
  allow_public_previews: true
```

`public_url` is the base used in JSON links. Bind the server on `:8080` (not only `127.0.0.1`) when a proxy publishes it.

### Caddy

```caddyfile
api.example.com, *.sbx.example.com {
	reverse_proxy sbx:8080
}
```

Caddy forwards `Host`, upgrades WebSockets, and terminates HTTP/2. Point the wildcard DNS record at this proxy.

### Traefik and Dokploy

```yaml
http:
  routers:
    sbx:
      rule: Host(`api.example.com`) || HostRegexp(`^[0-9]+-sbx-[0-9a-f]+\.sbx\.example\.com$`)
      entryPoints: [websecure]
      tls: { certResolver: le }
      service: sbx
  services:
    sbx:
      loadBalancer:
        passHostHeader: true
        servers:
          - url: http://sbx:8080
```

Dokploy puts Traefik in front of the service. Add the API domain and a wildcard domain for `*.sbx.example.com`, both targeting container port 8080. Set `SBX_ADDR=:8080`, `SBX_NETWORK_MODE=full`, `SBX_PREVIEW_DOMAIN=sbx.example.com`, and `SBX_PUBLIC_URL=https://api.example.com`. The same compose file already mounts the Docker socket, which the firewall helper needs when the server is not root.

Copies of these snippets live in `deploy/proxy/`.

## Environment

`SBX_NETWORK_MODE`, `SBX_BLOCK_PRIVATE`, `SBX_BLOCK_METADATA`, `SBX_PRIVATE_ALLOW`, `SBX_DNS`, `SBX_IPV6`, `SBX_EGRESS_ALLOW`, `SBX_EGRESS_DENY`, `SBX_DEFAULT_EGRESS_KBPS`, `SBX_DEFAULT_MAX_CONNECTIONS`, `SBX_PREVIEW_DOMAIN`, `SBX_PUBLIC_URL`, `SBX_ALLOW_PUBLIC_PREVIEWS`, `SBX_PREVIEW_MAX_STREAMS`, `SBX_SANDBOX_ROOT`. Lists are comma-separated.
