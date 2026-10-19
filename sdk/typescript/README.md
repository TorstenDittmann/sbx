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

Releases are cut from git tags. The workflow sets this package's version from the tag (`v0.2.0` publishes `0.2.0`) and attaches npm provenance. A prerelease tag is published under the `next` dist-tag.
