# @sbx.torsten.work/sbx

TypeScript client for [sbx](https://github.com/TorstenDittmann/sbx), a single-binary sandbox server.

The published package is ESM and ships its types. It runs on Node 20+ and on Bun. `sbx` on npm is a different project, so this package is scoped.

```bash
npm install @sbx.torsten.work/sbx
```

```bash
bun add @sbx.torsten.work/sbx
```

```ts
import { Sandbox } from "@sbx.torsten.work/sbx"

const box = await Sandbox.create({ template: "base", ttl: "5m" })
const result = await box.exec(["echo", "hello"])
console.log(result.stdout)
await box.kill()
```

`SBX_URL` defaults to `http://127.0.0.1:8080`. `SBX_API_KEY` is required unless you pass `apiKey`.

`box.files.watch` yields `{ type, path }` events (`create`, `write`, `remove`). The agent uses inotify and falls back to polling when inotify is unavailable.

`uploadArchive` accepts a tar or gzip tar, and `downloadArchive` returns a gzip tar. When the server is running `sbx-agent`, those calls do not need a `tar` binary in the sandbox image.

`client.createVolume(name)` creates a volume that outlives sandboxes. A name the key already uses fails with 409; `client.ensureVolume(name)` returns the existing volume instead, so a retried job gets the same one back. Pass `mounts: [{ volumeId, path }]` to `Sandbox.create`. Mounting `/workspace` keeps that directory; `diskMb` does not cap it. `deleteVolume` fails while a live sandbox still mounts the volume.

`client.createTemplate({ name, image, warm, setup, network })` keeps ready sandboxes for that template. `Sandbox.create({ template })` claims one when the request does not override the template.

`Sandbox.template(image)` builds a reusable image. Each method returns a new template, so one base can branch into variants. sbx builds the steps once, caches them by content hash, and later creates start from the cached image. `.build()` only warms the cache.

```ts
const base = Sandbox.template("node:22-bookworm-slim")
  .withPackages("ffmpeg", "git")
  .withEnv({ NODE_ENV: "production" }) // build time only
  .workdir("/app")
  .run("npm init -y")

await base.build() // optional
const box = await Sandbox.create(base, { ttl: "5m" })
await box.exec("ffmpeg -version")
```

`Sandbox.template()` with no argument starts from the `base` template, and `Sandbox.template({ template: "python" })` from another one. Builds need an admin key unless the server sets `builds.allow_sandbox_keys`. `client.createTemplate({ name, image, steps, warm })` keeps a named template with steps and a warm pool.

`secrets` on `Sandbox.create` are stored for that sandbox and injected into commands. The API returns `secretKeys` and does not return the values. `client.setSecrets(id, secrets)` replaces them.

`client.listSandboxes({ template, q, labels })` filters the caller's sandboxes. `labels` is an exact match. `q` is a substring of the id, template, image, status, or label text.

Releases are cut from git tags. The workflow sets this package's version from the tag (`v0.2.0` publishes `0.2.0`) and attaches npm provenance. A prerelease tag is published under the `next` dist-tag.
