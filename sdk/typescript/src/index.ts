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
  stdin?: string | Uint8Array
}

export type ProcessInfo = {
  id: string
  pid: number
  cmd: string[]
  cwd?: string
  tty: boolean
  status: "running" | "exited"
  exitCode: number
  startedAt?: string
}

export type TerminalInfo = {
  id: string
  pid: number
  cols: number
  rows: number
}

export type PreviewInfo = {
  id: string
  sandboxId: string
  port: number
  url: string
  createdAt?: string
}

export type FileEvent = {
  type: string
  path: string
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
  if (typeof options.stdin === "string") body.stdin = options.stdin
  else if (options.stdin) body.stdin_base64 = bytesToBase64(options.stdin)
  return body
}

function envValue(name: string): string | undefined {
  const proc = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process
  return proc?.env?.[name]
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = ""
  const chunk = 0x8000
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk))
  }
  return btoa(binary)
}

function wsBase(httpBase: string): string {
  if (httpBase.startsWith("https://")) return "wss://" + httpBase.slice("https://".length)
  if (httpBase.startsWith("http://")) return "ws://" + httpBase.slice("http://".length)
  return httpBase
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
    const baseUrl = (options.baseUrl ?? envValue("SBX_URL") ?? "http://127.0.0.1:8080").replace(/\/$/, "")
    const apiKey = options.apiKey ?? envValue("SBX_API_KEY") ?? ""
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
    yield* readExecEvents(response.body)
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

  statFile(id: string, path: string): Promise<FileEntry> {
    const q = new URLSearchParams({ path })
    return this.requestJSON("GET", `/v1/sandboxes/${encodeURIComponent(id)}/files/stat?${q}`)
  }

  mkdir(id: string, path: string, mode?: number | string): Promise<void> {
    const body: Record<string, unknown> = { path }
    const modeValue = modeQuery(mode)
    if (modeValue) body.mode = modeValue
    return this.requestJSON("POST", `/v1/sandboxes/${encodeURIComponent(id)}/files/mkdir`, body)
  }

  async *watch(id: string, path: string, options: { recursive?: boolean } = {}): AsyncGenerator<FileEvent> {
    const q = new URLSearchParams({ path })
    if (options.recursive) q.set("recursive", "1")
    const response = await this.request("GET", `/v1/sandboxes/${encodeURIComponent(id)}/files/watch?${q}`)
    if (!response.body) throw new SbxError(response.status, "driver", "empty watch stream")
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
        const event = parseSSE(block)
        if (event?.event === "change") {
          const raw = event.json
          yield { type: String(raw.type ?? ""), path: String(raw.path ?? "") }
        } else if (event?.event === "error") {
          throw new SbxError(502, String(event.json.code ?? "driver"), String(event.json.message ?? "watch failed"))
        }
        split = buffer.indexOf("\n\n")
      }
    }
  }

  startProcess(id: string, cmd: string | string[], options: ExecOptions = {}): Promise<ProcessInfo> {
    return this.requestJSON<ProcessJSON>("POST", `/v1/sandboxes/${encodeURIComponent(id)}/processes`, execBody(cmd, options)).then(mapProcess)
  }

  listProcesses(id: string): Promise<ProcessInfo[]> {
    return this.requestJSON<{ processes: ProcessJSON[] }>("GET", `/v1/sandboxes/${encodeURIComponent(id)}/processes`).then((body) =>
      (body.processes ?? []).map(mapProcess),
    )
  }

  signalProcess(id: string, procId: string, signal: "TERM" | "KILL" | "INT" | "HUP" | "QUIT"): Promise<void> {
    return this.requestJSON("POST", `/v1/sandboxes/${encodeURIComponent(id)}/processes/${encodeURIComponent(procId)}/signal`, { signal })
  }

  async writeProcessStdin(id: string, procId: string, data: string | Uint8Array): Promise<void> {
    const body = typeof data === "string" ? new TextEncoder().encode(data) : data
    await this.request("POST", `/v1/sandboxes/${encodeURIComponent(id)}/processes/${encodeURIComponent(procId)}/stdin`, body, "application/octet-stream")
  }

  async *streamProcess(id: string, procId: string): AsyncGenerator<ExecEvent> {
    const response = await this.request("GET", `/v1/sandboxes/${encodeURIComponent(id)}/processes/${encodeURIComponent(procId)}/stream`)
    if (!response.body) throw new SbxError(response.status, "driver", "empty process stream")
    yield* readExecEvents(response.body)
  }

  startTerminal(id: string, cmd: string | string[], options: ExecOptions & { cols?: number; rows?: number } = {}): Promise<TerminalInfo> {
    const body = execBody(cmd, options)
    if (options.cols != null) body.cols = options.cols
    if (options.rows != null) body.rows = options.rows
    return this.requestJSON<TerminalJSON>("POST", `/v1/sandboxes/${encodeURIComponent(id)}/terminals`, body).then(mapTerminal)
  }

  resizeTerminal(id: string, termId: string, cols: number, rows: number): Promise<void> {
    return this.requestJSON("POST", `/v1/sandboxes/${encodeURIComponent(id)}/terminals/${encodeURIComponent(termId)}/resize`, { cols, rows })
  }

  connectTerminal(id: string, termId: string): TerminalSession {
    const WS = (globalThis as { WebSocket?: typeof WebSocket }).WebSocket
    if (!WS) throw new Error("WebSocket is not available")
    const url = `${wsBase(this.baseUrl)}/v1/sandboxes/${encodeURIComponent(id)}/terminals/${encodeURIComponent(termId)}/ws?access_token=${encodeURIComponent(this.apiKey)}`
    return new TerminalSession(termId, new WS(url))
  }

  createPreview(id: string, port: number): Promise<PreviewInfo> {
    return this.requestJSON<PreviewJSON>("POST", `/v1/sandboxes/${encodeURIComponent(id)}/previews`, { port }).then(mapPreview)
  }

  listPreviews(id: string): Promise<PreviewInfo[]> {
    return this.requestJSON<{ previews: PreviewJSON[] }>("GET", `/v1/sandboxes/${encodeURIComponent(id)}/previews`).then((body) =>
      (body.previews ?? []).map(mapPreview),
    )
  }

  deletePreview(id: string, previewId: string): Promise<void> {
    return this.requestJSON("DELETE", `/v1/sandboxes/${encodeURIComponent(id)}/previews/${encodeURIComponent(previewId)}`)
  }

  previewFetch(id: string, previewId: string, path = "/", init: RequestInit = {}): Promise<Response> {
    const suffix = path.startsWith("/") ? path : `/${path}`
    const headers = new Headers(init.headers)
    headers.set("Authorization", `Bearer ${this.apiKey}`)
    return this.fetchImpl(`${this.baseUrl}/v1/sandboxes/${encodeURIComponent(id)}/previews/${encodeURIComponent(previewId)}${suffix}`, {
      ...init,
      headers,
    })
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

type ProcessJSON = {
  id: string
  pid: number
  cmd: string[]
  cwd?: string
  tty?: boolean
  status: "running" | "exited"
  exit_code: number
  started_at?: string
}

type TerminalJSON = { id: string; pid: number; cols: number; rows: number }

type PreviewJSON = {
  id: string
  sandbox_id: string
  port: number
  url: string
  created_at?: string
}

function mapProcess(raw: ProcessJSON): ProcessInfo {
  return {
    id: raw.id,
    pid: raw.pid,
    cmd: raw.cmd,
    cwd: raw.cwd,
    tty: Boolean(raw.tty),
    status: raw.status,
    exitCode: raw.exit_code,
    startedAt: raw.started_at,
  }
}

function mapTerminal(raw: TerminalJSON): TerminalInfo {
  return { id: raw.id, pid: raw.pid, cols: raw.cols, rows: raw.rows }
}

function mapPreview(raw: PreviewJSON): PreviewInfo {
  return { id: raw.id, sandboxId: raw.sandbox_id, port: raw.port, url: raw.url, createdAt: raw.created_at }
}

async function* readExecEvents(body: ReadableStream<Uint8Array>): AsyncGenerator<ExecEvent> {
  const reader = body.getReader()
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

function parseSSE(block: string): { event: string; json: Record<string, unknown> } | undefined {
  let event = ""
  const dataLines: string[] = []
  for (const line of block.split("\n")) {
    if (line.startsWith("event:")) event = line.slice(6).trim()
    else if (line.startsWith("data:")) dataLines.push(line.slice(5).trim())
  }
  if (!event) return undefined
  const json = dataLines.length ? (JSON.parse(dataLines.join("\n")) as Record<string, unknown>) : {}
  return { event, json }
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

  stat(path: string): Promise<FileEntry> {
    return this.#client.statFile(this.#id, path)
  }

  mkdir(path: string, mode?: number | string): Promise<void> {
    return this.#client.mkdir(this.#id, path, mode)
  }

  watch(path = "/workspace", options: { recursive?: boolean } = {}): AsyncGenerator<FileEvent> {
    return this.#client.watch(this.#id, path, options)
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

  startProcess(cmd: string | string[], options?: ExecOptions): Promise<ProcessInfo> {
    return this.#client.startProcess(this.id, cmd, options)
  }

  listProcesses(): Promise<ProcessInfo[]> {
    return this.#client.listProcesses(this.id)
  }

  signalProcess(procId: string, signal: "TERM" | "KILL" | "INT" | "HUP" | "QUIT"): Promise<void> {
    return this.#client.signalProcess(this.id, procId, signal)
  }

  writeProcessStdin(procId: string, data: string | Uint8Array): Promise<void> {
    return this.#client.writeProcessStdin(this.id, procId, data)
  }

  streamProcess(procId: string): AsyncGenerator<ExecEvent> {
    return this.#client.streamProcess(this.id, procId)
  }

  startTerminal(cmd: string | string[], options?: ExecOptions & { cols?: number; rows?: number }): Promise<TerminalInfo> {
    return this.#client.startTerminal(this.id, cmd, options)
  }

  connectTerminal(termId: string): TerminalSession {
    return this.#client.connectTerminal(this.id, termId)
  }

  createPreview(port: number): Promise<PreviewInfo> {
    return this.#client.createPreview(this.id, port)
  }

  listPreviews(): Promise<PreviewInfo[]> {
    return this.#client.listPreviews(this.id)
  }

  deletePreview(previewId: string): Promise<void> {
    return this.#client.deletePreview(this.id, previewId)
  }

  previewFetch(previewId: string, path?: string, init?: RequestInit): Promise<Response> {
    return this.#client.previewFetch(this.id, previewId, path, init)
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

// TerminalSession is a WebSocket attached to a PTY. Binary frames are bytes.
// Text frames are JSON control messages.
export class TerminalSession {
  readonly id: string
  readonly socket: WebSocket
  #output = new Set<(data: Uint8Array) => void>()
  #exit = new Set<(code: number) => void>()

  constructor(id: string, socket: WebSocket) {
    this.id = id
    this.socket = socket
    this.socket.binaryType = "arraybuffer"
    this.socket.addEventListener("message", (event) => {
      const data = event.data
      if (typeof data === "string") {
        try {
          const msg = JSON.parse(data) as { type?: string; exit_code?: number }
          if (msg.type === "exit") {
            for (const fn of this.#exit) fn(Number(msg.exit_code ?? 0))
          }
        } catch {
          // Ignore non-JSON text.
        }
        return
      }
      const bytes = data instanceof ArrayBuffer ? new Uint8Array(data) : new Uint8Array()
      for (const fn of this.#output) fn(bytes)
    })
  }

  send(data: string | Uint8Array): void {
    const bytes = typeof data === "string" ? new TextEncoder().encode(data) : data
    this.socket.send(bytes)
  }

  resize(cols: number, rows: number): void {
    this.socket.send(JSON.stringify({ type: "resize", cols, rows }))
  }

  onOutput(fn: (data: Uint8Array) => void): () => void {
    this.#output.add(fn)
    return () => this.#output.delete(fn)
  }

  onExit(fn: (code: number) => void): () => void {
    this.#exit.add(fn)
    return () => this.#exit.delete(fn)
  }

  close(): void {
    this.socket.close()
  }
}
