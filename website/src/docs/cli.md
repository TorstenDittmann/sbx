# CLI

The `sbx` binary is both the server and a small operator CLI. Every command that reads configuration accepts `--config sbx.yaml` (or `SBX_CONFIG`) and `--data-dir`.

```text
sbx serve [--config sbx.yaml] [--addr 127.0.0.1:8080] [--data-dir ./data] [--backend docker|gvisor|kata|firecracker]
sbx doctor [--config sbx.yaml]
sbx setup gvisor|kata|firecracker [--apply] [--data-dir ./data]
sbx keys create --name NAME [--admin] [--expires 720h] [--data-dir ./data]
sbx keys list [--data-dir ./data]
sbx keys revoke --id ID [--data-dir ./data]
sbx healthcheck [--addr 127.0.0.1:8080]
sbx version
```

## serve

Starts the API server. On the first boot of a data directory it creates an admin key, prints it, and writes it to `data/initial_api_key`. Flags: `--config`, `--addr`, `--data-dir`, `--backend`, `--docker-host`, `--agent-bin`, and `--bootstrap-key`. They override the config file and environment; see [Configuration](/docs/configuration/#flags).

`sbx server` is an alias.

## doctor

```bash
sbx doctor
```

Checks the host and prints one line per check: `ok`, `warn`, or `fail`. It covers the data directory, the Docker daemon, the gVisor runtime, `/dev/kvm`, the Kata runtime, and the Firecracker binary and kernel. A missing optional backend is a warning; it becomes a failure when that backend is the configured default. The command exits non-zero when any check fails.

## setup

```bash
sbx setup gvisor
sbx setup kata
sbx setup firecracker
```

Without `--apply`, prints the steps for the host. With `--apply`, downloads pinned binaries and, for gVisor and Kata, registers a Docker runtime in `daemon.json`. It does not restart Docker.

| Flag | Meaning |
|---|---|
| `--apply` | Download files and write config. |
| `--data-dir` | Where Firecracker files go (`data/firecracker/`). |
| `--prefix` | Directory for downloaded binaries. |
| `--docker-config` | The `daemon.json` to update. |

See [Backends](/docs/backends/) for what each target installs.

## keys

Manages API keys directly in the database, so it works while the server is stopped and does not need an existing key.

```bash
sbx keys create --name ci                       # a sandbox key
sbx keys create --name ops --admin --expires 720h
sbx keys list
sbx keys revoke --id key_...
```

`create` prints the new key once. `--expires` takes a Go duration such as `720h`. `list` prints id, prefix, name, scopes, and state (`active`, `expired`, or `revoked`).

`revoke` can revoke the last admin key, which the API refuses to do. That is deliberate: the host is the recovery path. Create a new admin key with `sbx keys create --admin` afterwards. See [API keys](/docs/api-keys/).

## healthcheck

```bash
sbx healthcheck
```

Calls `/healthz` on the local server (from `--addr` or `SBX_ADDR`) and exits non-zero unless it returns `200`. The container image uses it as its `HEALTHCHECK`.

## version

```bash
sbx version
sbx-agent version
```

Prints the version, git commit, and build time stamped into the binary.
