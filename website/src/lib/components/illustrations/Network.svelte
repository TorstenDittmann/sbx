<script lang="ts">
	import Packet from "./Packet.svelte";

	const targets = [
		{ y: 40, host: "pypi.org", allowed: true, at: 0.05 },
		{ y: 95, host: "169.254.169.254", allowed: false, at: 0.38 },
		{ y: 150, host: "10.0.0.0/8", allowed: false, at: 0.68 },
	];
</script>

<svg class="ill" viewBox="0 0 440 220" role="img" aria-label="A sandbox in allow mode reaches pypi.org, while the metadata address and private ranges stay blocked.">
	<rect class="tint" x="16" y="50" width="130" height="120" rx="10" />
	<text x="30" y="74" class="t-accent">sandbox</text>
	<text x="30" y="100" class="small"><tspan class="dim">mode</tspan> allow</text>
	<text x="30" y="120" class="small"><tspan class="dim">allow</tspan> pypi.org</text>

	{#each targets as target}
		{@const y = target.y + 15}
		<path class={target.allowed ? "wire" : "wire blocked"} d="M146 110 H186 {y === 110 ? '' : `V${y}`} H270" />
		{#if target.allowed}
			<Packet path="M146 110 H186 V{y} H270" cycle={5} from={target.at} to={target.at + 0.24} />
			<circle class="ok" cx="228" cy={y} r="9" />
			<path class="ok-mark" d="M224 {y} l3 3 l5 -6" />
		{:else}
			<Packet path="M146 110 H186 {y === 110 ? '' : `V${y}`} H219" cycle={5} from={target.at} to={target.at + 0.18} variant="warn" />
			<circle class="no hit" cx="228" cy={y} r="9" style="animation-duration: 5s; animation-delay: {(target.at + 0.18) * 5 - 0.2}s" />
			<path class="no-mark" d="M224.5 {y - 3.5} l7 7 M231.5 {y - 3.5} l-7 7" />
		{/if}
		<rect class={target.allowed ? "box" : "soft"} x="270" y={target.y} width="154" height="30" rx="6" />
		<text x="284" y={target.y + 19} class={target.allowed ? "" : "dim"}>{target.host}</text>
	{/each}

	<text x="270" y="206" class="dim small">metadata and private ranges</text>
</svg>
