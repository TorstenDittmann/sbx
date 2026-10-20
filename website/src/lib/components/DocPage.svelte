<script lang="ts">
	import Seo from "./Seo.svelte";

	type Link = { slug: string; title: string; description: string };

	let {
		page,
		html,
		headings,
		prev,
		next,
	}: {
		page: Link;
		html: string;
		headings: { id: string; text: string }[];
		prev: Link | null;
		next: Link | null;
	} = $props();

	function href(slug: string): string {
		return slug === "introduction" ? "/docs/" : `/docs/${slug}/`;
	}
</script>

<Seo title="{page.title} · sbx docs" description={page.description} />

<div class="page">
	<article>
		<header>
			<h1>{page.title}</h1>
			<p class="lead">{page.description}</p>
		</header>
		<div class="prose">{@html html}</div>
		<nav class="pager" aria-label="Previous and next">
			{#if prev}
				<a class="prev" href={href(prev.slug)}><span>Previous</span>{prev.title}</a>
			{/if}
			{#if next}
				<a class="next" href={href(next.slug)}><span>Next</span>{next.title}</a>
			{/if}
		</nav>
	</article>
	{#if headings.length > 1}
		<aside>
			<nav aria-label="On this page">
				<p>On this page</p>
				<ul>
					{#each headings as heading}
						<li><a href="#{heading.id}">{heading.text}</a></li>
					{/each}
				</ul>
			</nav>
		</aside>
	{/if}
</div>

<style>
	.page {
		display: grid;
		grid-template-columns: minmax(0, 1fr) 180px;
		gap: 48px;
	}

	article {
		min-width: 0;
		max-width: 740px;
	}

	header {
		margin-bottom: 32px;
		padding-bottom: 24px;
		border-bottom: 1px solid var(--line);
	}

	h1 {
		margin: 0 0 8px;
		font-size: clamp(30px, 4vw, 38px);
		font-weight: 500;
		line-height: 1.1;
		letter-spacing: -0.035em;
	}

	.lead {
		margin: 0;
		font-size: 17px;
		color: var(--dim);
	}

	aside nav {
		position: sticky;
		top: 24px;
		font-size: 13px;
	}

	aside p {
		margin: 0 0 8px;
		font-weight: 500;
		color: var(--dim);
		font-size: 12px;
	}

	aside ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}

	aside li a {
		display: block;
		padding: 3px 0;
		color: var(--dim);
	}

	aside li a:hover {
		color: var(--fg);
	}

	.pager {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 16px;
		margin-top: 56px;
	}

	.pager a {
		display: flex;
		flex-direction: column;
		gap: 2px;
		border: 1px solid var(--line);
		border-radius: 8px;
		padding: 12px 16px;
		font-weight: 500;
	}

	.pager a:hover {
		border-color: var(--accent);
	}

	.pager span {
		font-size: 12px;
		font-weight: 400;
		color: var(--dim);
	}

	.next {
		grid-column: 2;
		text-align: right;
	}

	@media (max-width: 1100px) {
		.page {
			grid-template-columns: minmax(0, 1fr);
		}

		aside {
			display: none;
		}
	}

	@media (max-width: 520px) {
		.pager {
			grid-template-columns: 1fr;
		}

		.next {
			grid-column: 1;
		}
	}
</style>
