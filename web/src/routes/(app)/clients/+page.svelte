<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { api, type Client } from '$lib/api';
	import { errorMessage } from '$lib/errors';
	import { clientState, formatAgo, formatBytes, stateLabels, type ClientState } from '$lib/format';
	import { live, useLive } from '$lib/live.svelte';
	import { poll } from '$lib/poll';
	import { sortClients, storedSort, storeSort, type SortKey } from '$lib/sort';
	import Result from '$lib/components/Result.svelte';
	import SortSelect from '$lib/components/SortSelect.svelte';
	import StateBadge from '$lib/components/StateBadge.svelte';

	let clients = $state<Client[]>();
	let now = $state(Date.now());
	let search = $state('');
	let error = $state('');
	let warning = $state('');
	let toggling = $state<string>();

	// The dashboard's tiles link here with a state filter; "all" means none. This resets
	// on every navigation to this page (even a query-only one, since the component isn't
	// remounted for those), but stays clearable locally in between.
	let stateFilter = $state<ClientState | 'all'>('all');
	$effect(() => {
		const s = page.url.searchParams.get('state');
		stateFilter = s === 'online' || s === 'paused' ? s : 'all';
	});

	async function load() {
		try {
			clients = await api.clients();
			now = Date.now();
		} catch (err) {
			error = errorMessage(err);
		}
	}
	// The live feed pushes the clients as the daemon polls the peers, so while it's open this
	// page doesn't ask for them; when it can't be had, the page polls as it always did.
	$effect(() => useLive());
	$effect(() => {
		if (!live.open) return poll(load, 5000);
	});
	$effect(() => {
		const s = live.status;
		if (!live.open || !s) return;
		clients = s.clients;
		now = Date.now();
	});

	const sortOptions: SortKey[] = ['name', 'status', 'handshake', 'ip'];
	let sort = $state(storedSort('clients', sortOptions, 'name'));
	$effect(() => storeSort('clients', sort));

	let shown = $derived(
		sortClients(
			(clients ?? []).filter((c) => {
				if (stateFilter !== 'all' && clientState(c, now) !== stateFilter) return false;
				const q = search.trim().toLowerCase();
				return !q || c.name.toLowerCase().includes(q) || c.ipv4.includes(q) || c.ipv6.includes(q);
			}),
			sort,
			now
		)
	);

	async function toggle(c: Client) {
		toggling = c.id;
		error = '';
		warning = '';
		try {
			const res = c.enabled ? await api.pauseClient(c.id) : await api.resumeClient(c.id);
			warning = res.warning ?? '';
			await load();
		} catch (err) {
			error = errorMessage(err);
		} finally {
			toggling = undefined;
		}
	}
</script>

<svelte:head><title>Clients · Drawbridge</title></svelte:head>

<h1 class="text-2xl font-semibold tracking-tight">Clients</h1>

<Result {error} {warning} />

{#if clients === undefined}
	<p class="text-sm text-neutral-500">Loading…</p>
{:else if clients.length === 0}
	<p class="card text-sm text-neutral-600 dark:text-neutral-400">
		No clients yet. Add one with "+ Add Client" above.
	</p>
{:else}
	<div class="flex flex-wrap items-center gap-3">
		<label class="sr-only" for="search">Search clients</label>
		<input
			class="input max-w-xs"
			id="search"
			type="search"
			placeholder="Search by name or address"
			bind:value={search}
		/>
		{#if stateFilter !== 'all'}
			<button
				type="button"
				class="inline-flex items-center gap-1.5 rounded-full bg-indigo-100 px-3 py-1 text-sm font-medium text-indigo-800 dark:bg-indigo-950 dark:text-indigo-200"
				aria-label={`Clear the ${stateLabels[stateFilter].toLowerCase()} filter`}
				onclick={() => (stateFilter = 'all')}
			>
				<span aria-hidden="true">{stateLabels[stateFilter]} ✕</span>
			</button>
		{/if}
		<span class="text-sm text-neutral-500">{shown.length} of {clients.length}</span>
		<div class="ml-auto">
			<SortSelect id="clients-sort" options={sortOptions} bind:value={sort} />
		</div>
	</div>
	<ul class="card flex flex-col divide-y divide-neutral-100 p-0 sm:p-0 dark:divide-neutral-800">
		{#each shown as c (c.id)}
			{@const state = clientState(c, now)}
			<li class="flex flex-wrap items-center gap-x-6 gap-y-2 px-4 py-3">
				<div class="min-w-40 flex-1">
					<a
						class="font-medium text-indigo-700 hover:underline dark:text-indigo-300"
						href={resolve('/(app)/clients/[id]', { id: c.id })}>{c.name}</a
					>
					<p class="text-xs text-neutral-500 dark:text-neutral-400">
						{c.ipv4}{#if c.ipv6}, {c.ipv6}{/if}
					</p>
				</div>
				<div class="w-32"><StateBadge {state} /></div>
				<div class="w-40 text-sm text-neutral-600 dark:text-neutral-400">
					{#if c.peer?.last_handshake}
						{formatAgo(c.peer.last_handshake, now)}
						{#if c.peer.endpoint}<br /><span class="text-xs break-all">{c.peer.endpoint}</span>{/if}
					{:else}
						—
					{/if}
				</div>
				<div class="w-36 text-xs text-neutral-600 dark:text-neutral-400">
					{#if c.peer}
						↓ {formatBytes(c.peer.receive_bytes)} · ↑ {formatBytes(c.peer.send_bytes)}
					{/if}
				</div>
				<button
					type="button"
					class="btn w-24 px-2.5 py-1"
					disabled={toggling === c.id}
					onclick={() => toggle(c)}>{c.enabled ? 'Pause' : 'Resume'}</button
				>
			</li>
		{:else}
			<li class="px-4 py-3 text-sm text-neutral-500">
				{#if search}
					No client matches “{search}”.
				{:else}
					No client is {stateLabels[stateFilter as ClientState].toLowerCase()}.
				{/if}
			</li>
		{/each}
	</ul>
{/if}
