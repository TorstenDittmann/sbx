<script lang="ts">
	import Seo from "../lib/components/Seo.svelte";
	import Sandbox from "../lib/components/illustrations/Sandbox.svelte";
	import Templates from "../lib/components/illustrations/Templates.svelte";
	import Previews from "../lib/components/illustrations/Previews.svelte";
	import Backends from "../lib/components/illustrations/Backends.svelte";
	import Volumes from "../lib/components/illustrations/Volumes.svelte";
	import Network from "../lib/components/illustrations/Network.svelte";

	let { data } = $props();

	const concepts = [
		{
			title: "Closed by default",
			text: "A sandbox starts with no network, no Linux capabilities, uid 1000, a read-only root filesystem, and a TTL the server enforces.",
			href: "/docs/sandboxes/",
			link: "Sandboxes",
			art: Sandbox,
		},
		{
			title: "Templates with warm pools",
			text: "Bake dependencies into a template with cached build steps, and keep a few sandboxes warm so create returns one right away.",
			href: "/docs/templates/",
			link: "Templates and builds",
			art: Templates,
		},
		{
			title: "Previews for what runs inside",
			text: "Publish a port as a URL, public or behind a token. The proxy reaches it through the agent, so it works without network.",
			href: "/docs/previews/",
			link: "Previews",
			art: Previews,
		},
		{
			title: "One API, four backends",
			text: "Start on Docker. Move to gVisor, Kata, or Firecracker microVMs for stronger isolation without changing a call.",
			href: "/docs/backends/",
			link: "Backends",
			art: Backends,
		},
		{
			title: "Network you opt into",
			text: "Choose none, full, or an allow list. Cloud metadata and private ranges stay blocked either way.",
			href: "/docs/network/",
			link: "Network",
			art: Network,
		},
		{
			title: "Volumes that outlive a sandbox",
			text: "Mount a named volume at /workspace and pick up where the last sandbox stopped.",
			href: "/docs/volumes/",
			link: "Volumes",
			art: Volumes,
		},
	];

	const steps = [
		"Your code calls the API with a key, through the SDK or plain HTTP.",
		"sbx checks the key's quota and network policy, then starts a sandbox from an image or template.",
		"Exec, files, terminals, and previews go through sbx-agent inside the sandbox.",
		"When the TTL runs out, the reaper deletes the sandbox.",
	];

	const next = [
		{ href: "/docs/quickstart/", title: "Quickstart", text: "Start the server and run your first command." },
		{ href: "/docs/installation/", title: "Installation", text: "Release binaries, the container image, and the npm package." },
		{ href: "/docs/sdk/", title: "TypeScript SDK", text: "The client for Node and Bun." },
		{ href: "/docs/api/", title: "HTTP API", text: "Every route, its body, and its errors." },
	];
</script>

<Seo title="sbx: sandboxes on your own machine" description={data.site.description} />

<section class="container hero">
	<div class="intro">
		<h1>Run untrusted code on your own machine.</h1>
		<p>One Go binary. Create a sandbox, run a command, and use the same API on Docker, gVisor, Kata, or Firecracker.</p>
		<div class="actions">
			<a class="button" href="/docs/quickstart/">Get started</a>
			<a class="quiet" href="/docs/">Read the docs</a>
			<a class="quiet" href={data.site.repo}>GitHub</a>
		</div>
	</div>
	<div class="code">{@html data.sdk}</div>
</section>

<section class="container how" id="how">
	<h2>How it fits together</h2>
	<p class="sub">sbx sits between your code and the machine. It holds the keys, the state, and the clock, and every sandbox carries a small agent that does the work inside.</p>

	<div class="flow" role="img" aria-label="Your code calls sbx over HTTP. sbx starts sandboxes on a backend, each with sbx-agent inside.">
		<div class="node">
			<span class="label">your code</span>
			<ul>
				<li><code>Sandbox.create()</code></li>
				<li><code>POST /v1/sandboxes</code></li>
			</ul>
			<span class="foot">TypeScript SDK, curl, an AI agent</span>
		</div>

		<div class="link" aria-hidden="true">
			<span>HTTP + key</span>
			<svg viewBox="0 0 48 10"><path d="M0 5 H42" /><path class="tip" d="M48 5 l-7 -4 v8 Z" /><circle class="dot" cx="0" cy="5" r="2.5" /></svg>
		</div>

		<div class="node server">
			<span class="label">sbx</span>
			<ul>
				<li>API and API keys</li>
				<li>SQLite state</li>
				<li>TTL reaper</li>
				<li>preview proxy</li>
				<li>template builds</li>
			</ul>
			<span class="foot">one Go binary</span>
		</div>

		<div class="link" aria-hidden="true">
			<span>start, exec</span>
			<svg viewBox="0 0 48 10"><path d="M0 5 H42" /><path class="tip" d="M48 5 l-7 -4 v8 Z" /><circle class="dot" cx="0" cy="5" r="2.5" /></svg>
		</div>

		<div class="node backend">
			<span class="label">backend</span>
			<div class="backends">
				<span class="on">docker</span>
				<span>gvisor</span>
				<span>kata</span>
				<span>firecracker</span>
			</div>
			<div class="boxes">
				{#each ["sbx_3f2a", "sbx_8c1d"] as id}
					<div class="box">
						<span class="id">{id}</span>
						<span class="agent">sbx-agent</span>
						<span class="ops">exec · files · pty · ports</span>
					</div>
				{/each}
			</div>
		</div>
	</div>

	<ol class="steps">
		{#each steps as step, i}
			<li><span class="n">{i + 1}</span>{step}</li>
		{/each}
	</ol>
	<p class="more"><a href="/docs/">Read the introduction →</a></p>
</section>

<section class="container concepts">
	{#each concepts as concept}
		{@const Art = concept.art}
		<article>
			<div class="art"><Art /></div>
			<h3>{concept.title}</h3>
			<p>{concept.text}</p>
			<a href={concept.href}>{concept.link} →</a>
		</article>
	{/each}
</section>

<section class="container start" id="start">
	<div class="intro">
		<h2>Start in a minute</h2>
		<p>Install the binary, start the server, and add the SDK. The first boot prints an admin key. Docker has to be running.</p>
		<ul class="next">
			{#each next as item}
				<li>
					<a href={item.href}>
						<span class="title">{item.title}</span>
						<span class="text">{item.text}</span>
					</a>
				</li>
			{/each}
		</ul>
	</div>
	<div class="code">{@html data.install}</div>
</section>

<style>
	.hero,
	.start {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 56px;
		align-items: center;
	}

	.hero {
		padding-block: 80px 72px;
	}

	.hero > *,
	.start > * {
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

	h2 {
		margin: 0 0 12px;
		font-size: 24px;
		font-weight: 500;
		letter-spacing: -0.03em;
	}

	.intro p,
	.sub {
		margin: 0 0 16px;
		max-width: 46ch;
		color: var(--dim);
	}

	.sub {
		max-width: 62ch;
		margin-bottom: 36px;
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

	/* How it fits together */

	.how {
		padding-block: 72px 0;
	}

	.flow {
		display: grid;
		grid-template-columns: 1fr 72px 1fr 72px 1.35fr;
		align-items: center;
	}

	.node {
		display: flex;
		flex-direction: column;
		gap: 12px;
		border: 1px solid var(--edge);
		border-radius: 10px;
		padding: 16px;
		min-width: 0;
		font-size: 13px;
	}

	.label {
		font-family: var(--mono);
		font-size: 12px;
		color: var(--dim);
	}

	.server {
		background: var(--code);
	}

	.server .label {
		color: var(--fg);
		font-size: 15px;
		font-weight: 500;
	}

	.node ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: 6px;
	}

	.node li {
		border: 1px solid var(--line);
		border-radius: 5px;
		background: var(--bg);
		padding: 4px 8px;
	}

	.node li code {
		font-size: 12px;
	}

	.foot {
		font-size: 12px;
		color: var(--dim);
	}

	.link {
		display: grid;
		justify-items: center;
		gap: 4px;
		padding-inline: 8px;
		font-family: var(--mono);
		font-size: 11px;
		color: var(--dim);
		text-align: center;
	}

	.link svg {
		width: 100%;
		height: 10px;
		overflow: visible;
	}

	.link path {
		fill: none;
		stroke: var(--dim);
		stroke-width: 1.25;
		vector-effect: non-scaling-stroke;
	}

	.link path.tip {
		fill: var(--dim);
		stroke: none;
	}

	/* A request crosses each arrow in turn. */
	.link .dot {
		fill: var(--accent);
		opacity: 0;
		animation: travel 3s ease-in-out infinite;
	}

	.link + .server + .link .dot {
		animation-delay: 1.2s;
	}

	@keyframes travel {
		0% {
			opacity: 0;
			transform: translateX(0);
		}
		8% {
			opacity: 1;
		}
		32% {
			opacity: 1;
		}
		40%,
		100% {
			opacity: 0;
			transform: translateX(40px);
		}
	}

	.backends {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		font-family: var(--mono);
		font-size: 11px;
	}

	.backends span {
		border: 1px solid var(--line);
		border-radius: 99px;
		padding: 1px 8px;
		color: var(--dim);
	}

	.backends .on {
		border-color: var(--accent);
		color: var(--accent);
	}

	.boxes {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 8px;
	}

	.box {
		display: grid;
		gap: 6px;
		border: 1px solid color-mix(in srgb, var(--accent) 55%, var(--bg));
		background: color-mix(in srgb, var(--accent) 7%, var(--bg));
		border-radius: 8px;
		padding: 10px;
		min-width: 0;
	}

	.box .id {
		font-family: var(--mono);
		font-size: 12px;
		color: var(--accent);
	}

	.box .agent {
		justify-self: start;
		font-family: var(--mono);
		font-size: 11px;
		border: 1px solid var(--edge);
		border-radius: 4px;
		background: var(--bg);
		padding: 1px 6px;
	}

	.box .ops {
		font-size: 11px;
		color: var(--dim);
	}

	.steps {
		list-style: none;
		margin: 40px 0 0;
		padding: 0;
		display: grid;
		grid-template-columns: repeat(4, 1fr);
		gap: 24px;
		font-size: 14px;
		color: color-mix(in srgb, var(--fg) 78%, var(--bg));
	}

	.steps li {
		display: grid;
		gap: 10px;
		align-content: start;
		border-top: 1px solid var(--line);
		padding-top: 14px;
	}

	.n {
		display: grid;
		place-items: center;
		width: 22px;
		height: 22px;
		border-radius: 50%;
		background: color-mix(in srgb, var(--accent) 12%, var(--bg));
		color: var(--accent);
		font-family: var(--mono);
		font-size: 12px;
	}

	.more {
		margin: 28px 0 0;
		font-size: 14px;
	}

	.more a,
	.concepts a {
		color: var(--accent);
	}

	.more a:hover,
	.concepts a:hover {
		text-decoration: underline;
		text-underline-offset: 3px;
	}

	/* Concepts */

	.concepts {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 64px 48px;
		padding-block: 96px 0;
	}

	.concepts article {
		min-width: 0;
	}

	.art {
		border: 1px solid var(--line);
		border-radius: 12px;
		padding: 20px;
		margin-bottom: 20px;
	}

	h3 {
		margin: 0 0 6px;
		font-size: 17px;
		font-weight: 500;
		letter-spacing: -0.01em;
	}

	.concepts p {
		margin: 0 0 10px;
		color: var(--dim);
		max-width: 52ch;
	}

	.concepts a {
		font-size: 14px;
	}

	/* Start */

	.start {
		padding-block: 112px 88px;
	}

	.next {
		list-style: none;
		margin: 24px 0 0;
		padding: 0;
		border-top: 1px solid var(--line);
	}

	.next a {
		display: grid;
		gap: 2px;
		padding: 12px 0;
		border-bottom: 1px solid var(--line);
	}

	.next .title {
		font-size: 15px;
		font-weight: 500;
	}

	.next .text {
		font-size: 13px;
		color: var(--dim);
	}

	.next a:hover .title {
		color: var(--accent);
	}

	@media (max-width: 760px) {
		.hero,
		.start {
			grid-template-columns: 1fr;
			gap: 36px;
		}

		.hero {
			padding-block: 48px;
		}

		.how {
			padding-top: 40px;
		}

		.flow {
			grid-template-columns: 1fr;
		}

		.link {
			grid-template-columns: 10px auto;
			justify-content: center;
			align-items: center;
			gap: 10px;
			padding-block: 10px;
		}

		.link span {
			order: 2;
		}

		.link svg {
			width: 32px;
			height: 10px;
			transform: rotate(90deg);
			justify-self: center;
		}

		.steps {
			grid-template-columns: 1fr 1fr;
		}

		.concepts {
			grid-template-columns: 1fr;
			gap: 48px;
			padding-top: 72px;
		}

		.art {
			padding: 12px;
		}

		.start {
			padding-block: 72px 56px;
		}
	}

	@media (max-width: 440px) {
		.steps {
			grid-template-columns: 1fr;
		}
	}
</style>
