<script lang="ts">
	import { resolve } from '$app/paths';
	import { api, type DrawbridgeEvent, type EventFilter } from '$lib/api';
	import { errorMessage } from '$lib/errors';
	import { eventActor, eventDetails, eventLabel, formatTime } from '$lib/format';

	const pageSize = 50;
	let category = $state<'' | 'admin' | 'system' | 'connection'>('');
	let events = $state<DrawbridgeEvent[]>([]);
	let more = $state(false);
	let error = $state('');
	let loading = $state(false);

	async function fetchPage(before?: number) {
		loading = true;
		error = '';
		try {
			const filter: EventFilter = { limit: pageSize, before };
			if (category) filter.category = category;
			const page = await api.events(filter);
			events = before ? [...events, ...page] : page;
			more = page.length === pageSize;
		} catch (err) {
			error = errorMessage(err);
		} finally {
			loading = false;
		}
	}

	// Reload from the newest whenever the filter changes.
	$effect(() => {
		void category;
		void fetchPage();
	});
</script>

<svelte:head><title>Logs · Drawbridge</title></svelte:head>

<div class="flex flex-wrap items-center justify-between gap-3">
	<h1 class="text-2xl font-semibold tracking-tight">Logs</h1>
	<div class="flex items-center gap-2">
		<label class="text-sm" for="category">Show</label>
		<select class="input w-auto" id="category" bind:value={category}>
			<option value="">Everything</option>
			<option value="admin">Changes and logins</option>
			<option value="connection">Client connections</option>
			<option value="system">Drawbridge itself</option>
		</select>
	</div>
</div>

{#if error}
	<p class="alert-error" role="alert">{error}</p>
{/if}

<div class="card overflow-x-auto p-0 sm:p-0">
	<table class="w-full text-left text-sm">
		<thead
			class="border-b border-neutral-200 text-xs text-neutral-500 uppercase dark:border-neutral-800"
		>
			<tr>
				<th class="px-4 py-2 font-medium">Time</th>
				<th class="px-4 py-2 font-medium">Event</th>
				<th class="px-4 py-2 font-medium">Who</th>
				<th class="px-4 py-2 font-medium">Details</th>
			</tr>
		</thead>
		<tbody class="divide-y divide-neutral-100 dark:divide-neutral-800">
			{#each events as e (e.id)}
				<tr>
					<td class="px-4 py-2 whitespace-nowrap text-neutral-500 dark:text-neutral-400">
						<time datetime={e.time}>{formatTime(e.time)}</time>
					</td>
					<td class="px-4 py-2">
						{eventLabel(e.kind)}{#if e.client_name}:
							{#if e.client_id}
								<a
									class="font-medium text-indigo-700 hover:underline dark:text-indigo-300"
									href={resolve('/(app)/clients/[id]', { id: e.client_id })}>{e.client_name}</a
								>
							{:else}
								<strong>{e.client_name}</strong>
							{/if}
						{/if}
					</td>
					<td class="px-4 py-2 whitespace-nowrap">{eventActor(e)}</td>
					<td class="px-4 py-2 text-xs text-neutral-600 dark:text-neutral-400">{eventDetails(e)}</td
					>
				</tr>
			{:else}
				<tr
					><td class="px-4 py-3 text-neutral-500" colspan="4"
						>{loading ? 'Loading…' : 'No events.'}</td
					></tr
				>
			{/each}
		</tbody>
	</table>
</div>

{#if more}
	<button
		type="button"
		class="btn self-center"
		disabled={loading}
		onclick={() => fetchPage(events[events.length - 1]?.id)}>Load Older</button
	>
{/if}
