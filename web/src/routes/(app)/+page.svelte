<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import {
		api,
		type Client,
		type ServerStatus,
		type Settings,
		type TrafficRange,
		type TrafficSeries
	} from '$lib/api';
	import { errorMessage } from '$lib/errors';
	import { clientState, endpointAddress, formatBitrate, formatBytes } from '$lib/format';
	import { live, onLiveEvent, useLive } from '$lib/live.svelte';
	import { poll } from '$lib/poll';
	import { chartRange } from '$lib/range.svelte';
	import { sortClients, storedSort, storeSort, type SortKey } from '$lib/sort';
	import { buildSeriesData, refreshMs, bandwidthSeries } from '$lib/traffic';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import LinkCard from '$lib/components/LinkCard.svelte';
	import NetworkChart from '$lib/components/NetworkChart.svelte';
	import RangeSelect from '$lib/components/RangeSelect.svelte';
	import SortSelect from '$lib/components/SortSelect.svelte';
	import StateBadge from '$lib/components/StateBadge.svelte';
	import { fetchVersion, formatVersion, type VersionInfo } from '$lib/version';

	let settings = $state<Settings>();
	let status = $state<ServerStatus>();
	let clients = $state<Client[]>([]);
	let traffic = $state<TrafficSeries>();
	// The range `traffic` holds, which lags the chosen range until the new range's data arrives.
	let trafficShown = $state<TrafficRange>(chartRange.value);
	let bandwidth = $derived(traffic ? buildSeriesData(traffic, trafficShown) : undefined);
	let version = $state<VersionInfo>();
	let error = $state('');
	let now = $state(Date.now());

	async function load() {
		try {
			[settings, status, clients] = await Promise.all([api.server(), api.status(), api.clients()]);
			now = Date.now();
			error = '';
		} catch (err) {
			error = errorMessage(err);
		}
	}

	async function loadSettings() {
		try {
			settings = await api.server();
		} catch {
			// load() shows a real error; the settings just stay as they were.
		}
	}

	async function loadTraffic() {
		try {
			const asked = chartRange.value;
			const series = await api.traffic(asked);
			// A slower response to an earlier range mustn't replace the current one.
			if (asked !== chartRange.value) return;
			traffic = series;
			trafficShown = asked;
		} catch {
			// The chart just stays as it was; load() above already shows a real error.
		}
	}

	// The Clients page has no route parameter for a state filter, only a query string.
	function goClients(state: 'all' | 'online' | 'paused') {
		// eslint-disable-next-line svelte/no-navigation-without-resolve
		void goto(resolve('/clients') + '?state=' + state);
	}

	// All-time totals, summed across every client (0 for one that's paused or never connected).
	let totalReceived = $derived(clients.reduce((sum, c) => sum + (c.peer?.receive_bytes ?? 0), 0));
	let totalSent = $derived(clients.reduce((sum, c) => sum + (c.peer?.send_bytes ?? 0), 0));

	const sortOptions: SortKey[] = ['name', 'status'];
	let sort = $state(storedSort('dashboard', sortOptions, 'name'));
	$effect(() => storeSort('dashboard', sort));
	let sorted = $derived(sortClients(clients, sort, now));

	// The live feed pushes the status as the daemon polls the peers, so while it's open this page
	// doesn't ask for it; when it can't be had, the page polls as it always did.
	$effect(() => useLive());
	$effect(() => {
		if (!live.open) return poll(load, 5000);
	});
	$effect(() => {
		const s = live.status;
		if (!live.open || !s) return;
		status = s.server;
		clients = s.clients;
		now = Date.now();
		error = '';
	});
	// The settings aren't in the feed, so they're loaded once, and again when they change.
	$effect(() => {
		void loadSettings();
	});
	$effect(() =>
		onLiveEvent((e) => {
			if (e.kind === 'server.settings_changed') void loadSettings();
		})
	);
	// Traffic history is minute-granularity at best (the 1 minute range is the exception), so it
	// doesn't need the 5s peer-status cadence above; changing the range restarts this poll, for
	// an immediate refetch.
	$effect(() => poll(loadTraffic, refreshMs(chartRange.value)));
	$effect(() => {
		fetchVersion().then(
			(v) => (version = v),
			() => {}
		);
	});
</script>

<svelte:head><title>Dashboard · Drawbridge</title></svelte:head>

<h1 class="text-2xl font-semibold tracking-tight">Dashboard</h1>

{#if error}
	<p class="alert-error" role="alert">{error}</p>
{/if}

{#if status && !status.tunnel_up}
	<p class="alert-error" role="status">
		The tunnel is stopped, so no client can connect. Changes are saved and apply when it starts:
		<code>sudo systemctl start drawbridge-tunnel</code>.
	</p>
{/if}
{#if settings && !settings.endpoint}
	<p class="alert-warning" role="status">
		Clients can't get a config until the server's public address is set.
		<a class="font-medium underline" href={resolve('/settings')}>Set it in Settings</a>.
	</p>
{/if}

{#if status}
	<section class="grid grid-cols-2 gap-3 sm:grid-cols-4" aria-label="Summary">
		<div class="card">
			<p class="text-sm text-neutral-500 dark:text-neutral-400">Tunnel</p>
			<p class="text-xl font-semibold {status.tunnel_up ? 'text-emerald-600' : 'text-red-600'}">
				{status.tunnel_up ? 'Up' : 'Stopped'}
			</p>
		</div>
		<button
			type="button"
			class="card card-link text-left"
			aria-label="View all clients"
			onclick={() => goClients('all')}
		>
			<p class="text-sm text-neutral-500 dark:text-neutral-400">Clients</p>
			<p class="text-xl font-semibold">{status.clients}</p>
		</button>
		<button
			type="button"
			class="card card-link text-left"
			aria-label="View online clients"
			onclick={() => goClients('online')}
		>
			<p class="text-sm text-neutral-500 dark:text-neutral-400">Online</p>
			<p class="text-xl font-semibold">{status.online}</p>
		</button>
		<button
			type="button"
			class="card card-link text-left"
			aria-label="View paused clients"
			onclick={() => goClients('paused')}
		>
			<p class="text-sm text-neutral-500 dark:text-neutral-400">Paused</p>
			<p class="text-xl font-semibold">{status.paused}</p>
		</button>
	</section>
{/if}

<LinkCard href={resolve('/charts')} labelledby="bandwidth-heading" class="flex flex-col gap-3">
	<div class="flex flex-wrap items-start justify-between gap-2">
		<div>
			<h2 id="bandwidth-heading" class="font-semibold">
				<a class="hover:underline focus-visible:underline" href={resolve('/charts')}>Bandwidth</a>
			</h2>
			<p class="text-sm text-neutral-500 dark:text-neutral-400">Network traffic of all clients</p>
		</div>
		<RangeSelect id="dashboard-range" />
	</div>
	<NetworkChart
		x={bandwidth?.x ?? []}
		series={bandwidth ? bandwidthSeries(bandwidth, 'rate') : []}
		domain={bandwidth?.domain ?? [0, 0]}
		step={bandwidth?.step ?? 0}
		range={trafficShown}
		format={formatBitrate}
		label="Bandwidth"
		total
		legend={false}
		empty={bandwidth ? 'No traffic in this range' : 'Loading…'}
	/>
</LinkCard>

<div class="grid gap-6 lg:grid-cols-2">
	{#if settings}
		<LinkCard href={resolve('/settings')} labelledby="server-heading" class="flex flex-col gap-3">
			<h2 id="server-heading" class="font-semibold">
				<a class="hover:underline focus-visible:underline" href={resolve('/settings')}>Server</a>
			</h2>
			<dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
				<dt class="text-neutral-500 dark:text-neutral-400">Endpoint</dt>
				<dd>{settings.endpoint || 'Not set'}</dd>
				<dt class="text-neutral-500 dark:text-neutral-400">Listen port</dt>
				<dd>UDP {settings.listen_port}</dd>
				<dt class="text-neutral-500 dark:text-neutral-400">Addresses</dt>
				<dd>
					{settings.ipv4_address}/{settings.ipv4_subnet.split('/')[1]}
					{#if settings.ipv6_address}
						<br />{settings.ipv6_address}/{settings.ipv6_subnet.split('/')[1]}
					{/if}
				</dd>
				<dt class="text-neutral-500 dark:text-neutral-400">DNS</dt>
				<dd>{settings.dns.length ? settings.dns.join(', ') : 'None'}</dd>
				<dt class="text-neutral-500 dark:text-neutral-400">Public key</dt>
				<dd class="flex items-start gap-2">
					<span class="mono">{settings.public_key}</span>
					<CopyButton text={settings.public_key} />
				</dd>
			</dl>
		</LinkCard>
	{/if}

	<LinkCard
		href={resolve('/clients')}
		labelledby="clients-heading"
		class="@container flex flex-col gap-3"
	>
		<div class="flex flex-wrap items-center justify-between gap-2">
			<h2 id="clients-heading" class="font-semibold">
				<a class="hover:underline focus-visible:underline" href={resolve('/clients')}>Clients</a>
			</h2>
			{#if clients.length > 0}
				<SortSelect id="dashboard-sort" options={sortOptions} bind:value={sort} />
			{/if}
		</div>
		{#if clients.length > 0}
			<p class="text-xs text-neutral-500 dark:text-neutral-400">
				All Clients: ↓ {formatBytes(totalReceived)} · ↑ {formatBytes(totalSent)}
			</p>
		{/if}
		{#if clients.length === 0}
			<p class="text-sm text-neutral-500 dark:text-neutral-400">No clients yet.</p>
		{:else}
			<!-- One grid for the whole list, so every row's columns line up: the name, the badge,
			     and the session on the first line; a connected client's endpoint under its name,
			     and the total under the session. A narrow card (a phone) stacks the endpoint, the
			     session, and the total under the name and badge instead. -->
			<ul
				class="grid grid-cols-[minmax(0,1fr)_auto] gap-x-3 divide-y divide-neutral-100 text-sm @min-[26rem]:grid-cols-[minmax(0,1fr)_auto_auto] dark:divide-neutral-800"
			>
				{#each sorted as c (c.id)}
					<li class="col-span-full grid grid-cols-subgrid items-baseline gap-y-1 py-2">
						<a
							class="font-medium wrap-break-word text-indigo-700 hover:underline dark:text-indigo-300"
							href={resolve('/(app)/clients/[id]', { id: c.id })}>{c.name}</a
						>
						<StateBadge state={clientState(c, now)} />
						{#if c.peer?.session_started_at && c.peer.endpoint}
							<div
								class="col-span-full text-xs break-all text-neutral-500 @min-[26rem]:col-[1/3] @min-[26rem]:row-2"
							>
								{endpointAddress(c.peer.endpoint)}
							</div>
						{/if}
						<div
							class="col-span-full text-xs text-neutral-600 @min-[26rem]:col-3 @min-[26rem]:row-1 @min-[26rem]:text-right dark:text-neutral-400"
						>
							{#if c.peer?.session_started_at}
								This session: ↓ {formatBytes(c.peer.session_receive_bytes ?? 0)} · ↑ {formatBytes(
									c.peer.session_send_bytes ?? 0
								)}
							{:else}
								Not connected
							{/if}
						</div>
						{#if c.peer}
							<div
								class="col-span-full text-xs text-neutral-500 @min-[26rem]:col-3 @min-[26rem]:row-2 @min-[26rem]:text-right"
							>
								Total: ↓ {formatBytes(c.peer.receive_bytes)} · ↑ {formatBytes(c.peer.send_bytes)}
							</div>
						{/if}
					</li>
				{/each}
			</ul>
		{/if}
	</LinkCard>
</div>

{#if version}
	<p class="text-center text-xs text-neutral-400">Drawbridge {formatVersion(version)}</p>
{/if}
