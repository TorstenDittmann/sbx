<script lang="ts">
	import Packet from "./Packet.svelte";
</script>

<svg class="ill" viewBox="0 0 440 220" role="img" aria-label="A named volume mounted at /workspace outlives one sandbox and is mounted by the next.">
	<rect class="ghost" x="24" y="24" width="150" height="66" rx="8" />
	<text x="40" y="50" class="dim">sbx_a</text>
	<text x="158" y="50" class="dim small" text-anchor="end">stopped</text>
	<text x="40" y="74" class="dim small">/workspace</text>

	<text x="220" y="54" class="dim small" text-anchor="middle">later</text>
	<path class="wire faint" d="M184 62 H250" />
	<path class="head" d="M256 62 l-7 -4 v8 Z" />

	<rect class="tint" x="266" y="24" width="150" height="66" rx="8" />
	<text x="282" y="50" class="t-accent">sbx_b</text>
	<text x="400" y="50" class="dim small" text-anchor="end">running</text>
	<text x="282" y="74" class="small">/workspace</text>

	<path class="wire faint" d="M99 90 V118 Q99 128 109 128 H168" />
	<path class="wire" d="M341 90 V118 Q341 128 331 128 H272" />

	<Packet path="M99 90 V118 Q99 128 109 128 H168" from={0.05} to={0.35} />
	<Packet path="M272 128 H331 Q341 128 341 118 V90" from={0.55} to={0.85} />
	<path class="soft" d="M160 132 V178 A60 11 0 0 0 280 178 V132" />
	<ellipse class="box" cx="220" cy="132" rx="60" ry="11" />
	<text x="220" y="166" text-anchor="middle">vol project</text>
	<text x="220" y="210" class="dim small" text-anchor="middle">outlives every sandbox</text>
</svg>
