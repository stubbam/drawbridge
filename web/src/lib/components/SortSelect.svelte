<script lang="ts">
	import { sortLabels, type SortKey } from '$lib/sort';

	let {
		value = $bindable(),
		options,
		id
	}: { value: SortKey; options: readonly SortKey[]; id: string } = $props();
</script>

<div class="flex items-center gap-2">
	<label class="text-sm whitespace-nowrap text-neutral-500 dark:text-neutral-400" for={id}
		>Sort by</label
	>
	<select {id} class="input w-auto py-1.5" bind:value>
		{#each options as o (o)}
			<option value={o}>{sortLabels[o]}</option>
		{/each}
	</select>
</div>
