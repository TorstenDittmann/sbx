import assert from "node:assert/strict"
import { createServer, type Server } from "node:http"
import { once } from "node:events"
import test from "node:test"

import { parseDuration, Sandbox, SbxClient, SbxError, type SandboxEvent } from "../src/index.ts"

test("parseDuration accepts seconds and unit suffixes", () => {
  assert.equal(parseDuration(90), 90)
  assert.equal(parseDuration("30s"), 30)
  assert.equal(parseDuration("5m"), 300)
  assert.equal(parseDuration("1h"), 3600)
  assert.equal(parseDuration("500ms"), 1)
  assert.throws(() => parseDuration("soon"))
  assert.throws(() => parseDuration(-1))
})

test("client maps create, exec, files, and errors", async () => {
  const seen: { method?: string; url?: string; body?: string; auth?: string } = {}
  const server = createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on("data", (chunk) => chunks.push(chunk))
    req.on("end", () => {
      seen.method = req.method
      seen.url = req.url
      seen.auth = req.headers.authorization
      seen.body = Buffer.concat(chunks).toString("utf8")
      if (req.url === "/v1/sandboxes" && req.method === "POST") {
        res.writeHead(201, { "content-type": "application/json" })
        res.end(JSON.stringify(sandboxJSON()))
        return
      }
      if (req.url?.startsWith("/v1/sandboxes/sbx_test/exec") && !req.url.includes("stream")) {
        res.writeHead(200, { "content-type": "application/json" })
        res.end(JSON.stringify({ exit_code: 0, stdout: "hi\n", stderr: "", duration_ms: 4, truncated: false }))
        return
      }
      if (req.url?.includes("stream=1")) {
        res.writeHead(200, { "content-type": "text/event-stream" })
        res.end('event: stdout\ndata: {"data":"stream"}\n\n' + 'event: exit\ndata: {"exit_code":0,"duration_ms":2,"truncated":false}\n\n')
        return
      }
      if (req.method === "PUT" && req.url?.includes("/files/content")) {
        res.writeHead(204)
        res.end()
        return
      }
      if (req.method === "GET" && req.url?.startsWith("/v1/sandboxes?")) {
        res.writeHead(200, { "content-type": "application/json" })
        res.end(JSON.stringify({ sandboxes: [sandboxJSON()] }))
        return
      }
      if (req.method === "GET" && req.url === "/v1/sandboxes/sbx_test") {
        res.writeHead(200, { "content-type": "application/json" })
        res.end(JSON.stringify(sandboxJSON()))
        return
      }
      if (req.method === "POST" && req.url === "/v1/templates") {
        res.writeHead(201, { "content-type": "application/json" })
        res.end(JSON.stringify({
          name: "agent", image: "node:22-bookworm-slim", cpu: 1, memory_mb: 512, disk_mb: 1024,
          network: "full", setup: ["npm", "install"], setup_timeout_seconds: 120, warm: 2,
          builtin: false, created_at: "2026-10-19T12:00:00Z",
        }))
        return
      }
      if (req.method === "GET" && req.url === "/v1/templates") {
        res.writeHead(200, { "content-type": "application/json" })
        res.end(JSON.stringify({ templates: [{
          name: "agent", image: "node:22-bookworm-slim", cpu: 1, memory_mb: 512, disk_mb: 1024,
          network: "full", setup: ["npm", "install"], setup_timeout_seconds: 120, warm: 2,
          builtin: false, created_at: "2026-10-19T12:00:00Z",
        }] }))
        return
      }
      if (req.method === "DELETE" && req.url === "/v1/templates/agent") {
        res.writeHead(204)
        res.end()
        return
      }
      if (req.method === "PUT" && req.url === "/v1/sandboxes/sbx_test/secrets") {
        const sent = JSON.parse(Buffer.concat(chunks).toString("utf8")) as { secrets: Record<string, string> }
        res.writeHead(200, { "content-type": "application/json" })
        res.end(JSON.stringify({ ...sandboxJSON(), secret_keys: Object.keys(sent.secrets) }))
        return
      }
      if (req.method === "GET" && req.url === "/v1/sandboxes/missing") {
        res.writeHead(404, { "content-type": "application/json" })
        res.end(JSON.stringify({ error: { code: "not_found", message: "gone" } }))
        return
      }
      if (req.method === "POST" && req.url === "/v1/volumes") {
        res.writeHead(201, { "content-type": "application/json" })
        res.end(JSON.stringify({ id: "vol_abc0123456789ab", name: "cache", backend: "docker", created_at: "2026-10-19T12:00:00Z" }))
        return
      }
      if (req.method === "GET" && req.url === "/v1/volumes") {
        res.writeHead(200, { "content-type": "application/json" })
        res.end(JSON.stringify({ volumes: [{ id: "vol_abc0123456789ab", name: "cache", backend: "docker", created_at: "2026-10-19T12:00:00Z" }] }))
        return
      }
      if (req.method === "GET" && req.url === "/v1/volumes/vol_abc0123456789ab") {
        res.writeHead(200, { "content-type": "application/json" })
        res.end(JSON.stringify({ id: "vol_abc0123456789ab", name: "cache", backend: "docker", created_at: "2026-10-19T12:00:00Z" }))
        return
      }
      if (req.method === "DELETE" && req.url === "/v1/volumes/vol_abc0123456789ab") {
        res.writeHead(204)
        res.end()
        return
      }
      res.writeHead(500)
      res.end("no")
    })
  })
  await listen(server)
  const address = server.address()
  if (!address || typeof address === "string") throw new Error("no port")
  const baseUrl = `http://127.0.0.1:${address.port}`
  const client = new SbxClient({ baseUrl, apiKey: "sbx_testkey" })

  const created = await client.createSandbox({ template: "base", ttl: "5m", memoryMb: 128, labels: { user: "1" } })
  assert.equal(created.id, "sbx_test")
  assert.equal(created.memoryMb, 64)
  assert.equal(created.node, "local")
  assert.equal(seen.auth, "Bearer sbx_testkey")
  assert.deepEqual(JSON.parse(seen.body ?? "{}"), {
    template: "base",
    ttl_seconds: 300,
    memory_mb: 128,
    labels: { user: "1" },
  })

  const result = await client.exec("sbx_test", "echo hi", { timeout: "20s" })
  assert.equal(result.stdout, "hi\n")
  assert.equal(result.exitCode, 0)
  assert.deepEqual(JSON.parse(seen.body ?? "{}").cmd, ["sh", "-lc", "echo hi"])
  assert.equal(JSON.parse(seen.body ?? "{}").timeout_seconds, 20)

  const events = []
  for await (const event of client.execStream("sbx_test", ["echo", "stream"])) {
    events.push(event)
  }
  assert.equal(events[0]?.type, "stdout")
  assert.equal(events[0]?.data, "stream")
  assert.equal(events[1]?.type, "exit")
  assert.equal(events[1]?.exitCode, 0)

  await client.writeFile("sbx_test", "/workspace/a.txt", "hello", 0o644)
  assert.match(seen.url ?? "", /path=%2Fworkspace%2Fa.txt/)
  assert.match(seen.url ?? "", /mode=644/)
  assert.equal(seen.body, "hello")

  await assert.rejects(client.getSandbox("missing"), (err: unknown) => {
    assert.ok(err instanceof SbxError)
    assert.equal(err.status, 404)
    assert.equal(err.code, "not_found")
    return true
  })

  const networked = await client.createSandbox({
    image: "alpine:3.20",
    network: { mode: "allow", allow: [], deny: ["example.net"], egressKbps: 1000, maxConnections: 8 },
  })
  assert.equal(networked.network, "none")
  assert.deepEqual(JSON.parse(seen.body ?? "{}").network, {
    mode: "allow",
    allow: [],
    deny: ["example.net"],
    egress_kbps: 1000,
    max_connections: 8,
  })

  const found = await client.listSandboxes({ template: "base", q: "alp", labels: { user: "1" } })
  assert.equal(found.length, 1)
  assert.match(seen.url ?? "", /template=base/)
  assert.match(seen.url ?? "", /q=alp/)
  assert.match(seen.url ?? "", /label=user%3A1/)

  const sandbox = await Sandbox.connect("sbx_test", { baseUrl, apiKey: "sbx_testkey" })
  assert.equal(sandbox.id, "sbx_test")

  const mounted = await client.createSandbox({ mounts: [{ volumeId: "vol_abc0123456789ab", path: "/cache" }] })
  assert.equal(mounted.id, "sbx_test")
  assert.deepEqual(JSON.parse(seen.body ?? "{}").mounts, [{ volume_id: "vol_abc0123456789ab", path: "/cache" }])

  const volume = await client.createVolume("cache")
  assert.equal(volume.id, "vol_abc0123456789ab")
  assert.equal(volume.name, "cache")
  assert.deepEqual(JSON.parse(seen.body ?? "{}"), { name: "cache" })
  await client.ensureVolume("cache")
  assert.deepEqual(JSON.parse(seen.body ?? "{}"), { name: "cache", if_exists: "reuse" })
  const volumes = await client.listVolumes()
  assert.equal(volumes.length, 1)
  const one = await client.getVolume(volume.id)
  assert.equal(one.backend, "docker")
  await client.deleteVolume(volume.id)
  assert.equal(seen.method, "DELETE")
  assert.equal(seen.url, "/v1/volumes/vol_abc0123456789ab")

  const template = await client.createTemplate({
    name: "agent",
    image: "node:22-bookworm-slim",
    network: "full",
    setup: ["npm", "install"],
    setupTimeout: "2m",
    warm: 2,
  })
  assert.equal(template.warm, 2)
  assert.equal(template.network, "full")
  assert.deepEqual(JSON.parse(seen.body ?? "{}"), {
    name: "agent",
    image: "node:22-bookworm-slim",
    network: "full",
    setup: ["npm", "install"],
    setup_timeout_seconds: 120,
    warm: 2,
  })
  const templates = await client.listTemplates()
  assert.equal(templates[0]?.setup[0], "npm")
  await client.deleteTemplate("agent")
  assert.equal(seen.method, "DELETE")

  await client.createSandbox({ secrets: { TOKEN: "sekret" } })
  assert.deepEqual(JSON.parse(seen.body ?? "{}").secrets, { TOKEN: "sekret" })
  const withSecret = await client.setSecrets("sbx_test", { TOKEN: "next" })
  assert.deepEqual(withSecret.secretKeys, ["TOKEN"])
  assert.equal(JSON.parse(seen.body ?? "{}").secrets.TOKEN, "next")

  await server.close()
})

test("listBackends returns the ladder", async () => {
  const server = createServer((req, res) => {
    if (req.url === "/v1/backends") {
      res.writeHead(200, { "content-type": "application/json" })
      res.end(JSON.stringify({
        backends: [{
          name: "docker", available: true, isolation: "container",
          pause: true, snapshot: false, exec: true, pty: true, files: true,
          watch: true, preview: true, networks: ["none", "full"], requirements: ["docker"],
        }],
      }))
      return
    }
    res.writeHead(404)
    res.end()
  })
  await listen(server)
  const address = server.address()
  if (!address || typeof address === "string") throw new Error("no port")
  const client = new SbxClient({ baseUrl: `http://127.0.0.1:${address.port}`, apiKey: "sbx_testkey" })
  const rows = await client.listBackends()
  assert.equal(rows.length, 1)
  assert.equal(rows[0].name, "docker")
  assert.equal(rows[0].available, true)
  await server.close()
})

async function listen(server: Server): Promise<void> {
  server.listen(0, "127.0.0.1")
  await once(server, "listening")
}

function sandboxJSON() {
  return {
    id: "sbx_test",
    status: "running",
    backend: "docker",
    template: "base",
    image: "alpine:3.20",
    cpu: 0.5,
    memory_mb: 64,
    disk_mb: 32,
    pids: 64,
    network: "none",
    expires_at: "2026-10-18T20:00:00Z",
    created_at: "2026-10-18T19:55:00Z",
    node: "local",
  }
}

test("client lists, gets, and revokes keys", async () => {
  const calls: string[] = []
  const key = { id: "key_1", name: "ci", prefix: "sbx_ab", scopes: ["sandbox"], created_at: "2026-10-20T00:00:00Z", expires_at: "2027-01-01T00:00:00Z" }
  const server = createServer((req, res) => {
    calls.push(`${req.method} ${req.url}`)
    if (req.method === "GET" && req.url === "/v1/keys") {
      res.writeHead(200, { "content-type": "application/json" })
      res.end(JSON.stringify({ keys: [key] }))
      return
    }
    if (req.method === "GET" && req.url === "/v1/keys/key_1") {
      res.writeHead(200, { "content-type": "application/json" })
      res.end(JSON.stringify({ ...key, revoked_at: "2026-10-21T00:00:00Z" }))
      return
    }
    if (req.method === "DELETE" && req.url === "/v1/keys/key_1") {
      res.writeHead(204)
      res.end()
      return
    }
    res.writeHead(404, { "content-type": "application/json" })
    res.end(JSON.stringify({ error: { code: "not_found", message: "nope" } }))
  })
  server.listen(0, "127.0.0.1")
  await once(server, "listening")
  try {
    const { port } = server.address() as { port: number }
    const client = new SbxClient({ baseUrl: `http://127.0.0.1:${port}`, apiKey: "sbx_test" })
    const keys = await client.listKeys()
    assert.equal(keys[0].expiresAt, "2027-01-01T00:00:00Z")
    await client.revokeKey("key_1")
    const got = await client.getKey("key_1")
    assert.equal(got.revokedAt, "2026-10-21T00:00:00Z")
    assert.deepEqual(calls, ["GET /v1/keys", "DELETE /v1/keys/key_1", "GET /v1/keys/key_1"])
  } finally {
    server.close()
  }
})

test("events streams lifecycle events and resumes after a drop", async () => {
  const urls: string[] = []
  const server = createServer((req, res) => {
    urls.push(req.url ?? "")
    res.writeHead(200, { "content-type": "text/event-stream" })
    if (urls.length === 1) {
      // The first stream starts after id 4, sends one event, then drops.
      res.end(
        "id: 4\n\n" +
          'id: 5\nevent: sandbox.created\ndata: {"id":5,"type":"sandbox.created","sandbox_id":"sbx_a","time":"2026-10-23T12:00:00Z","data":{"status":"running"}}\n\n',
      )
      return
    }
    res.write(": ping\n\n")
    res.write(
      'id: 6\nevent: sandbox.lost\ndata: {"id":6,"type":"sandbox.lost","sandbox_id":"sbx_a","time":"2026-10-23T12:00:01Z","data":{"status":"stopped","reason":"missing"}}\n\n',
    )
  })
  server.listen(0)
  await once(server, "listening")
  try {
    const { port } = server.address() as { port: number }
    const client = new SbxClient({ baseUrl: `http://127.0.0.1:${port}`, apiKey: "sbx_test" })
    const controller = new AbortController()
    const seen: SandboxEvent[] = []
    for await (const event of client.events({ sandbox: "sbx_a", signal: controller.signal })) {
      seen.push(event)
      if (seen.length === 2) break
    }
    controller.abort()
    assert.deepEqual(
      seen.map((e) => [e.id, e.type, e.sandboxId, e.data.status]),
      [
        [5, "sandbox.created", "sbx_a", "running"],
        [6, "sandbox.lost", "sbx_a", "stopped"],
      ],
    )
    assert.equal(seen[1].data.reason, "missing")
    assert.equal(urls[0], "/v1/events?sandbox=sbx_a")
    assert.equal(urls[1], "/v1/events?after=5&sandbox=sbx_a")
  } finally {
    server.closeAllConnections()
    server.close()
  }
})
