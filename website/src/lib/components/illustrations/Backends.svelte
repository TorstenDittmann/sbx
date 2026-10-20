<script lang="ts">
	import Packet from "./Packet.svelte";

	const columns = [
		{ name: "docker", lines: ["namespaces", "cgroups"] },
		{ name: "gvisor", lines: ["user-space", "kernel"] },
		{ name: "kata", lines: ["VM per", "sandbox"] },
		{ name: "firecracker", lines: ["microVM", "on KVM"] },
	];
</script>

<svg class="ill" viewBox="0 0 440 220" role="img" aria-label="One API, POST /v1/sandboxes, runs on docker, gvisor, kata, or firecracker, from lighter to stronger isolation.">
	<rect class="tint" x="16" y="20" width="408" height="34" rx="8" />
	<text x="220" y="42" class="t-accent" text-anchor="middle">POST /v1/sandboxes</text>

	{#each columns as column, i}
		{@const x = 16 + i * 105}
		<path class="wire" d="M{x + 46.5} 54 V74" />
		<rect class="box" x={x} y="74" width="93" height="104" rx="8" />
		<rect class="tint glow" x={x} y="74" width="93" height="104" rx="8" style="animation-delay: {0.72 + i * 0.8}s" />
		<Packet path="M{x + 46.5} 54 V74" from={0.1 + i * 0.2} to={0.18 + i * 0.2} />
		<text x={x + 46.5} y="98" text-anchor="middle">{column.name}</text>
		<text x={x + 46.5} y="120" class="dim small" text-anchor="middle">{column.lines[0]}</text>
		<text x={x + 46.5} y="134" class="dim small" text-anchor="middle">{column.lines[1]}</text>
		{#each [0, 1, 2, 3] as level}
			<rect class={level <= i ? "fill-accent" : "fill-line"} x={x + 18 + level * 15} y="154" width="12" height="6" rx="1.5" />
		{/each}
	{/each}

	<text x="424" y="198" class="dim small" text-anchor="end">stronger isolation</text>
	<path class="wire" d="M16 206 H418" />
	<path class="head" d="M424 206 l-7 -4 v8 Z" />
</svg>
