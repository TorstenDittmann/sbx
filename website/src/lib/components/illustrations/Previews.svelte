<script lang="ts">
	import Packet from "./Packet.svelte";
</script>

<svg class="ill" viewBox="0 0 440 220" role="img" aria-label="A browser opens a preview URL, the sbx proxy forwards it to sbx-agent inside the sandbox, which reaches the app on port 3000.">
	<rect class="box" x="12" y="40" width="168" height="130" rx="8" />
	<circle class="fill-line" cx="26" cy="52" r="3" />
	<circle class="fill-line" cx="36" cy="52" r="3" />
	<circle class="fill-line" cx="46" cy="52" r="3" />
	<rect class="soft" x="20" y="64" width="152" height="22" rx="4" />
	<text x="28" y="79" class="small">3000-sbx-3f2a.sbx.dev</text>
	<g class="load">
		<rect class="fill-line" x="24" y="100" width="120" height="8" rx="2" />
		<rect class="fill-line" x="24" y="116" width="92" height="8" rx="2" />
		<rect class="fill-line" x="24" y="132" width="132" height="24" rx="3" />
	</g>
	<text x="96" y="194" class="dim small" text-anchor="middle">public or with a token</text>

	<path class="wire" d="M180 90 H224" />
	<path class="head" d="M230 90 l-7 -4 v8 Z" />
	<Packet path="M180 90 H226" from={0.05} to={0.2} />
	<rect class="box" x="230" y="66" width="72" height="48" rx="6" />
	<text x="266" y="88" text-anchor="middle">sbx</text>
	<text x="266" y="104" class="dim small" text-anchor="middle">proxy</text>
	<path class="wire" d="M302 90 H340" />
	<path class="head" d="M346 90 l-7 -4 v8 Z" />

	<Packet path="M302 90 H342" from={0.24} to={0.36} />
	<rect class="tint" x="336" y="40" width="94" height="130" rx="8" />
	<text x="348" y="60" class="t-accent small">sandbox</text>
	<rect class="box" x="346" y="78" width="74" height="24" rx="4" />
	<text x="383" y="94" class="small" text-anchor="middle">sbx-agent</text>
	<path class="wire" d="M383 102 V120" />
	<path class="head" d="M383 126 l-4 -7 h8 Z" />
	<Packet path="M383 102 V122" from={0.4} to={0.5} />
	<rect class="soft" x="346" y="126" width="74" height="24" rx="4" />
	<text x="383" y="142" class="small" text-anchor="middle">app :3000</text>
	<text x="383" y="194" class="dim small" text-anchor="middle">network: none</text>
</svg>
