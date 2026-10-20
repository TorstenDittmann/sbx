<script lang="ts">
	import "@fontsource-variable/geist";
	import "@fontsource-variable/geist-mono";
	import "../app.css";
	import { onMount } from "svelte";
	import { page } from "$app/state";

	let { data, children } = $props();

	// Docs pages use a wider grid, so the header and footer follow it.
	const wide = $derived(page.url.pathname.startsWith("/docs"));

	// Code blocks are server-rendered HTML, so their copy buttons are handled by delegation.
	onMount(() => {
		async function copy(event: MouseEvent) {
			const button = (event.target as Element).closest<HTMLButtonElement>("button.copy");
			if (!button) return;
			try {
				await navigator.clipboard.writeText(button.dataset.copy ?? "");
			} catch {
				return;
			}
			button.dataset.copied = "";
			button.ariaLabel = "Copied";
			setTimeout(() => {
				delete button.dataset.copied;
				button.ariaLabel = "Copy code";
			}, 1500);
		}
		document.addEventListener("click", copy);
		return () => document.removeEventListener("click", copy);
	});
</script>

<header class="container" class:wide>
	<nav aria-label="Main">
		<a class="logo" href="/">sbx</a>
		<a href="/docs/">Docs</a>
		<a href="/docs/quickstart/">Quickstart</a>
		<a href="/docs/sdk/">SDK</a>
		<a href="/docs/api/">API</a>
		<a class="strong" href={data.site.repo}>GitHub</a>
	</nav>
</header>

<main>
	{@render children()}
</main>

<footer class="container" class:wide>
	<div>
		<span>sbx is MIT licensed.</span>
		<span class="links">
			<a href="/docs/">Docs</a>
			<a href={data.site.repo}>github.com/TorstenDittmann/sbx</a>
		</span>
	</div>
</footer>

<style>
	.wide {
		--width: 1200px;
	}

	nav {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 12px 22px;
		padding-block: 22px;
		font-size: 14px;
		color: var(--dim);
	}

	nav a:hover {
		color: var(--fg);
	}

	.logo {
		margin-right: auto;
		font-family: var(--mono);
		font-weight: 500;
		font-size: 15px;
		color: var(--fg);
	}

	.strong {
		color: var(--fg);
	}

	footer div {
		display: flex;
		flex-wrap: wrap;
		justify-content: space-between;
		gap: 8px 24px;
		border-top: 1px solid var(--line);
		padding-block: 22px 32px;
		font-size: 13px;
		color: var(--dim);
	}

	footer .links {
		display: flex;
		gap: 20px;
	}

	footer a:hover {
		color: var(--fg);
	}
</style>
