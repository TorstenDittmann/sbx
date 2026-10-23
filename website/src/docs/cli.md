# CLI

The `sbx` binary is the server, a small operator CLI, and a client for a running server. The operator commands read configuration and accept `--config sbx.yaml` (or `SBX_CONFIG`) and `--data-dir`. The [sandbox commands](#sandbox-commands) talk to the API instead.

```text
sbx serve [--config sbx.yaml] [--addr 127.0.0.1:8080] [--data-dir ./data] [--backend docker|gvisor|kata|firecracker]
sbx doctor [--config sbx.yaml]
sbx setup gvisor|kata|firecracker [--apply] [--data-dir ./data]
sbx keys create --name NAME [--admin] [--expires 720h] [--data-dir ./data]
sbx keys list [--data-dir ./data]
sbx keys revoke --id ID [--data-dir ./data]
sbx healthcheck [--addr 127.0.0.1:8080]
sbx version

sbx create [TEMPLATE] [--image IMAGE] [--ttl 5m] [--network none|full] [--env K=V] [--label K=V] [--json]
sbx ls [--status S] [--template T] [--label key:value] [-q TEXT] [--quiet] [--json]
sbx get ID
sbx exec [--cwd DIR] [--env K=V] [--timeout 30s] [-i] ID [--] CMD [ARGS...]
sbx shell [--cwd DIR] ID [-- CMD [ARGS...]]
sbx cp SRC DST
sbx pause ID
sbx resume ID
sbx ttl ID 10m
sbx rm ID [ID...]
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

## Sandbox commands

These commands call the HTTP API, so the server must be running. They read the server from `SBX_URL` (default `http://127.0.0.1:8080`) and the key from `SBX_API_KEY`, or from `--url` and `--key`. Without a key they read `initial_api_key` from `SBX_DATA_DIR` (default `./data`), so on the server host the first-boot admin key works with no setup.

```bash
id=$(sbx create node --ttl 30m --network full --label team=web)
sbx cp ./app $id:app
sbx exec --cwd app $id -- npm install
sbx shell $id
sbx rm $id
```

`create` prints the new id, or the whole sandbox with `--json`. The positional argument is the template, the same as `--template`. Other flags are `--image`, `--backend`, `--cpu`, `--memory` and `--disk` (MiB), `--idle-timeout`, and the repeatable `--env`, `--label`, `--secret` (all `KEY=VALUE`), and `--volume VOLUME_ID:/path`. Durations are seconds or values such as `5m`.

`ls` prints a table of id, status, template (or image), backend, network, age, expiry, and labels. It takes the same filters as `GET /v1/sandboxes`; `--quiet` prints only ids, so `sbx rm $(sbx ls --quiet --label team:web)` removes a group. `get` prints one sandbox as JSON.

`exec` streams stdout and stderr as the command runs and exits with the command's exit code. Flags go before the id; everything after it is the command, so its own flags are left alone. `-i` sends stdin to the command. A relative `--cwd` is under `/workspace`.

`shell` opens an interactive terminal: bash when the image has it, otherwise sh. It puts the local terminal in raw mode and follows window resizes. Pass a command after `--` to run something else. Terminals need [sbx-agent](/docs/agent/).

`cp` copies files and directories in either direction. One side is `ID:PATH`; a relative path is under `/workspace`. When the destination is an existing directory, the source is copied into it under its own name, like `cp -r`. A directory is sent as a tar archive, so the destination's parent must exist. `-` reads a file from stdin or writes one to stdout.

`pause` and `resume` print the new status. `ttl` sets the expiry to a duration from now. `rm` deletes one or more sandboxes and prints each id it removed.
