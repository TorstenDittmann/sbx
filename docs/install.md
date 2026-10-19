# Install

Releases are git tags of the form `vX.Y.Z`. Each tag publishes GitHub release archives, a multi-arch image at `ghcr.io/torstendittmann/sbx`, and the npm package `@torstendittmann/sbx`.

## Binary

```bash
curl -fsSL https://raw.githubusercontent.com/TorstenDittmann/sbx/main/install.sh | sh
sbx version
```

`install.sh` downloads the archive for this machine from the latest GitHub release and checks it against `checksums.txt`.

| Host | `sbx` archive | `sbx-agent` archive |
|---|---|---|
| Linux amd64 | `sbx_<version>_linux_amd64.tar.gz` | `sbx-agent_<version>_linux_amd64.tar.gz` |
| Linux arm64 | `sbx_<version>_linux_arm64.tar.gz` | `sbx-agent_<version>_linux_arm64.tar.gz` |
| macOS Intel | `sbx_<version>_darwin_amd64.tar.gz` | `sbx-agent_<version>_linux_amd64.tar.gz` |
| macOS Apple silicon | `sbx_<version>_darwin_arm64.tar.gz` | `sbx-agent_<version>_linux_arm64.tar.gz` |

`sbx` is the server and CLI. `sbx-agent` is the static binary the server copies into each sandbox. On macOS the agent is still a Linux binary, because Docker Desktop runs Linux containers. The installer places the two binaries in the same directory. The server looks for `sbx-agent` next to itself.

Linux binaries are static. The script installs to `/usr/local/bin` when that directory is writable, and to `~/.local/bin` otherwise.

```bash
SBX_VERSION=v0.2.0 sh install.sh
SBX_INSTALL_DIR=$HOME/.local/bin sh install.sh
SBX_SKIP_AGENT=1 sh install.sh   # sbx only
```

`--version v0.2.0` is the same as `SBX_VERSION`. `SBX_OS` and `SBX_ARCH` override detection (`linux` or `darwin`, `amd64` or `arm64`).

You can also download the archives and `checksums.txt` from the GitHub release and unpack them yourself. `sbx version` prints the version, the git commit, and the build time stamped into the binary.

## Docker

```bash
docker run --rm -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v sbx-data:/data \
  ghcr.io/torstendittmann/sbx:latest
```

The image is `linux/amd64` and `linux/arm64`. It listens on `:8080` (`SBX_ADDR`) and stores its data in `/data`. `sbx-agent` is at `/usr/local/bin/sbx-agent`, and the entrypoint exports `SBX_AGENT_BIN` when that file is present. The first boot prints an admin key.

Tags:

| Tag | When |
|---|---|
| `0.2.0` | version, without the `v` |
| `v0.2.0` | the git tag |
| `latest` | a final release, not a prerelease |

`docker compose up --build` in this repo still compiles the root `Dockerfile` from source. That image is for local development. The published image is the one above.

## npm and Bun

The unscoped npm name `sbx` is already published by another project (`bhoriuchi/sbx`). This SDK is `@torstendittmann/sbx`.

```bash
npm install @torstendittmann/sbx
```

```bash
bun add @torstendittmann/sbx
```

```ts
import { Sandbox } from "@torstendittmann/sbx"

const box = await Sandbox.create({ template: "base", ttl: "5m" })
console.log((await box.exec(["echo", "hello"])).stdout)
await box.kill()
```

The package is ESM and includes TypeScript declarations. It runs on Node 20+ and on Bun. `SBX_URL` defaults to `http://127.0.0.1:8080`. Set `SBX_API_KEY`, or pass `apiKey`. A prerelease tag is published under the npm dist-tag `next` (`npm install @torstendittmann/sbx@next`).

The examples in this repo import the TypeScript source directly and do not need the package installed.
