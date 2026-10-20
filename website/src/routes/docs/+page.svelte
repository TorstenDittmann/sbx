<script lang="ts">
	import DocPage from "../../lib/components/DocPage.svelte";

	let { data } = $props();
</script>

<DocPage {...data} />
