<script lang="ts">
	import { page } from "$app/state";

	let { data, children } = $props();

	function href(slug: string): string {
		return slug === data.sections[0].pages[0].slug ? "/docs/" : `/docs/${slug}/`;
	}

	const current = $derived(
		data.sections.flatMap((section) => section.pages).find((item) => href(item.slug) === page.url.pathname),
	);
</script>

<div class="container docs">
	<details class="mobile-nav">
		<summary>{current?.title ?? "Docs"}</summary>
		{@render nav()}
	</details>
	<aside>
		{@render nav()}
	</aside>
	<div class="content">
		{@render children()}
	</div>
</div>

{#snippet nav()}
	<nav aria-label="Docs">
		{#each data.sections as section}
			<p class="section">{section.title}</p>
			<ul>
				{#each section.pages as item}
					<li>
						<a href={href(item.slug)} aria-current={href(item.slug) === page.url.pathname ? "page" : undefined}>{item.title}</a>
					</li>
				{/each}
			</ul>
		{/each}
	</nav>
{/snippet}

<style>
	.docs {
		--width: 1200px;
		display: grid;
		grid-template-columns: 200px minmax(0, 1fr);
		gap: 48px;
		padding-block: 32px 72px;
	}

	aside nav {
		position: sticky;
		top: 24px;
		max-height: calc(100vh - 48px);
		overflow-y: auto;
		padding-bottom: 24px;
	}

	.section {
		margin: 24px 0 6px;
		font-size: 12px;
		font-weight: 500;
		color: var(--dim);
	}

	.section:first-child {
		margin-top: 0;
	}

	ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}

	li a {
		display: block;
		padding: 4px 0 4px 12px;
		border-left: 1px solid var(--line);
		font-size: 14px;
		color: color-mix(in srgb, var(--fg) 72%, var(--bg));
	}

	li a:hover {
		color: var(--fg);
	}

	li a[aria-current="page"] {
		border-left-color: var(--accent);
		color: var(--accent);
		font-weight: 500;
	}

	.mobile-nav {
		display: none;
	}

	@media (max-width: 860px) {
		.docs {
			grid-template-columns: minmax(0, 1fr);
			gap: 24px;
			padding-block: 8px 48px;
		}

		aside {
			display: none;
		}

		.mobile-nav {
			display: block;
			border: 1px solid var(--line);
			border-radius: 8px;
			padding: 10px 16px;
		}

		.mobile-nav summary {
			cursor: pointer;
			font-size: 14px;
			font-weight: 500;
		}

		.mobile-nav nav {
			padding-block: 8px 4px;
		}
	}
</style>
