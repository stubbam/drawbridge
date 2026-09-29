<script lang="ts">
	import type { TrafficRange } from '$lib/api';
	import { rangeLabels, trafficRanges } from '$lib/traffic';

	let { value = $bindable(), id }: { value: TrafficRange; id: string } = $props();
</script>

<div class="flex items-center gap-2">
	<label class="text-sm whitespace-nowrap text-neutral-500 dark:text-neutral-400" for={id}
		>Range</label
	>
	<select {id} class="input w-auto py-1.5" bind:value>
		{#each trafficRanges as r (r)}
			<option value={r}>{rangeLabels[r]}</option>
		{/each}
	</select>
</div>
