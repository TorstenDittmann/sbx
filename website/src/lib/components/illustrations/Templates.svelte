<script lang="ts">
	import Packet from "./Packet.svelte";
</script>

<svg class="ill" viewBox="0 0 440 220" role="img" aria-label="A python template with an image and build steps keeps a pool of warm sandboxes, and create hands one out without a cold start.">
	<rect class="box" x="16" y="34" width="156" height="152" rx="8" />
	<text x="30" y="58">template: python</text>
	<path class="fill-line" d="M16 70 H172 v1 H16 Z" />
	<text x="30" y="94" class="small"><tspan class="dim">image</tspan> python:3.12</text>
	<text x="30" y="118" class="small"><tspan class="dim">build</tspan> pip install</text>
	<text x="30" y="142" class="small"><tspan class="dim">setup</tspan> git clone</text>
	<text x="30" y="166" class="small"><tspan class="dim">warm </tspan> 3</text>

	<path class="wire" d="M172 110 H210" />
	<path class="head" d="M216 110 l-7 -4 v8 Z" />
	<Packet path="M172 110 H212" from={0.05} to={0.25} />

	<text x="222" y="30" class="dim small">warm pool</text>
	{#each [40, 88, 136] as y, i}
		<rect class={i === 0 ? "tint" : "soft"} x="222" y={y} width="96" height="40" rx="6" />
		<circle class="fill-accent pulse" cx="238" cy={y + 20} r="3.5" style="animation-delay: {i * 0.4}s" />
		<text x="250" y={y + 24} class="small">ready</text>
	{/each}

	<path class="wire" d="M318 60 H340" />
	<path class="head" d="M346 60 l-7 -4 v8 Z" />
	<Packet path="M318 60 H342" from={0.55} to={0.72} />
	<rect class="box" x="346" y="42" width="80" height="36" rx="6" />
	<text x="386" y="64" text-anchor="middle">create()</text>
	<text x="386" y="98" class="dim small" text-anchor="middle">no cold</text>
	<text x="386" y="112" class="dim small" text-anchor="middle">start</text>
</svg>
