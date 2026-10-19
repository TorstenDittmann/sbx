import assert from "node:assert/strict"
import { createServer, type Server } from "node:http"
import { once } from "node:events"
import test from "node:test"

import { Sandbox, SbxClient } from "../src/index.ts"

test("agent APIs cover exec stdin, processes, watch, preview, and terminals", async () => {
  const seen: string[] = []
  const server = createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on("data", (chunk) => chunks.push(chunk))
    req.on("end", () => {
      const url = req.url ?? ""
      seen.push(`${req.method} ${url}`)
      const body = Buffer.concat(chunks).toString("utf8")
      if (req.method === "POST" && url === "/v1/sandboxes") {
        res.writeHead(201, { "content-type": "application/json" })
        res.end(JSON.stringify(sandboxJSON()))
        return
      }
      if (req.method === "POST" && url === "/v1/sandboxes/sbx_test/exec") {
        const parsed = JSON.parse(body) as { stdin?: string; stdin_base64?: string }
        assert.equal(parsed.stdin, "hi")
        res.writeHead(200, { "content-type": "application/json" })
        res.end(JSON.stringify({ exit_code: 0, stdout: "hi", stderr: "", duration_ms: 1, truncated: false }))
        return
      }
      if (req.method === "POST" && url === "/v1/sandboxes/sbx_test/processes") {
        const parsed = JSON.parse(body) as { stdin_base64?: string; timeout_seconds?: number }
        assert.equal(parsed.stdin_base64, "YWI=")
        assert.equal(parsed.timeout_seconds, 0)
        res.writeHead(201, { "content-type": "application/json" })
        res.end(JSON.stringify({ id: "proc_1", pid: 7, cmd: ["sleep", "30"], cwd: "/workspace", tty: false, status: "running", exit_code: 0 }))
        return
      }
      if (req.method === "GET" && url === "/v1/sandboxes/sbx_test/processes") {
        res.writeHead(200, { "content-type": "application/json" })
        res.end(JSON.stringify({ processes: [{ id: "proc_1", pid: 7, cmd: ["sleep"], status: "running", exit_code: 0, tty: false }] }))
        return
      }
      if (req.method === "POST" && url === "/v1/sandboxes/sbx_test/processes/proc_1/stdin") {
        assert.equal(body, "more")
        res.writeHead(204)
        res.end()
        return
      }
      if (req.method === "GET" && url === "/v1/sandboxes/sbx_test/processes/proc_1/stream") {
        res.writeHead(200, { "content-type": "text/event-stream" })
        res.end('event: stdout\ndata: {"data":"ok"}\n\nevent: exit\ndata: {"exit_code":0,"duration_ms":1,"truncated":false}\n\n')
        return
      }
      if (req.method === "POST" && url === "/v1/sandboxes/sbx_test/processes/proc_1/signal") {
        assert.equal(JSON.parse(body).signal, "KILL")
        res.writeHead(204)
        res.end()
        return
      }
      if (req.method === "GET" && url.startsWith("/v1/sandboxes/sbx_test/files/watch")) {
        res.writeHead(200, { "content-type": "text/event-stream" })
        res.end('event: ready\ndata: {"path":"/workspace"}\n\nevent: change\ndata: {"type":"write","path":"/workspace/a.txt"}\n\n')
        return
      }
      if (req.method === "GET" && url.startsWith("/v1/sandboxes/sbx_test/files/stat")) {
        res.writeHead(200, { "content-type": "application/json" })
        res.end(JSON.stringify({ name: "a.txt", path: "/workspace/a.txt", type: "file", size: 1 }))
        return
      }
      if (req.method === "POST" && url === "/v1/sandboxes/sbx_test/files/mkdir") {
        res.writeHead(204)
        res.end()
        return
      }
      if (req.method === "POST" && url === "/v1/sandboxes/sbx_test/terminals") {
        res.writeHead(201, { "content-type": "application/json" })
        res.end(JSON.stringify({ id: "term_1", pid: 9, cols: 100, rows: 40 }))
        return
      }
      if (req.method === "POST" && url === "/v1/sandboxes/sbx_test/previews") {
        res.writeHead(201, { "content-type": "application/json" })
        res.end(JSON.stringify({ id: "prv_0011223344556677", sandbox_id: "sbx_test", port: 3000, url: "http://example/v1/sandboxes/sbx_test/previews/prv_0011223344556677/" }))
        return
      }
      if (req.method === "GET" && url === "/v1/sandboxes/sbx_test/previews/prv_0011223344556677/health") {
        assert.equal(req.headers.authorization, "Bearer sbx_testkey")
        res.writeHead(200, { "content-type": "text/plain" })
        res.end("preview-ok")
        return
      }
      res.writeHead(500)
      res.end(url)
    })
  })
  await listen(server)
  const address = server.address()
  if (!address || typeof address === "string") throw new Error("no port")
  const baseUrl = `http://127.0.0.1:${address.port}`
  const box = await Sandbox.create({ baseUrl, apiKey: "sbx_testkey", image: "alpine:3.20" })
  const execd = await box.exec(["cat"], { stdin: "hi" })
  assert.equal(execd.stdout, "hi")
  const proc = await box.startProcess(["sleep", "30"], { stdin: new TextEncoder().encode("ab"), timeout: 0 })
  assert.equal(proc.id, "proc_1")
  assert.equal(proc.status, "running")
  const listed = await box.listProcesses()
  assert.equal(listed.length, 1)
  await box.writeProcessStdin(proc.id, "more")
  const events = []
  for await (const event of box.streamProcess(proc.id)) events.push(event.type)
  assert.deepEqual(events, ["stdout", "exit"])
  await box.signalProcess(proc.id, "KILL")
  const changes = []
  for await (const event of box.files.watch("/workspace", { recursive: true })) changes.push(event)
  assert.equal(changes[0]?.path, "/workspace/a.txt")
  const stat = await box.files.stat("/workspace/a.txt")
  assert.equal(stat.size, 1)
  await box.files.mkdir("/workspace/sub", "755")
  const term = await box.startTerminal(["sh"], { cols: 100, rows: 40 })
  const sockets: FakeSocket[] = []
  const previous = globalThis.WebSocket
  globalThis.WebSocket = class {
    binaryType = ""
    url: string
    sent: unknown[] = []
    listeners = new Map<string, ((event: { data: unknown }) => void)[]>()
    constructor(url: string) {
      this.url = url
      sockets.push(this as unknown as FakeSocket)
    }
    addEventListener(type: string, fn: (event: { data: unknown }) => void) {
      const list = this.listeners.get(type) ?? []
      list.push(fn)
      this.listeners.set(type, list)
    }
    send(data: unknown) {
      this.sent.push(data)
    }
    close() {}
    emit(type: string, event: { data: unknown }) {
      for (const fn of this.listeners.get(type) ?? []) fn(event)
    }
  } as unknown as typeof WebSocket
  try {
    const session = box.connectTerminal(term.id)
    assert.equal(sockets.length, 1)
    assert.ok(sockets[0].url.includes("access_token=sbx_testkey"))
    assert.ok(sockets[0].url.startsWith("ws://"))
    const output: string[] = []
    const exits: number[] = []
    session.onOutput((bytes) => output.push(new TextDecoder().decode(bytes)))
    session.onExit((code) => exits.push(code))
    session.resize(120, 40)
    session.send("echo\n")
    const bytes = new TextEncoder().encode("ok")
    sockets[0].emit("message", { data: bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) })
    sockets[0].emit("message", { data: JSON.stringify({ type: "exit", exit_code: 0 }) })
    assert.equal(output.join(""), "ok")
    assert.deepEqual(exits, [0])
    assert.equal(JSON.parse(String(sockets[0].sent[0])).type, "resize")
  } finally {
    globalThis.WebSocket = previous
  }
  const preview = await box.createPreview(3000)
  assert.equal(preview.port, 3000)
  const proxied = await box.previewFetch(preview.id, "/health")
  assert.equal(await proxied.text(), "preview-ok")
  await new Promise((resolve, reject) => server.close((err) => (err ? reject(err) : resolve(undefined))))
})

function sandboxJSON() {
  return {
    id: "sbx_test",
    status: "running",
    backend: "docker",
    image: "alpine:3.20",
    cpu: 1,
    memory_mb: 64,
    disk_mb: 32,
    pids: 32,
    network: "none",
    expires_at: "2026-10-18T00:00:00Z",
    created_at: "2026-10-18T00:00:00Z",
    node: "local",
  }
}

type FakeSocket = {
  url: string
  sent: unknown[]
  emit(type: string, event: { data: unknown }): void
}

function listen(server: Server): Promise<void> {
  server.listen(0, "127.0.0.1")
  return once(server, "listening").then(() => undefined)
}
