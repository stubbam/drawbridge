<script lang="ts">
	import { chartRange, setChartRange } from '$lib/range.svelte';
	import { rangeLabels, trafficRanges } from '$lib/traffic';

	let { id }: { id: string } = $props();
</script>

<!-- The choice is shared by every chart (range.svelte.ts), so it isn't a prop. -->
<select
	{id}
	aria-label="Range"
	class="input w-auto py-1.5"
	bind:value={() => chartRange.value, setChartRange}
>
	{#each trafficRanges as r (r)}
		<option value={r}>{rangeLabels[r]}</option>
	{/each}
</select>
