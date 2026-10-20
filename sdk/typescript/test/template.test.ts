import assert from "node:assert/strict"
import { createServer } from "node:http"
import { once } from "node:events"
import test from "node:test"

import { Sandbox, Template } from "../src/index.ts"

test("Template is immutable and branches", () => {
  const base = Sandbox.template("node:22")
    .withPackages("ffmpeg", "git")
    .withEnv({ NODE_ENV: "production" })
    .workdir("/app")
  const install = base.run("npm install")
  const test = base.run("npm test")
  assert.equal(base.steps.length, 3)
  assert.deepEqual(install.steps.at(-1), { run: "npm install" })
  assert.deepEqual(test.steps.at(-1), { run: "npm test" })
  assert.equal(install.image, "node:22")
  assert.ok(Object.isFrozen(base.steps))
  assert.deepEqual(Sandbox.template().template, "base")
  assert.deepEqual(Sandbox.template({ template: "python" }).template, "python")
  assert.throws(() => base.withPackages())
  assert.throws(() => base.run(""))
})

test("Sandbox.create and Template.build send the steps", async () => {
  const bodies: Array<{ url?: string; body: Record<string, unknown> }> = []
  const server = createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on("data", (chunk) => chunks.push(chunk))
    req.on("end", () => {
      const body = JSON.parse(Buffer.concat(chunks).toString("utf8") || "{}")
      bodies.push({ url: req.url, body })
      if (req.url === "/v1/builds") {
        res.writeHead(201, { "content-type": "application/json" })
        res.end(JSON.stringify({ id: "abc", image: "sbx-template:abc", base: "node:22", steps: body.steps, cached: false, duration_ms: 1200 }))
        return
      }
      res.writeHead(201, { "content-type": "application/json" })
      res.end(JSON.stringify({
        id: "sbx_test", status: "running", backend: "docker", image: "sbx-template:abc",
        cpu: 1, memory_mb: 512, disk_mb: 1024, pids: 256, network: "none",
        expires_at: "2026-10-18T20:00:00Z", created_at: "2026-10-18T19:55:00Z", node: "local",
      }))
    })
  })
  server.listen(0, "127.0.0.1")
  await once(server, "listening")
  const address = server.address()
  if (!address || typeof address === "string") throw new Error("no port")
  const conn = { baseUrl: `http://127.0.0.1:${address.port}`, apiKey: "sbx_testkey" }

  const tpl = Sandbox.template("node:22").withPackages("git").workdir("/app").run("npm install")
  const built = await tpl.build(conn)
  assert.equal(built.image, "sbx-template:abc")
  assert.equal(built.durationMs, 1200)
  assert.deepEqual(bodies[0], {
    url: "/v1/builds",
    body: { image: "node:22", steps: [{ packages: ["git"] }, { workdir: "/app" }, { run: "npm install" }] },
  })

  const box = await Sandbox.create(tpl, { ...conn, ttl: "5m", env: { PORT: "3000" } })
  assert.equal(box.image, "sbx-template:abc")
  assert.deepEqual(bodies[1]?.body, {
    image: "node:22",
    steps: [{ packages: ["git"] }, { workdir: "/app" }, { run: "npm install" }],
    ttl_seconds: 300,
    env: { PORT: "3000" },
  })

  await Sandbox.create({ ...conn, template: new Template({ template: "python" }).run("pip install requests") })
  assert.deepEqual(bodies[2]?.body, { template: "python", steps: [{ run: "pip install requests" }] })

  server.close()
})
