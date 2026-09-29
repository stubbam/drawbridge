<script lang="ts">
	import { stateLabels, type ClientState } from '$lib/format';

	let { state }: { state: ClientState } = $props();

	const colors: Record<ClientState, string> = {
		online: 'bg-emerald-500',
		idle: 'bg-amber-400',
		never: 'bg-neutral-300 dark:bg-neutral-600',
		paused: 'bg-sky-500',
		down: 'bg-red-500'
	};
</script>

<span class="inline-flex items-center gap-1.5 text-sm whitespace-nowrap">
	<span class="size-2.5 rounded-full {colors[state]}" aria-hidden="true"></span>
	{stateLabels[state]}
</span>
