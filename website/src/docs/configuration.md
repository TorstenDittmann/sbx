# Configuration

sbx works with no configuration. Every setting has a default, and you change only what you need.

Settings come from four places. Later ones win:

1. Built-in defaults
2. A YAML file, passed with `--config sbx.yaml` or `SBX_CONFIG`
3. Environment variables
4. Command-line flags on `sbx serve`

Start from [`sbx.yaml.example`](https://github.com/TorstenDittmann/sbx/blob/main/sbx.yaml.example) in the repository. Omitted keys keep their defaults. The server validates the result at startup and refuses to start on a bad value.

## Example

```yaml
addr: :8080
data_dir: /var/lib/sbx
backend: docker

limits:
  default_ttl_seconds: 600
  max_sandboxes: 50

network:
  default_mode: full
  egress_deny:
    - example.net
  preview_domain: sbx.example.com
  public_url: https://api.example.com

builds:
  allow_sandbox_keys: false
```

## Server

| Key | Env | Default | Meaning |
|---|---|---|---|
| `addr` | `SBX_ADDR` | `127.0.0.1:8080` | Listen address. Use `:8080` behind a proxy in another container. |
| `data_dir` | `SBX_DATA_DIR` | `data` | Holds `sbx.db`, `initial_api_key`, and Firecracker files. |
| `backend` | `SBX_BACKEND` | `docker` | Default backend: `docker`, `gvisor`, `kata`, or `firecracker`. |
| `log_level` | `SBX_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. |
| `auth.bootstrap_key` | `SBX_BOOTSTRAP_KEY` | empty | Admin key to create on first boot instead of generating one. |

## Docker

| Key | Env | Default | Meaning |
|---|---|---|---|
| `docker.host` | `SBX_DOCKER_HOST` | `/var/run/docker.sock` | Docker engine socket. |
| `docker.gvisor_runtime` | `SBX_GVISOR_RUNTIME` | `runsc` | Docker runtime name used for `backend: gvisor`. |
| `docker.read_only_rootfs` | `SBX_READ_ONLY_ROOTFS` | `true` | Mount the container root filesystem read-only. |
| `docker.sandbox_uid` | `SBX_SANDBOX_UID` | `1000` | User the sandbox runs as. |
| `docker.sandbox_gid` | `SBX_SANDBOX_GID` | `1000` | Group the sandbox runs as. |
| `docker.sandbox_root` | `SBX_SANDBOX_ROOT` | `false` | Run as uid 0 with the few capabilities `apt` needs. See [Network](/docs/network/#installing-packages-at-runtime). |
| `docker.agent_bin` | `SBX_AGENT_BIN` | empty | Path to `sbx-agent`. Empty looks next to the `sbx` binary. |

## Kata

| Key | Env | Default | Meaning |
|---|---|---|---|
| `kata.runtime` | `SBX_KATA_RUNTIME` | `kata` | Runtime name in Docker's `daemon.json`. |
| `kata.runtime_type` | `SBX_KATA_RUNTIME_TYPE` | `io.containerd.kata.v2` | Expected containerd shim. |

## Firecracker

| Key | Env | Default | Meaning |
|---|---|---|---|
| `firecracker.bin` | `SBX_FIRECRACKER_BIN` | `data/firecracker/firecracker`, then `PATH` | Firecracker binary. |
| `firecracker.kernel` | `SBX_FIRECRACKER_KERNEL` | `data/firecracker/vmlinux` | Guest kernel. |
| `firecracker.jailer` | `SBX_FIRECRACKER_JAILER` | empty (off) | Path to the jailer. Set it to turn the jailer on. |
| `firecracker.uid` | `SBX_FIRECRACKER_UID` | `123` | Jailer uid. Must be at least 1 with a jailer. |
| `firecracker.gid` | `SBX_FIRECRACKER_GID` | `123` | Jailer gid. Must be at least 1 with a jailer. |

## Limits

| Key | Env | Default | Meaning |
|---|---|---|---|
| `limits.default_ttl_seconds` | `SBX_DEFAULT_TTL_SECONDS` | `300` | TTL when a create omits it. |
| `limits.max_ttl_seconds` | `SBX_MAX_TTL_SECONDS` | `86400` | Longest TTL or idle timeout allowed. |
| `limits.default_cpu` | | `1` | CPU when neither the request nor the template sets it. |
| `limits.max_cpu` | | `4` | |
| `limits.default_memory_mb` | `SBX_DEFAULT_MEMORY_MB` | `512` | |
| `limits.max_memory_mb` | | `8192` | |
| `limits.default_disk_mb` | | `1024` | Size of `/workspace`. |
| `limits.max_disk_mb` | | `10240` | |
| `limits.default_pids` | | `256` | |
| `limits.max_pids` | | `4096` | |
| `limits.max_sandboxes_per_key` | `SBX_MAX_SANDBOXES_PER_KEY` | `20` | Live sandboxes per API key. |
| `limits.max_sandboxes` | `SBX_MAX_SANDBOXES` | `100` | Live sandboxes on the server, warm pools included. `0` means no cap. |
| `limits.requests_per_minute` | `SBX_REQUESTS_PER_MINUTE` | `1200` | Per-key request budget. `0` turns the limiter off. |
| `limits.default_exec_seconds` | | `60` | Exec timeout when the request omits it. |
| `limits.max_exec_seconds` | | `3600` | |
| `limits.max_output_bytes` | | `8388608` | Exec output kept before `truncated` is set (8 MiB). |
| `limits.max_upload_bytes` | `SBX_MAX_UPLOAD_BYTES` | `33554432` | Largest request body, such as a file upload or process stdin (32 MiB). |
| `limits.max_archive_bytes` | `SBX_MAX_ARCHIVE_BYTES` | `268435456` | Largest archive upload, checked on the request body and again after gunzip (256 MiB). With `sbx-agent` the archive streams into the sandbox; without it the server holds the whole tar in memory. |
| `limits.reaper_interval_seconds` | | `5` | How often the reaper runs. |
| `limits.retain_stopped_seconds` | | `86400` | How long stopped sandbox rows are kept. |
| `limits.creating_timeout_seconds` | | `900` | A sandbox stuck in `creating` longer than this is reaped. |

## Network

See [Network](/docs/network/) for how these combine with API key and sandbox policies, and [Previews](/docs/previews/) for the preview settings.

| Key | Env | Default | Meaning |
|---|---|---|---|
| `network.default_mode` | `SBX_NETWORK_MODE` | `none` | `none`, `full`, or `allow`. |
| `network.block_private` | `SBX_BLOCK_PRIVATE` | `true` | Reject RFC 1918 and IPv6 private ranges. |
| `network.block_metadata` | `SBX_BLOCK_METADATA` | `true` | Reject cloud metadata addresses. |
| `network.private_allow` | `SBX_PRIVATE_ALLOW` | empty | CIDRs that skip the private-range reject. |
| `network.dns` | `SBX_DNS` | `1.1.1.1,8.8.8.8` | Resolvers written into sandboxes. |
| `network.ipv6` | `SBX_IPV6` | `auto` | `auto`, `on`, or `off`. |
| `network.egress_allow` | `SBX_EGRESS_ALLOW` | empty | Server allow list for `allow` mode. |
| `network.egress_deny` | `SBX_EGRESS_DENY` | empty | Server deny list, always applied. |
| `network.default_egress_kbps` | `SBX_DEFAULT_EGRESS_KBPS` | `0` | Bandwidth cap. `0` is unlimited. |
| `network.default_max_connections` | `SBX_DEFAULT_MAX_CONNECTIONS` | `0` | Connection cap. `0` is unlimited. |
| `network.preview_domain` | `SBX_PREVIEW_DOMAIN` | empty | Serve previews on `{port}-sbx-{id}.{domain}`. |
| `network.public_url` | `SBX_PUBLIC_URL` | empty | Public base URL used in JSON links. |
| `network.allow_public_previews` | `SBX_ALLOW_PUBLIC_PREVIEWS` | `true` | Allow previews that need no credential. |
| `network.trust_forwarded_host` | `SBX_TRUST_FORWARDED_HOST` | `false` | Route preview hosts by `X-Forwarded-Host`. Only behind a proxy that sets it. |
| `network.preview_max_streams` | `SBX_PREVIEW_MAX_STREAMS` | `64` | Concurrent streams per preview. |

List values in environment variables are comma-separated.

## Builds

| Key | Env | Default | Meaning |
|---|---|---|---|
| `builds.allow_sandbox_keys` | `SBX_BUILDS_ALLOW_SANDBOX_KEYS` | `false` | Let sandbox keys run [build steps](/docs/templates/#builds-and-permissions). |
| `builds.timeout_seconds` | `SBX_BUILD_TIMEOUT_SECONDS` | `1200` | Longest a single image build may take. |

## Registries

`registries` lists credentials for private image registries. sbx sends them when it pulls a sandbox image or a template base, for every key. A key can carry [its own credentials](/docs/api-keys/#private-registries), which take precedence for the same server.

```yaml
registries:
  - server: ghcr.io
    username: deploy-bot
    password_env: GHCR_TOKEN
  - server: docker.io
    username: acme
    password_file: /run/secrets/dockerhub
  - server: registry.example.com:5000
    identity_token: ...
```

| Field | Meaning |
|---|---|
| `server` | Registry host. `https://` and a path are dropped, and `index.docker.io` is `docker.io`, the registry for unqualified names such as `acme/app`. |
| `username`, `password` | Basic credentials. |
| `password_env` | Read the password from this environment variable. Startup fails when it is unset. |
| `password_file` | Read the password from this file, without its trailing newline. |
| `identity_token` | An OAuth refresh token, in place of username and password. |

There is no environment variable form; the list only comes from the file.

## Flags

`sbx serve` accepts these flags, which override the file and the environment:

| Flag | Setting |
|---|---|
| `--config` | Config file path (also `SBX_CONFIG`). |
| `--addr` | `addr` |
| `--data-dir` | `data_dir` |
| `--backend` | `backend` |
| `--docker-host` | `docker.host` |
| `--agent-bin` | `docker.agent_bin` |
| `--bootstrap-key` | `auth.bootstrap_key` |
