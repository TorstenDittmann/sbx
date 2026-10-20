# Installation

Every release is a git tag `vX.Y.Z`. A tag publishes three things: release archives on GitHub, a multi-arch image at `ghcr.io/torstendittmann/sbx`, and the npm package `@sbx.torsten.work/sbx`.

## Install script

```bash
curl -fsSL https://raw.githubusercontent.com/TorstenDittmann/sbx/main/install.sh | sh
sbx version
```

The script picks the archive for this machine from the latest GitHub release and checks it against `checksums.txt`. It installs to `/usr/local/bin` when that directory is writable, and to `~/.local/bin` otherwise.

| Host | `sbx` archive | `sbx-agent` archive |
|---|---|---|
| Linux amd64 | `sbx_<version>_linux_amd64.tar.gz` | `sbx-agent_<version>_linux_amd64.tar.gz` |
| Linux arm64 | `sbx_<version>_linux_arm64.tar.gz` | `sbx-agent_<version>_linux_arm64.tar.gz` |
| macOS Intel | `sbx_<version>_darwin_amd64.tar.gz` | `sbx-agent_<version>_linux_amd64.tar.gz` |
| macOS Apple silicon | `sbx_<version>_darwin_arm64.tar.gz` | `sbx-agent_<version>_linux_arm64.tar.gz` |

On macOS the agent is still a Linux binary, because Docker Desktop runs Linux containers and the agent runs inside them. The script puts both binaries in the same directory, which is where the server looks for `sbx-agent`.

Options:

```bash
SBX_VERSION=v0.2.0 sh install.sh            # pin a release (same as --version v0.2.0)
SBX_INSTALL_DIR=$HOME/.local/bin sh install.sh
SBX_SKIP_AGENT=1 sh install.sh              # sbx only
```

`SBX_OS` and `SBX_ARCH` override detection (`linux` or `darwin`, `amd64` or `arm64`). You can also download the archives and `checksums.txt` from the [GitHub release](https://github.com/TorstenDittmann/sbx/releases) yourself.

`sbx version` and `sbx-agent version` print the version, git commit, and build time.

## Container image

```bash
docker run --rm -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v sbx-data:/data \
  ghcr.io/torstendittmann/sbx:latest
```

The image is built for `linux/amd64` and `linux/arm64`. It listens on `:8080` (`SBX_ADDR`) and keeps its data in `/data`. `sbx-agent` is at `/usr/local/bin/sbx-agent`, and the entrypoint exports `SBX_AGENT_BIN` (the older name for `SBX_DOCKER_AGENT_BIN`) so the server finds it.

| Tag | When |
|---|---|
| `0.2.0` | The version, without the `v`. |
| `v0.2.0` | The git tag. |
| `latest` | The newest final release, never a prerelease. |

The Docker socket mount is how sbx creates sandboxes. Sandboxes never see it. See [Deployment](/docs/deployment/) for compose files and a reverse proxy.

## From source

```bash
git clone https://github.com/TorstenDittmann/sbx
cd sbx
go build -o sbx ./cmd/sbx
go build -o sbx-agent ./cmd/sbx-agent
```

`docker compose up --build` in the repository builds the root `Dockerfile` from source. That image is for local development; the published image is the one above.

## TypeScript SDK

```bash
npm install @sbx.torsten.work/sbx
```

```bash
bun add @sbx.torsten.work/sbx
```

The package is ESM, ships its TypeScript declarations, and runs on Node 20+ and Bun. The unscoped npm name `sbx` belongs to a different project. Prereleases are published under the `next` dist-tag (`npm install @sbx.torsten.work/sbx@next`).

Releases are published from GitHub Actions with npm trusted publishing (OIDC), so there is no long-lived npm token. Each version published from CI carries a provenance attestation that links it to the workflow run and commit that built it. You can check it with `npm audit signatures`. See the [SDK reference](/docs/sdk/).

## Check the host

```bash
sbx doctor
```

`doctor` reports whether Docker answers, whether the gVisor (`runsc`) and Kata runtimes are registered, whether `/dev/kvm` is writable, and whether the Firecracker binary and kernel are installed. See [Backends](/docs/backends/) for setting each one up.
