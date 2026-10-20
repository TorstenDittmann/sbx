<script lang="ts">
	import Seo from "../lib/components/Seo.svelte";

	let { data } = $props();

	const backends = [
		{
			name: "docker",
			isolation: "Namespaces and cgroups.",
			host: "Docker",
		},
		{
			name: "gvisor",
			isolation: "The Docker driver with runsc. A user-space kernel, no KVM.",
			host: "Docker with runsc",
		},
		{
			name: "kata",
			isolation: "The Docker driver with the Kata runtime class. Same image and network policy.",
			host: "Kata and /dev/kvm",
		},
		{
			name: "firecracker",
			isolation: "A microVM per sandbox. OCI image to ext4, sbx-agent as init over vsock.",
			host: "/dev/kvm, a kernel, and sbx-agent",
		},
		{
			name: "kubernetes",
			isolation: "No driver.",
			host: "—",
		},
	];

	const routes = [
		{ method: "POST", path: "/v1/sandboxes", detail: "create" },
		{ method: "GET", path: "/v1/sandboxes", detail: "status, backend, template, label, and q" },
		{ method: "POST", path: "/v1/sandboxes/{id}/exec", detail: "wait, or SSE with ?stream=1" },
		{ method: "POST", path: "/v1/sandboxes/{id}/processes", detail: "background command" },
		{ method: "POST", path: "/v1/sandboxes/{id}/terminals", detail: "PTY, then a WebSocket" },
		{ method: "GET", path: "/v1/sandboxes/{id}/files/watch", detail: "inotify, or a poll every 200 ms" },
		{ method: "POST", path: "/v1/sandboxes/{id}/files/archive", detail: "extract a tar or gzip tar" },
		{ method: "GET", path: "/v1/sandboxes/{id}/files/archive", detail: "download a gzip tar" },
		{ method: "PUT", path: "/v1/sandboxes/{id}/secrets", detail: "replace secrets; values stay off the response" },
		{ method: "POST", path: "/v1/sandboxes/{id}/previews", detail: "a port on the sandbox loopback" },
		{ method: "POST", path: "/v1/volumes", detail: "a Docker volume on Docker, gVisor, or Kata" },
		{ method: "GET", path: "/v1/backends", detail: "which backends this host can run" },
		{ method: "GET", path: "/readyz", detail: "503 until startup reconcile finishes" },
		{ method: "GET", path: "/healthz", detail: "liveness; pings the configured backend" },
		{ method: "GET", path: "/openapi.yaml", detail: "the contract" },
	];
</script>

<Seo title="sbx: sandboxes on your own machine" description={data.site.description} />

<section class="container hero">
	<div class="intro">
		<h1>Run untrusted code on your own machine.</h1>
		<p>One Go binary. Create a sandbox, run a command, and use the same API on Docker, gVisor, Kata, or Firecracker.</p>
		<div class="actions">
			<a class="button" href="#quickstart">Quickstart</a>
			<a class="quiet" href={data.site.repo}>GitHub</a>
		</div>
	</div>
	<div class="code">{@html data.sdk}</div>
</section>

<section class="container">
	<div class="features">
		<div>
			<h2>One binary</h2>
			<p>A Go process, a SQLite file, and the Docker socket.</p>
		</div>
		<div>
			<h2>Closed by default</h2>
			<p>No network, no capabilities, uid 1000, a read-only rootfs, and a server-side TTL.</p>
		</div>
		<div>
			<h2>Agent inside</h2>
			<p>Streaming exec, a PTY, inotify watches, archives, and preview ports.</p>
		</div>
		<div>
			<h2>Opt-in network</h2>
			<p><code>none</code>, <code>full</code>, or an allow list. Public preview hosts when a domain is set.</p>
		</div>
		<div>
			<h2>Kept across sandboxes</h2>
			<p>Named volumes outlive a sandbox. A template can hold a warm pool.</p>
		</div>
		<div>
			<h2>Checked on the host</h2>
			<p><code>GET /v1/backends</code> and <code>sbx doctor</code> report what this machine can run.</p>
		</div>
	</div>
</section>

<section class="container compare" id="backends">
	<h2>Choose a backend</h2>
	<p class="sub">Set it for the server, or pass <code>backend</code> on a template or a sandbox. Omit it and the sandbox uses Docker. Docker, gVisor, Kata, and Firecracker share the calls. <code>GET /v1/backends</code> and <code>sbx doctor</code> report which of them this host can run. A missing runtime is an error. There is no Kubernetes driver.</p>
	<div class="table">
		<table>
			<colgroup>
				<col class="name" />
				<col />
				<col class="needs" />
			</colgroup>
			<thead>
				<tr>
					<th scope="col">Backend</th>
					<th scope="col">Isolation</th>
					<th scope="col">Needs</th>
				</tr>
			</thead>
			<tbody>
				{#each backends as row}
					<tr>
						<th scope="row">{row.name}</th>
						<td data-label="Isolation">{row.isolation}</td>
						<td data-label="Needs">{row.host}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
</section>

<section class="container setup" id="quickstart">
	<div class="intro">
		<h2>Build both binaries, then a request</h2>
		<p>Docker has to be running. Build <code>sbx-agent</code> next to <code>sbx</code> so the agent starts inside the sandbox. The first boot prints an admin key once and writes it to <code>data/initial_api_key</code>.</p>
		<p class="note">Templates are <code>base</code>, <code>python</code>, and <code>node</code>. A template can keep up to 8 warm sandboxes and a setup command. Create takes one when it does not change image, resources, backend, network policy, env, secrets, or mounts. Otherwise sbx starts a sandbox and runs setup then. Warm sandboxes stay off the normal list unless an admin asks for <code>status=warm</code>. The SDK reads <code>SBX_URL</code> and <code>SBX_API_KEY</code>. From this repo, <code>node --experimental-strip-types examples/quickstart.ts</code> runs on Node, and the same client runs on Bun.</p>
	</div>
	<div class="code">{@html data.quickstart}</div>
</section>

<section class="container setup" id="install">
	<div class="intro">
		<h2>Install a release</h2>
		<p>A version tag publishes the binary, <code>ghcr.io/torstendittmann/sbx</code>, and <code>@sbx.torsten.work/sbx</code>. The npm name <code>sbx</code> is a different project.</p>
		<p class="note">Linux installs <code>sbx</code> and <code>sbx-agent</code>. macOS installs a darwin server and a linux agent, because the agent runs in the container. The image includes both and sets <code>SBX_AGENT_BIN</code>.</p>
	</div>
	<div class="code">{@html data.install}</div>
</section>

<section class="container setup" id="sdk">
	<div class="intro">
		<h2>The same client</h2>
		<p><code>SbxClient</code> and <code>Sandbox.create</code> read <code>SBX_URL</code> (default <code>http://127.0.0.1:8080</code>) and <code>SBX_API_KEY</code>. <code>listBackends</code> reports the host. <code>createVolume</code> takes <code>docker</code>, <code>gvisor</code>, or <code>kata</code>.</p>
		<p class="note"><code>box.secretKeys</code> lists names only. <code>client.setSecrets</code> replaces the map. <code>client.listSandboxes</code> takes <code>template</code>, <code>q</code>, and exact <code>labels</code>. <code>client.uploadArchive</code> accepts a tar or gzip tar, and <code>client.downloadArchive</code> returns a gzip tar. <code>box.files.watch</code> yields <code>create</code>, <code>write</code>, and <code>remove</code>. <code>box.createPreview</code> returns <code>url</code>, <code>publicUrl</code>, and a token shown once. <code>client.createTemplate</code> takes <code>warm</code> and <code>setup</code>. Its <code>network</code> is <code>none</code> or <code>full</code>.</p>
	</div>
	<div class="code">{@html data.client}</div>
</section>

<section class="container api" id="api">
	<h2>The calls you make</h2>
	<p class="sub">Send <code>Authorization: Bearer sbx_…</code>. Keys are stored hashed. A sandbox key only sees sandboxes it created. <code>secrets</code> are injected into the sandbox and into later commands. Get and list return <code>secret_keys</code> and never the values, and a secret name cannot also be set in <code>env</code>. List filters combine exact <code>status</code>, <code>backend</code>, <code>template</code>, and <code>label=key:value</code> with <code>q</code>, a case-insensitive substring of the id, template, image, status, and label text, at most 128 characters. Env values are not searched. Processes, terminals, watches, archives, and previews need <code>sbx-agent</code>. Watches use inotify and poll every 200 ms when inotify cannot be created. Without the agent those routes return <code>unsupported</code>, and exec, files, and archives use a shell in the image, including <code>tar</code>. Pause and resume are <code>POST …/pause</code> and <code>POST …/resume</code>.</p>
	<div class="table">
		<table>
			<tbody>
				{#each routes as route}
					<tr>
						<th scope="row">{route.method}</th>
						<td><code>{route.path}</code></td>
						<td data-label="">{route.detail}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
</section>

<section class="container limits">
	<h2>Limits</h2>
	<ul>
		<li><code>network: none</code> is the default. <code>full</code> and <code>allow</code> reach the public internet, with DNS, allow and deny lists, and a connection limit. Docker, gVisor, and Kata also enforce per-sandbox bandwidth. Firecracker does not: a positive <code>egress_kbps</code> is rejected, and the guest NIC is IPv4. Metadata and private ranges stay blocked unless the operator opts out. Previews work with <code>network: none</code>. They can be public or token-protected, on <code>{'/p/{id}/{port}/'}</code> and, with <code>preview_domain</code>, on <code>{'{port}-sbx-{id}.{domain}'}</code>.</li>
		<li>On Docker, gVisor, and Kata, <code>disk_mb</code> is the writable <code>/workspace</code> tmpfs. A volume at <code>/workspace</code> replaces that tmpfs, and <code>disk_mb</code> does not cap the volume. Firecracker sizes an ext4 rootfs to <code>disk_mb</code> and does not attach volumes. The container root filesystem is read-only, memory has no swap, and a PID limit applies. Firecracker does not set a guest PID rlimit.</li>
		<li>On startup the server adopts sandboxes that are still running and marks missing ones stopped. <code>/readyz</code> stays 503 until that reconcile finishes. <code>/healthz</code> is liveness and pings the configured backend. <code>max_sandboxes</code> defaults to 100, on top of 20 sandboxes per key. The agent packs and extracts archives, and rejects a path that leaves the destination or passes through a symlink. The server pauses and resumes, keeps templates and quotas, and serves <code>/metrics</code>. It does not snapshot, restore, or schedule across nodes.</li>
	</ul>
</section>

<style>
	.hero,
	.setup {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 56px;
		align-items: center;
	}

	.hero {
		padding-block: 80px 72px;
	}

	.setup {
		padding-block: 72px 0;
	}

	.hero > *,
	.setup > * {
		min-width: 0;
	}

	h1 {
		margin: 0 0 16px;
		font-size: clamp(34px, 5vw, 44px);
		font-weight: 500;
		line-height: 1.08;
		letter-spacing: -0.04em;
		text-wrap: balance;
	}

	.intro p,
	.sub,
	.note {
		margin: 0 0 16px;
		max-width: 46ch;
		color: var(--dim);
	}

	.intro p:last-child,
	.note {
		margin-bottom: 0;
	}

	.note {
		font-size: 14px;
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 12px 20px;
		margin-top: 28px;
	}

	.button {
		background: var(--fg);
		color: var(--bg);
		padding: 9px 16px;
		border-radius: 6px;
		font-size: 14px;
		font-weight: 500;
	}

	.button:hover {
		opacity: 0.86;
	}

	.quiet {
		font-size: 14px;
		color: var(--dim);
	}

	.quiet:hover {
		color: var(--fg);
	}

	.compare,
	.api {
		padding-block: 88px 0;
	}

	.compare h2,
	.setup h2,
	.api h2,
	.limits h2 {
		margin: 0 0 12px;
		font-size: 24px;
		font-weight: 500;
		letter-spacing: -0.03em;
	}

	.sub {
		margin: 0 0 32px;
		max-width: 62ch;
	}

	.sub code,
	.intro code,
	.features code,
	.limits code {
		font-size: 0.86em;
		background: var(--code);
		border: 1px solid var(--line);
		border-radius: 4px;
		padding: 1px 5px;
	}

	table {
		width: 100%;
		border-collapse: collapse;
		font-size: 15px;
		line-height: 1.5;
	}

	.compare table {
		table-layout: fixed;
	}

	col.name {
		width: 18%;
	}

	col.needs {
		width: 32%;
	}

	th,
	td {
		text-align: left;
		vertical-align: top;
		padding: 16px 20px;
		border-bottom: 1px solid var(--line);
		text-wrap: pretty;
	}

	.compare th:first-child,
	.compare td:first-child,
	.api th {
		padding-left: 0;
	}

	thead th {
		padding-block: 12px;
		font-size: 13px;
		font-weight: 500;
		color: var(--dim);
		border-bottom-color: var(--fg);
	}

	tbody th {
		font-weight: 500;
		color: var(--fg);
		font-family: var(--mono);
		font-size: 13px;
	}

	td {
		color: color-mix(in srgb, var(--fg) 72%, var(--bg));
	}

	.compare td:last-child,
	.compare thead th:last-child {
		background: color-mix(in srgb, var(--accent) 9%, var(--bg));
		color: var(--fg);
		font-weight: 500;
	}

	.compare thead th:last-child {
		color: var(--accent);
		border-radius: 8px 8px 0 0;
	}

	.compare tbody tr:last-child td:last-child {
		border-radius: 0 0 8px 8px;
	}

	tbody tr:last-child > * {
		border-bottom: 0;
	}

	.api th {
		width: 4.5rem;
		padding-right: 12px;
	}

	.api td code {
		font-size: 13px;
	}

	.api td:last-child {
		color: var(--dim);
		font-weight: 400;
	}

	.features {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		border-block: 1px solid var(--line);
	}

	.features div {
		padding: 28px 24px 28px 0;
		min-width: 0;
	}

	.features div:not(:nth-child(3n + 1)) {
		padding-left: 24px;
		border-left: 1px solid var(--line);
	}

	@media (min-width: 761px) {
		.features div:nth-child(n + 4) {
			border-top: 1px solid var(--line);
		}
	}

	.features h2 {
		margin: 0 0 6px;
		font-size: 16px;
		font-weight: 500;
	}

	.features p {
		margin: 0;
		font-size: 14px;
		color: var(--dim);
	}

	.limits {
		padding-block: 88px 72px;
	}

	.limits ul {
		list-style: none;
		margin: 8px 0 0;
		padding: 0;
		max-width: 68ch;
	}

	.limits li {
		padding: 14px 0;
		border-bottom: 1px solid var(--line);
		color: color-mix(in srgb, var(--fg) 82%, var(--bg));
	}

	.table {
		overflow-x: auto;
	}

	@media (max-width: 760px) {
		.hero,
		.setup {
			grid-template-columns: 1fr;
			gap: 36px;
		}

		.hero {
			padding-block: 48px;
		}

		.setup {
			padding-block: 48px 0;
		}

		.compare,
		.api {
			padding-block: 64px 0;
		}

		.limits {
			padding-block: 64px 48px;
		}

		.compare table,
		.compare tbody,
		.compare tr,
		.compare th,
		.compare td {
			display: block;
		}

		.compare colgroup,
		.compare thead {
			display: none;
		}

		.compare tbody {
			display: grid;
			gap: 12px;
		}

		.compare tbody tr {
			border: 1px solid var(--line);
			border-radius: 10px;
			overflow: hidden;
		}

		.compare tbody th,
		.compare tbody td,
		.compare tbody tr:last-child > *,
		.compare th:first-child {
			padding: 10px 16px;
			border-bottom: 1px solid var(--line);
		}

		.compare tbody th {
			padding-block: 12px;
			font-size: 16px;
		}

		.compare td:last-child,
		.compare thead th:last-child,
		.compare tbody tr:last-child td:last-child {
			border-radius: 0;
		}

		.compare td:last-child::before {
			color: var(--accent);
		}

		.compare td::before {
			content: attr(data-label);
			display: block;
			font-size: 12px;
			font-weight: 500;
			color: var(--dim);
		}

		.compare td:last-child,
		.compare tbody tr:last-child td:last-child {
			border-bottom: 0;
		}

		.api th,
		.api td {
			padding-left: 0;
		}

		.features {
			grid-template-columns: 1fr;
		}

		.features div,
		.features div:not(:nth-child(3n + 1)) {
			padding: 22px 0;
			border-left: 0;
		}

		.features div + div {
			border-top: 1px solid var(--line);
		}
	}
</style>
