<script lang="ts">
	import Packet from "./Packet.svelte";
</script>

<svg class="ill" viewBox="0 0 440 220" role="img" aria-label="A sandbox with no network, a non-root user, a read-only root filesystem, a writable workspace, and a five minute TTL.">
	<text x="12" y="102" class="dim">internet</text>
	<path class="wire blocked" d="M12 112 H170" />
	<Packet path="M12 112 H91" from={0.1} to={0.35} variant="warn" />
	<circle class="no hit" cx="100" cy="112" r="9" style="animation-delay: 1.3s" />
	<path class="no-mark" d="M96.5 108.5 l7 7 M103.5 108.5 l-7 7" />
	<text x="12" y="140" class="dim small">network: none</text>

	<rect class="tint" x="170" y="30" width="250" height="160" rx="10" />
	<text x="186" y="55" class="t-accent">sbx_3f2a9c1e</text>
	<rect class="box" x="346" y="40" width="58" height="22" rx="11" />
	<text x="375" y="55" class="small" text-anchor="middle">ttl 5m</text>
	<rect class="fill-line" x="352" y="67" width="46" height="3" rx="1.5" />
	<rect class="fill-accent drain" x="352" y="67" width="46" height="3" rx="1.5" />

	<rect class="soft" x="186" y="72" width="218" height="28" rx="5" />
	<text x="198" y="90">uid 1000 · no caps</text>
	<rect class="soft" x="186" y="106" width="218" height="28" rx="5" />
	<text x="198" y="124">read-only rootfs</text>
	<rect class="box" x="186" y="140" width="218" height="34" rx="5" />
	<text x="198" y="161">/workspace</text>
	<rect class="fill-accent blink" x="282" y="150" width="7" height="14" rx="1" />
	<text x="392" y="161" class="dim small" text-anchor="end">writable</text>
</svg>
