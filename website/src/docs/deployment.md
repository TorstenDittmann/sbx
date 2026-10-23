# Deployment

sbx is one process with a SQLite file. To run it on a server you need Docker, a place for the data directory, and, if anything outside the machine talks to it, a reverse proxy that terminates TLS.

## Docker Compose

```yaml
services:
  sbx:
    image: ghcr.io/torstendittmann/sbx:latest
    restart: unless-stopped
    ports:
      - "127.0.0.1:8080:8080"
    environment:
      SBX_ADDR: ":8080"
      SBX_DATA_DIR: /data
      # SBX_NETWORK_MODE: full
      # SBX_PREVIEW_DOMAIN: sbx.example.com
      # SBX_PUBLIC_URL: https://api.example.com
    volumes:
      - ./data:/data
      - /var/run/docker.sock:/var/run/docker.sock
```

The Docker socket is how sbx creates sandboxes. With `network: full` it also uses it to install host firewall rules when the server itself cannot run iptables. Sandboxes never see the socket.

The image has a `HEALTHCHECK` that runs `sbx healthcheck`. The first boot prints the admin key to the container log and writes it to `./data/initial_api_key`.

The repository's `docker-compose.yml` and `deploy/compose/docker-compose.yml` build from source instead of pulling the image.

## systemd

Install the binaries with the [install script](/docs/installation/#install-script), then use the unit from `deploy/systemd/sbx.service`:

```text
[Unit]
Description=sbx sandbox server
After=docker.service
Requires=docker.service

[Service]
ExecStart=/usr/local/bin/sbx serve --config /etc/sbx/sbx.yaml
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
```

```bash
sudo install -D -m 0644 sbx.yaml.example /etc/sbx/sbx.yaml
sudo systemctl enable --now sbx
journalctl -u sbx -f
```

Set `data_dir` in the config to an absolute path such as `/var/lib/sbx`, because the unit does not set a working directory.

## Reverse proxy

Put a proxy in front when clients or browsers reach sbx over the network. The proxy should:

- terminate TLS and HTTP/2, and talk HTTP/1.1 to sbx so WebSocket upgrades work
- pass the `Host` and `Upgrade` headers
- route the API host and, if you use [preview domains](/docs/previews/#preview-domains), the wildcard preview host to sbx

Bind sbx on `:8080` (not just `127.0.0.1`) when the proxy runs in another container, and set `public_url` so JSON responses contain the public address:

```yaml
addr: :8080
network:
  preview_domain: sbx.example.com
  public_url: https://api.example.com
```

### Caddy

```text
api.example.com, *.sbx.example.com {
	reverse_proxy sbx:8080
}
```

Caddy forwards `Host`, upgrades WebSockets, and gets certificates on its own. A wildcard certificate needs Caddy's DNS challenge for your DNS provider. Point the wildcard DNS record at the proxy.

### Traefik

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

### Dokploy

Dokploy puts Traefik in front of the service. Add the API domain and a wildcard domain for `*.sbx.example.com`, both targeting container port 8080. Set `SBX_ADDR=:8080`, `SBX_NETWORK_MODE=full` if sandboxes need the internet, `SBX_PREVIEW_DOMAIN=sbx.example.com`, and `SBX_PUBLIC_URL=https://api.example.com`. Mount the Docker socket as in the compose file above.

Copies of these configs are in [`deploy/proxy/`](https://github.com/TorstenDittmann/sbx/tree/main/deploy/proxy).

These proxies pass `Host` as is, so leave `network.trust_forwarded_host` off. Turn it on only if your proxy rewrites `Host` and puts the original in `X-Forwarded-Host`, and make sure the proxy overwrites any `X-Forwarded-Host` the client sent.

## Health and monitoring

| Route | Use |
|---|---|
| `GET /healthz` | Liveness. `200` while the configured backend answers, `503` otherwise. |
| `GET /readyz` | Readiness. `503` until startup reconcile has adopted or retired sandboxes from the last run, and while the backend does not answer. |
| `GET /metrics` | Prometheus text format. No authentication. |
| `GET /` | Name and version. |

Metrics include `sbx_info`, `sbx_sandboxes` (by status), `sbx_sandbox_creates_total`, `sbx_sandbox_deletes_total`, `sbx_sandbox_reaped_total`, `sbx_execs_total`, `sbx_exec_errors_total`, `sbx_file_ops_total`, `sbx_image_builds_total`, and `sbx_image_gc_removed_total`.

Each running or paused sandbox also gets series labeled `sandbox` and `backend`: `sbx_sandbox_cpu_seconds_total`, `sbx_sandbox_cpu_limit_cores`, `sbx_sandbox_memory_bytes`, `sbx_sandbox_memory_limit_bytes`, `sbx_sandbox_network_receive_bytes_total`, `sbx_sandbox_network_transmit_bytes_total`, and `sbx_sandbox_pids`. A scrape samples up to 8 sandboxes at a time with a 2 second limit each, and leaves out any sandbox that fails. Disk is not scraped because it needs an exec per sandbox. Use `GET /v1/sandboxes/{id}/stats` for it. Firecracker sandboxes have no per-sandbox series yet. For CPU use as a share of the limit, graph `rate(sbx_sandbox_cpu_seconds_total[1m]) / sbx_sandbox_cpu_limit_cores`.

`/metrics` is unauthenticated. If the API is public, block it at the proxy or scrape it on a private address.

## Backups and upgrades

All state lives in the data directory: `sbx.db` (keys, sandboxes, templates, volumes, previews) and, for Firecracker, `firecracker/`. Back up `sbx.db` while the server is stopped, or with `sqlite3 sbx.db ".backup backup.db"` while it runs.

To upgrade, replace the binaries or pull a new image and restart. On startup sbx adopts sandboxes that are still running, so a restart does not kill them. Database migrations run automatically.

## Hardening checklist

- Keep `network.default_mode: none` unless sandboxes need the internet, and prefer `allow` lists over `full`.
- Keep `block_private` and `block_metadata` on.
- Give applications `sandbox` keys with an `expires_at`, and a key `network.max_mode` where you can.
- Leave `builds.allow_sandbox_keys` off unless you trust every sandbox key holder to run root builds with network.
- Use `preview_domain` instead of path previews for apps you do not control.
- Use `gvisor`, `kata`, or `firecracker` for code you do not trust at all.
- Run `sbx doctor` after changing the host.
