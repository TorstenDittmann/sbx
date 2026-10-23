# Templates and builds

A template is a named starting point for sandboxes: an image, resource defaults, env, a default backend and network, optional build steps, and an optional pool of warm sandboxes. Creating a sandbox from a template is `{"template": "name"}`.

## Built-in templates

| Name | Image | CPU | Memory | Disk |
|---|---|---|---|---|
| `base` | `alpine:3.20` | 0.5 | 128 MiB | 256 MiB |
| `python` | `python:3.12-slim` | 1 | 512 MiB | 1024 MiB |
| `node` | `node:22-bookworm-slim` | 1 | 512 MiB | 1024 MiB |

Built-in templates cannot be deleted. List every template with `GET /v1/templates`.

## Create a template

Templates are managed with an admin key:

```bash
curl -s $SBX_URL/v1/templates \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "web",
    "image": "node:22-bookworm-slim",
    "description": "Node with ffmpeg and a project skeleton",
    "cpu": 2,
    "memory_mb": 2048,
    "network": "full",
    "backend": "gvisor",
    "steps": [
      {"packages": ["ffmpeg", "git"]},
      {"workdir": "/app"},
      {"run": "npm init -y && npm install express"}
    ],
    "setup": ["sh", "-c", "cp -r /app/. /workspace/"],
    "warm": 2
  }'
```

| Field | Meaning |
|---|---|
| `name` | Lowercase letters, digits, `.`, `_`, and `-`, up to 64 characters. Must be unique. |
| `image` | The base image. Required. |
| `cpu`, `memory_mb`, `disk_mb`, `env` | Defaults for sandboxes created from it. A create can override them. |
| `backend` | Default backend. Empty uses the server default. |
| `network` | Default mode when a create omits `network`: `none` (default) or `full`. |
| `steps` | [Build steps](#build-steps), built when the template is created. A failing step fails the request. |
| `setup` | A command run in `/workspace` before a warm sandbox is offered, and on every cold create. |
| `setup_timeout_seconds` | Timeout for `setup`. Defaults to the exec default (60 seconds). |
| `warm` | How many ready sandboxes to keep, 0 to 8. |

`DELETE /v1/templates/{name}` removes a template and its warm pool.

## Warm pools

With `warm` above 0, sbx starts that many sandboxes ahead of time, runs `setup` in each, and keeps them in status `warm`. A create from the template takes one of them instead of starting a new container, so it skips the image pull, the container start, and the setup command.

A create only takes a warm sandbox when it does not change anything the pool was started with: image, resources, backend, network policy, env, secrets, mounts, or steps. Otherwise sbx starts a new sandbox and runs `setup` then. A pooled sandbox is only handed to a key whose effective [network policy](/docs/network/#inheritance) matches the pool's.

Warm sandboxes are hidden from normal lists. An admin can see them with `GET /v1/sandboxes?status=warm`. They count toward `max_sandboxes`.

## Build steps

Build steps install things into an image once. sbx runs the steps on the base image, tags the result `sbx-template:<hash>` (a hash of the base and the steps), and starts every later sandbox from that image. The next create with the same base and steps finds the cached image and skips the build. The idea follows [Railway's sandbox templates](https://docs.railway.com/sandboxes#templates).

Each step sets exactly one field:

| Step | What it does |
|---|---|
| `{"run": "npm install"}` | Runs a `/bin/sh -c` script as root. Up to 64 KiB. |
| `{"packages": ["ffmpeg", "git"]}` | Installs 1 to 64 system packages with whichever of `apt-get`, `apk`, `dnf`, or `yum` the base has. |
| `{"env": {"NODE_ENV": "production"}}` | Sets variables for later steps. |
| `{"workdir": "/app"}` | Sets the directory later steps run in. |

A template or request has at most 64 steps. Things to know:

- `env` and `workdir` only apply to later build steps. They are not set in the sandbox. Pass runtime variables in the create's `env`.
- Build outside `/workspace`. It is mounted over with a tmpfs (or a volume) at runtime, so anything built there disappears. Build into `/app` and copy or link it in `setup` if you need it in `/workspace`.
- Steps that only set `env` or `workdir` and run no command produce no new image. The build's `id` is then empty and `image` is the base.
- Changing any step, or a step's `env`, changes the hash and rebuilds.

### Steps on a create

`POST /v1/sandboxes` takes `steps` too. They run after the template's own steps:

```bash
curl -s $SBX_URL/v1/sandboxes \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"image":"node:22-bookworm-slim","steps":[{"packages":["ffmpeg"]},{"workdir":"/app"},{"run":"npm init -y"}]}'
```

The first create with a given base and steps waits for the build. Later ones start from the cache. A create with steps never takes a warm sandbox.

### Building ahead of time

`POST /v1/builds` builds and caches an image without creating a sandbox, so the first real create is fast:

```bash
curl -s $SBX_URL/v1/builds \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"image":"python:3.12-slim","steps":[{"run":"pip install numpy pandas"}]}'
```

```json
{
  "id": "4c1f0e…",
  "image": "sbx-template:4c1f0e…",
  "base": "python:3.12-slim",
  "steps": [{ "run": "pip install numpy pandas" }],
  "cached": false,
  "duration_ms": 18234
}
```

It returns `201` after a build and `200` with `cached: true` when the image already exists. Pass `template` to start from a template's image and steps, and `backend` to choose which backend's Docker daemon builds it. Firecracker cannot build images itself; it reads them from the same Docker daemon, so a Docker-based backend builds for it.

### Builds and permissions

Build steps run as root in Docker's builder, with the daemon's default network, not the sandbox network policy. For that reason a build needs an **admin key** unless the operator sets `builds.allow_sandbox_keys: true` (`SBX_BUILDS_ALLOW_SANDBOX_KEYS`). A sandbox key gets `403 forbidden` for:

- `POST /v1/builds`
- a create with `steps`
- a create that sets `image` on a template that has steps, since that is a new build

Creating from a template whose image is already built needs no build permission. A build is stopped after `builds.timeout_seconds` (default 1200).

`sbx_image_builds_total` on `/metrics` counts builds that missed the cache.

### Cleanup

Built images are garbage collected. Once an hour sbx removes `sbx-template` images that no template needs and no live sandbox uses, once nothing has created from or built them for `builds.retain_unused_seconds` (`SBX_BUILD_RETAIN_UNUSED_SECONDS`, default 604800, seven days). A template's own image is kept for as long as the template exists. Set it to `0` to keep every image forever. A removed image is rebuilt the next time something asks for it. `sbx_image_gc_removed_total` on `/metrics` counts removals.

## Snapshots

A snapshot turns a running sandbox into an image, so an environment you prepared by hand (cloned a repo, installed dependencies, warmed a cache) can be forked into new sandboxes.

```bash
curl -s http://127.0.0.1:8080/v1/sandboxes/$ID/snapshot \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"template":"my-app-ready","description":"repo cloned, deps installed"}'
```

```json
{
  "id": "snap_3f9c0a1b2c3d4e5f",
  "image": "sbx-template:snap_3f9c0a1b2c3d4e5f",
  "sandbox_id": "sbx_...",
  "base": "node:22-bookworm-slim",
  "workspace": true,
  "template": { "name": "my-app-ready", "image": "sbx-template:snap_3f9c0a1b2c3d4e5f", "...": "..." },
  "duration_ms": 1840
}
```

The image lands in the same `sbx-template` repository as built images. Create from it with `{"image": "sbx-template:snap_..."}`, or pass `template` on the snapshot to also save a template that copies the sandbox's CPU, memory, disk, env, backend, and network (`none` or `full`; an allow list becomes `none`). The body is optional.

What is kept:

- **`/workspace`**. It is a tmpfs, which a Docker commit skips, so sbx copies it into the image and every sandbox started from the image gets it back. When a volume is mounted at `/workspace`, the volume is the source of truth and nothing is copied (`workspace: false`).
- **Root filesystem changes**, when the root filesystem is writable (`docker.read_only_rootfs: false`).
- The base image's env, entrypoint, user, and labels.

What is not kept: secrets, the sandbox's labels, its network policy, running processes, `/tmp`, and memory. Docker cannot unset an env var on an image, so a key the sandbox added on top of the base image (including every secret) is in the image with an empty value. Env you want in forks belongs in the template, which the snapshot fills from the sandbox's `env`.

The sandbox must be running, not paused; it is paused for the moment of the commit. Snapshots need the same rights as a build (an admin key unless `builds.allow_sandbox_keys` is set), and saving a template needs an admin key. Only the `docker` backend snapshots (`"snapshot": true` in `GET /v1/backends`). gVisor keeps its writes inside the sandbox, where a commit cannot see them. Firecracker can boot a snapshot image, but it does not restore `/workspace` from it.

The copy uses `docker exec` with `/bin/sh`, `tar`, and `cp` from the image, so unlike the rest of sbx, snapshots need an image with a shell and those tools. A snapshot of a sandbox that was itself started from a snapshot replaces the parent's copy, so files deleted from `/workspace` are no longer in the new image's filesystem (its lower layers still hold them, as with any Docker image).

Snapshot images are collected like built images (see [Cleanup](#cleanup)), except that one cannot be rebuilt once removed. A template saved from a snapshot keeps its image for as long as the template exists; a snapshot without a template is removed after `builds.retain_unused_seconds` with no create from it.

## In the SDK

`Sandbox.template()` is a builder for steps. Every method returns a new template, so one base can branch into variants:

```ts
import { Sandbox } from "@sbx.torsten.work/sbx"

const base = Sandbox.template("node:22-bookworm-slim")
  .withPackages("ffmpeg", "git")
  .withEnv({ NODE_ENV: "production" }) // build time only
  .workdir("/app")
  .run("npm init -y")

await base.build() // optional: warm the cache
const box = await Sandbox.create(base, { ttl: "5m" })
await box.exec("ffmpeg -version")
```

`Sandbox.template()` with no argument starts from the `base` template, and `Sandbox.template({ template: "python" })` from another one. Named templates with a warm pool use `client.createTemplate({ name, image, steps, setup, warm })`. See the [SDK reference](/docs/sdk/#templates-and-builds).

`box.snapshot({ template: "my-app-ready" })` takes a snapshot of a sandbox, and `client.snapshot(id, options)` does the same by id.
