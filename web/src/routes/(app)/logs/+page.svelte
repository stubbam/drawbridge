<script lang="ts">
	import { resolve } from '$app/paths';
	import { api, type Client, type DrawbridgeEvent, type EventFilter } from '$lib/api';
	import { saveText } from '$lib/download';
	import { errorMessage } from '$lib/errors';
	import { eventActor, eventDetails, eventKinds, eventLabel, formatTime } from '$lib/format';

	const pageSize = 50;
	/** How far back each choice of "When" reaches, in hours. */
	const windows = [
		{ value: '', label: 'Any Time', hours: 0 },
		{ value: '1h', label: 'Last Hour', hours: 1 },
		{ value: '24h', label: 'Last 24 Hours', hours: 24 },
		{ value: '7d', label: 'Last 7 Days', hours: 7 * 24 },
		{ value: '30d', label: 'Last 30 Days', hours: 30 * 24 }
	];

	let category = $state<'' | 'admin' | 'system' | 'connection'>('');
	let kind = $state('');
	let clientID = $state('');
	let when = $state('');
	let clients = $state<Client[]>([]);
	let events = $state<DrawbridgeEvent[]>([]);
	let more = $state(false);
	let error = $state('');
	let loading = $state(false);
	let exporting = $state(false);
	/** The start of "When", fixed when the list loads so that its pages agree. */
	let from = '';

	const kinds = $derived(eventKinds(category));

	function filter(): EventFilter {
		const f: EventFilter = {};
		if (category) f.category = category;
		if (kind) f.kind = kind;
		if (clientID) f.client = clientID;
		if (from) f.from = from;
		return f;
	}

	/** Counts requests, so that a slow answer to an old filter can't replace a newer one's. */
	let latest = 0;

	async function fetchPage(before?: number) {
		const mine = ++latest;
		loading = true;
		error = '';
		try {
			const page = await api.events({ ...filter(), limit: pageSize, before });
			if (mine !== latest) return;
			events = before ? [...events, ...page] : page;
			more = page.length === pageSize;
		} catch (err) {
			if (mine === latest) error = errorMessage(err);
		} finally {
			if (mine === latest) loading = false;
		}
	}

	/** Choosing a category drops an event kind that belongs to another one. */
	function pickCategory(value: string) {
		category = value as typeof category;
		if (kind && !eventKinds(category).some((k) => k.kind === kind)) kind = '';
	}

	async function exportCsv() {
		exporting = true;
		error = '';
		try {
			saveText('drawbridge-events.csv', await api.eventsCsv(filter()), 'text/csv');
		} catch (err) {
			error = errorMessage(err);
		} finally {
			exporting = false;
		}
	}

	api
		.clients()
		.then((list) => (clients = list))
		.catch(() => {
			// The client filter just has nobody to pick; the log itself shows any real problem.
		});

	// Reload from the newest whenever a filter changes.
	$effect(() => {
		const hours = windows.find((w) => w.value === when)?.hours ?? 0;
		from = hours ? new Date(Date.now() - hours * 3_600_000).toISOString() : '';
		void fetchPage();
	});
</script>

<svelte:head><title>Logs · Drawbridge</title></svelte:head>

<div class="flex flex-wrap items-center justify-between gap-3">
	<h1 class="text-2xl font-semibold tracking-tight">Logs</h1>
	<button type="button" class="btn" disabled={exporting} onclick={exportCsv}>
		{exporting ? 'Exporting…' : 'Export CSV'}
	</button>
</div>

<div class="flex flex-wrap items-end gap-3">
	<div>
		<label class="label" for="category">Show</label>
		<select
			class="input w-auto"
			id="category"
			value={category}
			onchange={(e) => pickCategory(e.currentTarget.value)}
		>
			<option value="">Everything</option>
			<option value="admin">Changes and logins</option>
			<option value="connection">Client connections</option>
			<option value="system">Drawbridge itself</option>
		</select>
	</div>
	<div>
		<label class="label" for="kind">Event</label>
		<select class="input w-auto" id="kind" bind:value={kind}>
			<option value="">Any</option>
			{#each kinds as k (k.kind)}
				<option value={k.kind}>{k.label}</option>
			{/each}
		</select>
	</div>
	<div>
		<label class="label" for="client">Client</label>
		<select class="input w-auto" id="client" bind:value={clientID}>
			<option value="">Any</option>
			{#each clients as c (c.id)}
				<option value={c.id}>{c.name}</option>
			{/each}
		</select>
	</div>
	<div>
		<label class="label" for="when">When</label>
		<select class="input w-auto" id="when" bind:value={when}>
			{#each windows as w (w.value)}
				<option value={w.value}>{w.label}</option>
			{/each}
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
