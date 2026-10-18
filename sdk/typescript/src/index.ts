import { parseDuration } from "./duration.ts"

export { parseDuration } from "./duration.ts"

export class SbxError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = "SbxError"
    this.status = status
    this.code = code
  }
}

export type SandboxStatus = "creating" | "running" | "paused" | "stopped" | "error"

export type SandboxInfo = {
  id: string
  status: SandboxStatus
  backend: string
  template?: string
  image: string
  cpu: number
  memoryMb: number
  diskMb: number
  pids: number
  network: string
  env?: Record<string, string>
  labels?: Record<string, string>
  idleTimeoutSeconds?: number
  expiresAt: string
  createdAt: string
  node: string
}

export type CreateSandboxOptions = {
  template?: string
  image?: string
  cpu?: number
  memoryMb?: number
  diskMb?: number
  pids?: number
  ttl?: number | string
  idleTimeout?: number | string
  env?: Record<string, string>
  network?: { mode: "none" | "full"; allow?: string[] }
  backend?: "docker" | "gvisor"
  labels?: Record<string, string>
}

export type ExecResult = {
  exitCode: number
  stdout: string
  stderr: string
  durationMs: number
  truncated: boolean
}

export type ExecOptions = {
  cwd?: string
  env?: Record<string, string>
  timeout?: number | string
}

export type ExecEvent = {
  type: "stdout" | "stderr" | "exit" | "error"
  data: string
  exitCode?: number
  durationMs?: number
  truncated?: boolean
  code?: string
  message?: string
}

export type FileEntry = {
  name: string
  path: string
  type: "file" | "dir" | "symlink"
  size: number
  mode?: string
  mtime?: string
}

export type ConnectionOptions = {
  baseUrl?: string
  apiKey?: string
  fetch?: typeof fetch
}

type SandboxJSON = {
  id: string
  status: SandboxStatus
  backend: string
  template?: string
  image: string
  cpu: number
  memory_mb: number
  disk_mb: number
  pids: number
  network: string
  env?: Record<string, string>
  labels?: Record<string, string>
  idle_timeout_seconds?: number
  expires_at: string
  created_at: string
  node: string
}

type ExecJSON = {
  exit_code: number
  stdout: string
  stderr: string
  duration_ms: number
  truncated: boolean
}

function mapSandbox(raw: SandboxJSON): SandboxInfo {
  return {
    id: raw.id,
    status: raw.status,
    backend: raw.backend,
    template: raw.template,
    image: raw.image,
    cpu: raw.cpu,
    memoryMb: raw.memory_mb,
    diskMb: raw.disk_mb,
    pids: raw.pids,
    network: raw.network,
    env: raw.env,
    labels: raw.labels,
    idleTimeoutSeconds: raw.idle_timeout_seconds,
    expiresAt: raw.expires_at,
    createdAt: raw.created_at,
    node: raw.node,
  }
}

function mapExec(raw: ExecJSON): ExecResult {
  return {
    exitCode: raw.exit_code,
    stdout: raw.stdout,
    stderr: raw.stderr,
    durationMs: raw.duration_ms,
    truncated: raw.truncated,
  }
}

function commandArgs(cmd: string | string[]): string[] {
  if (typeof cmd === "string") return ["sh", "-lc", cmd]
  if (cmd.length === 0) throw new Error("cmd must not be empty")
  return cmd
}

function createBody(options: CreateSandboxOptions): Record<string, unknown> {
  const body: Record<string, unknown> = {}
  if (options.template) body.template = options.template
  if (options.image) body.image = options.image
  if (options.cpu != null) body.cpu = options.cpu
  if (options.memoryMb != null) body.memory_mb = options.memoryMb
  if (options.diskMb != null) body.disk_mb = options.diskMb
  if (options.pids != null) body.pids = options.pids
  if (options.ttl != null) body.ttl_seconds = parseDuration(options.ttl)
  if (options.idleTimeout != null) body.idle_timeout_seconds = parseDuration(options.idleTimeout)
  if (options.env) body.env = options.env
  if (options.network) body.network = options.network
  if (options.backend) body.backend = options.backend
  if (options.labels) body.labels = options.labels
  return body
}

function execBody(cmd: string | string[], options: ExecOptions = {}): Record<string, unknown> {
  const body: Record<string, unknown> = { cmd: commandArgs(cmd) }
  if (options.cwd) body.cwd = options.cwd
  if (options.env) body.env = options.env
  if (options.timeout != null) body.timeout_seconds = parseDuration(options.timeout)
  return body
}

function modeQuery(mode?: number | string): string | undefined {
  if (mode == null) return undefined
  if (typeof mode === "number") return mode.toString(8)
  return mode.replace(/^0o/, "")
}

// SbxClient is the low-level HTTP client. Sandbox is the usual entry point.
export class SbxClient {
  readonly baseUrl: string
  readonly apiKey: string
  readonly fetchImpl: typeof fetch

  constructor(options: ConnectionOptions = {}) {
    const baseUrl = (options.baseUrl ?? process.env.SBX_URL ?? "http://127.0.0.1:8080").replace(/\/$/, "")
    const apiKey = options.apiKey ?? process.env.SBX_API_KEY ?? ""
    if (!apiKey) throw new Error("an API key is required (pass apiKey or set SBX_API_KEY)")
    this.baseUrl = baseUrl
    this.apiKey = apiKey
    this.fetchImpl = options.fetch ?? globalThis.fetch
  }

  createSandbox(options: CreateSandboxOptions = {}): Promise<SandboxInfo> {
    return this.requestJSON<SandboxJSON>("POST", "/v1/sandboxes", createBody(options)).then(mapSandbox)
  }

  getSandbox(id: string): Promise<SandboxInfo> {
    return this.requestJSON<SandboxJSON>("GET", `/v1/sandboxes/${encodeURIComponent(id)}`).then(mapSandbox)
  }

  listSandboxes(query: { status?: string; backend?: string; labels?: Record<string, string> } = {}): Promise<SandboxInfo[]> {
    const params = new URLSearchParams()
    if (query.status) params.set("status", query.status)
    if (query.backend) params.set("backend", query.backend)
    for (const [key, value] of Object.entries(query.labels ?? {})) {
      params.append("label", `${key}:${value}`)
    }
    const suffix = params.size ? `?${params}` : ""
    return this.requestJSON<{ sandboxes: SandboxJSON[] }>("GET", `/v1/sandboxes${suffix}`).then((body) =>
      body.sandboxes.map(mapSandbox),
    )
  }

  deleteSandbox(id: string): Promise<SandboxInfo> {
    return this.requestJSON<SandboxJSON>("DELETE", `/v1/sandboxes/${encodeURIComponent(id)}`).then(mapSandbox)
  }

  setTTL(id: string, ttl: number | string): Promise<SandboxInfo> {
    return this.requestJSON<SandboxJSON>("POST", `/v1/sandboxes/${encodeURIComponent(id)}/timeout`, {
      ttl_seconds: parseDuration(ttl),
    }).then(mapSandbox)
  }

  pause(id: string): Promise<SandboxInfo> {
    return this.requestJSON<SandboxJSON>("POST", `/v1/sandboxes/${encodeURIComponent(id)}/pause`).then(mapSandbox)
  }

  resume(id: string): Promise<SandboxInfo> {
    return this.requestJSON<SandboxJSON>("POST", `/v1/sandboxes/${encodeURIComponent(id)}/resume`).then(mapSandbox)
  }

  exec(id: string, cmd: string | string[], options?: ExecOptions): Promise<ExecResult> {
    return this.requestJSON<ExecJSON>("POST", `/v1/sandboxes/${encodeURIComponent(id)}/exec`, execBody(cmd, options)).then(mapExec)
  }

  async *execStream(id: string, cmd: string | string[], options?: ExecOptions): AsyncGenerator<ExecEvent> {
    const response = await this.request("POST", `/v1/sandboxes/${encodeURIComponent(id)}/exec?stream=1`, execBody(cmd, options))
    if (!response.body) throw new SbxError(response.status, "driver", "empty exec stream")
    const reader = response.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ""
    while (true) {
      const chunk = await reader.read()
      if (chunk.done) break
      buffer += decoder.decode(chunk.value, { stream: true })
      let split = buffer.indexOf("\n\n")
      while (split >= 0) {
        const block = buffer.slice(0, split)
        buffer = buffer.slice(split + 2)
        const event = parseExecEvent(block)
        if (event) yield event
        split = buffer.indexOf("\n\n")
      }
    }
  }

  listFiles(id: string, path: string): Promise<{ path: string; entries: FileEntry[] }> {
    const q = new URLSearchParams({ path })
    return this.requestJSON("GET", `/v1/sandboxes/${encodeURIComponent(id)}/files?${q}`)
  }

  async readFile(id: string, path: string): Promise<Uint8Array> {
    const q = new URLSearchParams({ path })
    const response = await this.request("GET", `/v1/sandboxes/${encodeURIComponent(id)}/files/content?${q}`)
    return new Uint8Array(await response.arrayBuffer())
  }

  async writeFile(id: string, path: string, content: string | Uint8Array, mode?: number | string): Promise<void> {
    const q = new URLSearchParams({ path })
    const modeValue = modeQuery(mode)
    if (modeValue) q.set("mode", modeValue)
    const body = typeof content === "string" ? new TextEncoder().encode(content) : content
    await this.request("PUT", `/v1/sandboxes/${encodeURIComponent(id)}/files/content?${q}`, body, "application/octet-stream")
  }

  async removeFile(id: string, path: string): Promise<void> {
    const q = new URLSearchParams({ path })
    await this.request("DELETE", `/v1/sandboxes/${encodeURIComponent(id)}/files?${q}`)
  }

  async uploadArchive(id: string, path: string, content: Uint8Array): Promise<void> {
    const q = new URLSearchParams({ path })
    await this.request("POST", `/v1/sandboxes/${encodeURIComponent(id)}/files/archive?${q}`, content, "application/octet-stream")
  }

  async downloadArchive(id: string, path: string): Promise<Uint8Array> {
    const q = new URLSearchParams({ path })
    const response = await this.request("GET", `/v1/sandboxes/${encodeURIComponent(id)}/files/archive?${q}`)
    return new Uint8Array(await response.arrayBuffer())
  }

  private async requestJSON<T>(method: string, path: string, body?: unknown): Promise<T> {
    const response = await this.request(method, path, body == null ? undefined : body, body == null ? undefined : "application/json")
    if (response.status === 204) return undefined as T
    return (await response.json()) as T
  }

  private async request(method: string, path: string, body?: unknown, contentType?: string): Promise<Response> {
    const headers = new Headers({ Authorization: `Bearer ${this.apiKey}` })
    let payload: BodyInit | undefined
    if (body != null && contentType === "application/json") {
      headers.set("Content-Type", "application/json")
      payload = JSON.stringify(body)
    } else if (body instanceof Uint8Array) {
      headers.set("Content-Type", contentType ?? "application/octet-stream")
      payload = body
    }
    const response = await this.fetchImpl(`${this.baseUrl}${path}`, { method, headers, body: payload })
    if (!response.ok) throw await errorFrom(response)
    return response
  }
}

async function errorFrom(response: Response): Promise<SbxError> {
  let code = "driver"
  let message = response.statusText || "request failed"
  try {
    const body = (await response.json()) as { error?: { code?: string; message?: string } }
    if (body.error?.code) code = body.error.code
    if (body.error?.message) message = body.error.message
  } catch {
    // Keep the status text when the body is not JSON.
  }
  return new SbxError(response.status, code, message)
}

function parseExecEvent(block: string): ExecEvent | undefined {
  let type = ""
  const dataLines: string[] = []
  for (const line of block.split("\n")) {
    if (line.startsWith("event:")) type = line.slice(6).trim()
    else if (line.startsWith("data:")) dataLines.push(line.slice(5).trim())
  }
  if (!type) return undefined
  const raw = dataLines.length ? (JSON.parse(dataLines.join("\n")) as Record<string, unknown>) : {}
  if (type === "stdout" || type === "stderr") {
    return { type, data: String(raw.data ?? "") }
  }
  if (type === "exit") {
    return {
      type,
      data: "",
      exitCode: Number(raw.exit_code ?? 0),
      durationMs: Number(raw.duration_ms ?? 0),
      truncated: Boolean(raw.truncated),
    }
  }
  return {
    type: "error",
    data: "",
    code: String(raw.code ?? "driver"),
    message: String(raw.message ?? ""),
  }
}

export class SandboxFiles {
  #client: SbxClient
  #id: string

  constructor(client: SbxClient, id: string) {
    this.#client = client
    this.#id = id
  }

  list(path = "/workspace"): Promise<{ path: string; entries: FileEntry[] }> {
    return this.#client.listFiles(this.#id, path)
  }

  read(path: string): Promise<Uint8Array> {
    return this.#client.readFile(this.#id, path)
  }

  async readText(path: string): Promise<string> {
    const bytes = await this.read(path)
    return new TextDecoder().decode(bytes)
  }

  write(path: string, content: string | Uint8Array, mode?: number | string): Promise<void> {
    return this.#client.writeFile(this.#id, path, content, mode)
  }

  remove(path: string): Promise<void> {
    return this.#client.removeFile(this.#id, path)
  }
}

// Sandbox is one running sandbox plus the calls that use it.
export class Sandbox {
  readonly id: string
  status: SandboxStatus
  backend: string
  template?: string
  image: string
  cpu: number
  memoryMb: number
  diskMb: number
  pids: number
  network: string
  env?: Record<string, string>
  labels?: Record<string, string>
  idleTimeoutSeconds?: number
  expiresAt: string
  createdAt: string
  node: string
  readonly files: SandboxFiles
  #client: SbxClient

  constructor(client: SbxClient, info: SandboxInfo) {
    this.#client = client
    this.id = info.id
    this.status = info.status
    this.backend = info.backend
    this.template = info.template
    this.image = info.image
    this.cpu = info.cpu
    this.memoryMb = info.memoryMb
    this.diskMb = info.diskMb
    this.pids = info.pids
    this.network = info.network
    this.env = info.env
    this.labels = info.labels
    this.idleTimeoutSeconds = info.idleTimeoutSeconds
    this.expiresAt = info.expiresAt
    this.createdAt = info.createdAt
    this.node = info.node
    this.files = new SandboxFiles(client, info.id)
  }

  static async create(options: CreateSandboxOptions & ConnectionOptions = {}): Promise<Sandbox> {
    const client = clientFrom(options)
    return new Sandbox(client, await client.createSandbox(options))
  }

  static async connect(id: string, options: ConnectionOptions = {}): Promise<Sandbox> {
    const client = clientFrom(options)
    return new Sandbox(client, await client.getSandbox(id))
  }

  async refresh(): Promise<SandboxInfo> {
    const info = await this.#client.getSandbox(this.id)
    this.apply(info)
    return info
  }

  exec(cmd: string | string[], options?: ExecOptions): Promise<ExecResult> {
    return this.#client.exec(this.id, cmd, options)
  }

  execStream(cmd: string | string[], options?: ExecOptions): AsyncGenerator<ExecEvent> {
    return this.#client.execStream(this.id, cmd, options)
  }

  async kill(): Promise<SandboxInfo> {
    const info = await this.#client.deleteSandbox(this.id)
    this.apply(info)
    return info
  }

  async pause(): Promise<SandboxInfo> {
    const info = await this.#client.pause(this.id)
    this.apply(info)
    return info
  }

  async resume(): Promise<SandboxInfo> {
    const info = await this.#client.resume(this.id)
    this.apply(info)
    return info
  }

  async setTTL(ttl: number | string): Promise<SandboxInfo> {
    const info = await this.#client.setTTL(this.id, ttl)
    this.apply(info)
    return info
  }

  private apply(info: SandboxInfo): void {
    this.status = info.status
    this.expiresAt = info.expiresAt
    this.backend = info.backend
  }
}

function clientFrom(options: ConnectionOptions): SbxClient {
  return new SbxClient({ baseUrl: options.baseUrl, apiKey: options.apiKey, fetch: options.fetch })
}
