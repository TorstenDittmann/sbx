<script lang="ts">
	// A dot that travels along `path` once per `cycle` seconds, between the
	// fractions `from` and `to` of the cycle, and is hidden the rest of the time.
	let {
		path,
		cycle = 4,
		from = 0.1,
		to = 0.4,
		variant = "",
	}: { path: string; cycle?: number; from?: number; to?: number; variant?: "" | "warn" | "dim" } = $props();

	const fade = 0.03;
	const times = $derived(`0;${from};${to};1`);
	const opacity = $derived(`0;${from};${from + fade};${to - fade};${to};1`);
</script>

<circle class="packet {variant}" r="3.5" opacity="0">
	<animateMotion {path} dur="{cycle}s" repeatCount="indefinite" calcMode="linear" keyPoints="0;0;1;1" keyTimes={times} />
	<animate attributeName="opacity" dur="{cycle}s" repeatCount="indefinite" values="0;0;1;1;0;0" keyTimes={opacity} />
</circle>
