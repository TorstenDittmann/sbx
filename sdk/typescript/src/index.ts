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

export type SandboxStatus = "creating" | "running" | "paused" | "stopped" | "error" | "warm"

// BuildStep is one template build step. See Template for the builder.
export type BuildStep =
  | { run: string }
  | { packages: string[] }
  | { env: Record<string, string> }
  | { workdir: string }

export type BuildInfo = {
  // id is the content hash of the base image and steps. It is empty when no
  // step runs a command, and image is then the base itself.
  id: string
  image: string
  base: string
  steps: BuildStep[]
  cached: boolean
  durationMs: number
}

export type BuildOptions = {
  backend?: "docker" | "gvisor" | "kata" | "firecracker"
}

export type TemplateInfo = {
  name: string
  image: string
  description?: string
  cpu: number
  memoryMb: number
  diskMb: number
  env?: Record<string, string>
  backend?: string
  network: string
  setup: string[]
  setupTimeoutSeconds: number
  warm: number
  steps: BuildStep[]
  builtin: boolean
  createdAt: string
}

export type CreateTemplateOptions = {
  name: string
  image: string
  description?: string
  cpu?: number
  memoryMb?: number
  diskMb?: number
  env?: Record<string, string>
  backend?: "docker" | "gvisor" | "kata" | "firecracker"
  network?: "none" | "full"
  setup?: string[]
  setupTimeout?: number | string
  warm?: number
  steps?: BuildStep[]
}

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
  egressAllow?: string[]
  egressDeny?: string[]
  egressKbps?: number
  maxConnections?: number
  env?: Record<string, string>
  labels?: Record<string, string>
  secretKeys?: string[]
  idleTimeoutSeconds?: number
  expiresAt: string
  createdAt: string
  node: string
  mounts?: VolumeMount[]
}

export type VolumeMount = {
  volumeId: string
  path: string
}

export type VolumeInfo = {
  id: string
  name: string
  backend: string
  createdAt: string
}

export type NetworkMode = "none" | "full" | "allow"

export type NetworkPolicy = {
  mode?: NetworkMode
  allow?: string[]
  deny?: string[]
  egressKbps?: number
  maxConnections?: number
}

export type BackendInfo = {
  name: string
  available: boolean
  detail?: string
  isolation: string
  pause: boolean
  snapshot: boolean
  exec: boolean
  pty: boolean
  files: boolean
  watch: boolean
  preview: boolean
  networks: Array<"none" | "full" | "allow" | string>
  requirements: string[]
}

export type CreateSandboxOptions = {
  // A Template builds its steps (once, then from cache) before the create.
  template?: string | Template
  steps?: BuildStep[]
  image?: string
  cpu?: number
  memoryMb?: number
  diskMb?: number
  pids?: number
  ttl?: number | string
  idleTimeout?: number | string
  env?: Record<string, string>
  network?: NetworkPolicy
  backend?: "docker" | "gvisor" | "kata" | "firecracker"
  labels?: Record<string, string>
  mounts?: VolumeMount[]
  secrets?: Record<string, string>
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
  public: boolean
  url: string
  publicUrl?: string
  host?: string
  token?: string
  createdAt?: string
}

export type CreatePreviewOptions = {
  public?: boolean
}

export type CreatedKey = {
  id: string
  name: string
  prefix: string
  scopes: string[]
  key: string
  network: NetworkPolicy
}

/** A filesystem change inside a sandbox. `type` is `create`, `write`, or `remove`. */
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
  egress_allow?: string[]
  egress_deny?: string[]
  egress_kbps?: number
  max_connections?: number
  env?: Record<string, string>
  labels?: Record<string, string>
  secret_keys?: string[]
  idle_timeout_seconds?: number
  expires_at: string
  created_at: string
  node: string
  mounts?: { volume_id: string; path: string }[]
}

type VolumeJSON = {
  id: string
  name: string
  backend: string
  created_at: string
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
    egressAllow: raw.egress_allow,
    egressDeny: raw.egress_deny,
    egressKbps: raw.egress_kbps,
    maxConnections: raw.max_connections,
    env: raw.env,
    labels: raw.labels,
    secretKeys: raw.secret_keys,
    idleTimeoutSeconds: raw.idle_timeout_seconds,
    expiresAt: raw.expires_at,
    createdAt: raw.created_at,
    node: raw.node,
    mounts: raw.mounts?.map((mount) => ({ volumeId: mount.volume_id, path: mount.path })),
  }
}

function mapVolume(raw: VolumeJSON): VolumeInfo {
  return { id: raw.id, name: raw.name, backend: raw.backend, createdAt: raw.created_at }
}

type TemplateJSON = {
  name: string
  image: string
  description?: string
  cpu: number
  memory_mb: number
  disk_mb: number
  env?: Record<string, string>
  backend?: string
  network: string
  setup?: string[]
  setup_timeout_seconds?: number
  warm: number
  steps?: BuildStep[]
  builtin: boolean
  created_at: string
}

type BuildJSON = {
  id: string
  image: string
  base: string
  steps?: BuildStep[]
  cached: boolean
  duration_ms: number
}

function mapBuild(raw: BuildJSON): BuildInfo {
  return {
    id: raw.id,
    image: raw.image,
    base: raw.base,
    steps: raw.steps ?? [],
    cached: raw.cached,
    durationMs: raw.duration_ms,
  }
}

function mapTemplate(raw: TemplateJSON): TemplateInfo {
  return {
    name: raw.name,
    image: raw.image,
    description: raw.description,
    cpu: raw.cpu,
    memoryMb: raw.memory_mb,
    diskMb: raw.disk_mb,
    env: raw.env,
    backend: raw.backend,
    network: raw.network,
    setup: raw.setup ?? [],
    setupTimeoutSeconds: raw.setup_timeout_seconds ?? 0,
    warm: raw.warm,
    steps: raw.steps ?? [],
    builtin: raw.builtin,
    createdAt: raw.created_at,
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

function networkBody(policy: NetworkPolicy): Record<string, unknown> {
  const body: Record<string, unknown> = {}
  if (policy.mode) body.mode = policy.mode
  if (policy.allow != null) body.allow = policy.allow
  if (policy.deny != null) body.deny = policy.deny
  if (policy.egressKbps != null) body.egress_kbps = policy.egressKbps
  if (policy.maxConnections != null) body.max_connections = policy.maxConnections
  return body
}

function commandArgs(cmd: string | string[]): string[] {
  if (typeof cmd === "string") return ["sh", "-lc", cmd]
  if (cmd.length === 0) throw new Error("cmd must not be empty")
  return cmd
}

function createBody(options: CreateSandboxOptions): Record<string, unknown> {
  const body: Record<string, unknown> = {}
  const steps: BuildStep[] = []
  if (options.template instanceof Template) {
    if (options.template.template) body.template = options.template.template
    if (options.template.image) body.image = options.template.image
    steps.push(...options.template.steps)
  } else if (options.template) {
    body.template = options.template
  }
  if (options.image) body.image = options.image
  if (options.steps) steps.push(...options.steps)
  if (steps.length > 0) body.steps = steps
  if (options.cpu != null) body.cpu = options.cpu
  if (options.memoryMb != null) body.memory_mb = options.memoryMb
  if (options.diskMb != null) body.disk_mb = options.diskMb
  if (options.pids != null) body.pids = options.pids
  if (options.ttl != null) body.ttl_seconds = parseDuration(options.ttl)
  if (options.idleTimeout != null) body.idle_timeout_seconds = parseDuration(options.idleTimeout)
  if (options.env) body.env = options.env
  if (options.network) body.network = networkBody(options.network)
  if (options.backend) body.backend = options.backend
  if (options.labels) body.labels = options.labels
  if (options.mounts) {
    body.mounts = options.mounts.map((mount) => ({ volume_id: mount.volumeId, path: mount.path }))
  }
  if (options.secrets) body.secrets = options.secrets
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

  createVolume(name: string, backend?: "docker" | "gvisor" | "kata"): Promise<VolumeInfo> {
    const body: { name: string; backend?: string } = { name }
    if (backend) body.backend = backend
    return this.requestJSON<VolumeJSON>("POST", "/v1/volumes", body).then(mapVolume)
  }

  listVolumes(): Promise<VolumeInfo[]> {
    return this.requestJSON<{ volumes: VolumeJSON[] }>("GET", "/v1/volumes").then((body) => body.volumes.map(mapVolume))
  }

  getVolume(id: string): Promise<VolumeInfo> {
    return this.requestJSON<VolumeJSON>("GET", `/v1/volumes/${encodeURIComponent(id)}`).then(mapVolume)
  }

  deleteVolume(id: string): Promise<void> {
    return this.request("DELETE", `/v1/volumes/${encodeURIComponent(id)}`).then(() => undefined)
  }

  createTemplate(options: CreateTemplateOptions): Promise<TemplateInfo> {
    const body: Record<string, unknown> = { name: options.name, image: options.image }
    if (options.description) body.description = options.description
    if (options.cpu != null) body.cpu = options.cpu
    if (options.memoryMb != null) body.memory_mb = options.memoryMb
    if (options.diskMb != null) body.disk_mb = options.diskMb
    if (options.env) body.env = options.env
    if (options.backend) body.backend = options.backend
    if (options.network) body.network = options.network
    if (options.setup) body.setup = options.setup
    if (options.setupTimeout != null) body.setup_timeout_seconds = parseDuration(options.setupTimeout)
    if (options.warm != null) body.warm = options.warm
    if (options.steps) body.steps = options.steps
    return this.requestJSON<TemplateJSON>("POST", "/v1/templates", body).then(mapTemplate)
  }

  // build builds and caches an image from a base and steps. A create with
  // the same base and steps then starts from the cached image.
  build(options: { image?: string; template?: string; steps: BuildStep[]; backend?: string }): Promise<BuildInfo> {
    const body: Record<string, unknown> = { steps: options.steps }
    if (options.image) body.image = options.image
    if (options.template) body.template = options.template
    if (options.backend) body.backend = options.backend
    return this.requestJSON<BuildJSON>("POST", "/v1/builds", body).then(mapBuild)
  }

  listTemplates(): Promise<TemplateInfo[]> {
    return this.requestJSON<{ templates: TemplateJSON[] }>("GET", "/v1/templates").then((body) => body.templates.map(mapTemplate))
  }

  deleteTemplate(name: string): Promise<void> {
    return this.request("DELETE", `/v1/templates/${encodeURIComponent(name)}`).then(() => undefined)
  }

  createSandbox(options: CreateSandboxOptions = {}): Promise<SandboxInfo> {
    return this.requestJSON<SandboxJSON>("POST", "/v1/sandboxes", createBody(options)).then(mapSandbox)
  }

  createKey(name: string, options: { scopes?: string[]; network?: NetworkPolicy } = {}): Promise<CreatedKey> {
    const body: Record<string, unknown> = { name }
    if (options.scopes) body.scopes = options.scopes
    if (options.network) body.network = networkBody(options.network)
    return this.requestJSON<CreatedKeyJSON>("POST", "/v1/keys", body).then(mapKey)
  }

  getSandbox(id: string): Promise<SandboxInfo> {
    return this.requestJSON<SandboxJSON>("GET", `/v1/sandboxes/${encodeURIComponent(id)}`).then(mapSandbox)
  }

  listBackends(): Promise<BackendInfo[]> {
    return this.requestJSON<{ backends: BackendInfo[] }>("GET", "/v1/backends").then((body) => body.backends)
  }

  listSandboxes(query: { status?: string; backend?: string; template?: string; q?: string; labels?: Record<string, string> } = {}): Promise<SandboxInfo[]> {
    const params = new URLSearchParams()
    if (query.status) params.set("status", query.status)
    if (query.backend) params.set("backend", query.backend)
    if (query.template) params.set("template", query.template)
    if (query.q) params.set("q", query.q)
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

  setSecrets(id: string, secrets: Record<string, string>): Promise<SandboxInfo> {
    return this.requestJSON<SandboxJSON>("PUT", `/v1/sandboxes/${encodeURIComponent(id)}/secrets`, { secrets }).then(mapSandbox)
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

  /** Upload a tar or gzip tar. With sbx-agent, extraction does not use a tar binary. */
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

  /** Stream create, write, and remove events. The agent uses inotify and polls if inotify is unavailable. */
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

  createPreview(id: string, port: number, options: CreatePreviewOptions = {}): Promise<PreviewInfo> {
    const body: Record<string, unknown> = { port }
    if (options.public) body.public = true
    return this.requestJSON<PreviewJSON>("POST", `/v1/sandboxes/${encodeURIComponent(id)}/previews`, body).then(mapPreview)
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

  /** Download a directory as gzip tar. With sbx-agent, packing does not use a tar binary. */
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
  public?: boolean
  url: string
  public_url?: string
  host?: string
  token?: string
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

type CreatedKeyJSON = {
  id: string
  name: string
  prefix: string
  scopes?: string[]
  key: string
  network?: {
    mode?: NetworkMode
    allow?: string[]
    deny?: string[]
    egress_kbps?: number
    max_connections?: number
  }
}

function mapKey(raw: CreatedKeyJSON): CreatedKey {
  const network = raw.network ?? {}
  return {
    id: raw.id,
    name: raw.name,
    prefix: raw.prefix,
    scopes: raw.scopes ?? [],
    key: raw.key,
    network: {
      mode: network.mode,
      allow: network.allow,
      deny: network.deny,
      egressKbps: network.egress_kbps,
      maxConnections: network.max_connections,
    },
  }
}

function mapPreview(raw: PreviewJSON): PreviewInfo {
  return {
    id: raw.id,
    sandboxId: raw.sandbox_id,
    port: raw.port,
    public: Boolean(raw.public),
    url: raw.url,
    publicUrl: raw.public_url,
    host: raw.host,
    token: raw.token,
    createdAt: raw.created_at,
  }
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
  secretKeys?: string[]
  idleTimeoutSeconds?: number
  expiresAt: string
  createdAt: string
  node: string
  mounts?: VolumeMount[]
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
    this.secretKeys = info.secretKeys
    this.idleTimeoutSeconds = info.idleTimeoutSeconds
    this.expiresAt = info.expiresAt
    this.createdAt = info.createdAt
    this.node = info.node
    this.mounts = info.mounts
    this.files = new SandboxFiles(client, info.id)
  }

  static async create(options?: CreateSandboxOptions & ConnectionOptions): Promise<Sandbox>
  static async create(template: Template, options?: Omit<CreateSandboxOptions, "template"> & ConnectionOptions): Promise<Sandbox>
  static async create(
    first: Template | (CreateSandboxOptions & ConnectionOptions) = {},
    rest: Omit<CreateSandboxOptions, "template"> & ConnectionOptions = {},
  ): Promise<Sandbox> {
    const options = first instanceof Template ? { ...rest, template: first } : first
    const client = clientFrom(options)
    return new Sandbox(client, await client.createSandbox(options))
  }

  // template starts an image builder. A string is a base image. Without an
  // argument the builder starts from the built-in "base" template.
  static template(base?: string | { image?: string; template?: string }): Template {
    if (typeof base === "string") return new Template({ image: base })
    if (base?.image || base?.template) return new Template(base)
    return new Template({ template: "base" })
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

  createPreview(port: number, options?: CreatePreviewOptions): Promise<PreviewInfo> {
    return this.#client.createPreview(this.id, port, options)
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

// Template is an immutable list of build steps on a base image or template.
// Every method returns a new Template, so one base can branch into variants.
// sbx builds the steps once, caches the image by content hash, and starts
// later sandboxes from that image. env and workdir apply to later steps only;
// they are not set in sandboxes. /workspace is mounted over at runtime, so
// build into another directory such as /app.
export class Template {
  readonly image?: string
  readonly template?: string
  readonly steps: readonly BuildStep[]

  constructor(base: { image?: string; template?: string }, steps: readonly BuildStep[] = []) {
    this.image = base.image
    this.template = base.template
    this.steps = Object.freeze([...steps])
  }

  // run adds a shell command, run with /bin/sh -c as root.
  run(command: string): Template {
    if (!command) throw new Error("command must not be empty")
    return this.#with({ run: command })
  }

  // withPackages installs system packages with apt-get, apk, dnf, or yum,
  // whichever the base image has.
  withPackages(...names: string[]): Template {
    if (names.length === 0) throw new Error("withPackages needs at least one package")
    return this.#with({ packages: names })
  }

  // withEnv sets build-time variables for later steps. They are part of the
  // content hash, so a change rebuilds. Pass runtime variables to create.
  withEnv(vars: Record<string, string>): Template {
    if (Object.keys(vars).length === 0) throw new Error("withEnv needs at least one variable")
    return this.#with({ env: { ...vars } })
  }

  // workdir sets the working directory for later steps.
  workdir(dir: string): Template {
    return this.#with({ workdir: dir })
  }

  // build builds and caches the image ahead of time. Sandbox.create builds
  // on its own, so this only warms the cache.
  build(options: BuildOptions & ConnectionOptions = {}): Promise<BuildInfo> {
    return clientFrom(options).build({ image: this.image, template: this.template, steps: [...this.steps], backend: options.backend })
  }

  #with(step: BuildStep): Template {
    return new Template({ image: this.image, template: this.template }, [...this.steps, step])
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
