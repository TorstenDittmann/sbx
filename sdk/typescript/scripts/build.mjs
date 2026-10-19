import { spawnSync } from "node:child_process"
import { mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises"
import { dirname, join, relative } from "node:path"
import { fileURLToPath } from "node:url"

// Source imports end in .ts so Node's type stripper and Bun can run the tests
// without a build. Published ESM has to import the emitted .js files, so this
// stages a copy with those specifiers rewritten and then runs tsc.
const root = dirname(dirname(fileURLToPath(import.meta.url)))
const src = join(root, "src")
const staged = join(root, "build-src")
const dist = join(root, "dist")

await rm(staged, { recursive: true, force: true })
await rm(dist, { recursive: true, force: true })

async function stage(dir) {
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const from = join(dir, entry.name)
    const to = join(staged, relative(src, from))
    if (entry.isDirectory()) {
      await mkdir(to, { recursive: true })
      await stage(from)
      continue
    }
    let text = await readFile(from, "utf8")
    if (entry.name.endsWith(".ts") && !entry.name.endsWith(".d.ts")) {
      text = text.replaceAll(/(from\s+["'])(\.[^"']+)\.ts(["'])/g, "$1$2.js$3")
      text = text.replaceAll(/(import\s+["'])(\.[^"']+)\.ts(["'])/g, "$1$2.js$3")
    }
    await mkdir(dirname(to), { recursive: true })
    await writeFile(to, text)
  }
}

await stage(src)

const tsc = join(root, "node_modules", "typescript", "bin", "tsc")
const result = spawnSync(process.execPath, [tsc, "-p", join(root, "tsconfig.json")], { stdio: "inherit" })
await rm(staged, { recursive: true, force: true })
process.exit(result.status === null ? 1 : result.status)
