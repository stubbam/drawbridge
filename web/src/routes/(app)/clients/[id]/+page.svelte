<script lang="ts">
	import QRCode from 'qrcode';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import {
		api,
		ApiError,
		type Client,
		type ClientSession,
		type DrawbridgeEvent,
		type TrafficRange,
		type TrafficSeries
	} from '$lib/api';
	import { saveText } from '$lib/download';
	import { errorMessage } from '$lib/errors';
	import {
		clientState,
		configFileName,
		eventActor,
		eventDetails,
		eventLabel,
		formatAgo,
		formatBitrate,
		formatBytes,
		formatTime
	} from '$lib/format';
	import { live, onLiveEvent, useLive } from '$lib/live.svelte';
	import { poll } from '$lib/poll';
	import { chartRange } from '$lib/range.svelte';
	import { buildSeriesData, refreshMs, bandwidthSeries } from '$lib/traffic';
	import ClientDNSLog from '$lib/components/ClientDNSLog.svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import DeleteClientModal from '$lib/components/DeleteClientModal.svelte';
	import NetworkChart from '$lib/components/NetworkChart.svelte';
	import RangeSelect from '$lib/components/RangeSelect.svelte';
	import RenameClientModal from '$lib/components/RenameClientModal.svelte';
	import Result from '$lib/components/Result.svelte';
	import StateBadge from '$lib/components/StateBadge.svelte';

	let id = $derived(page.params.id ?? '');
	let client = $state<Client>();
	let events = $state<DrawbridgeEvent[]>([]);
	let traffic = $state<TrafficSeries>();
	// The range `traffic` holds, which lags the chosen range until the new range's data arrives.
	let trafficShown = $state<TrafficRange>(chartRange.value);
	let bandwidth = $derived(traffic ? buildSeriesData(traffic, trafficShown) : undefined);
	let sessions = $state<ClientSession[]>([]);
	let missing = $state(false);
	let now = $state(Date.now());
	let error = $state('');
	let warning = $state('');
	let success = $state('');
	let busy = $state(false);
	let qr = $state('');
	let configError = $state('');
	let showRename = $state(false);
	let showDelete = $state(false);

	async function load() {
		try {
			const [c, ev] = await Promise.all([api.client(id), api.events({ client: id, limit: 20 })]);
			client = c;
			events = ev;
			now = Date.now();
		} catch (err) {
			if (err instanceof ApiError && err.status === 404) missing = true;
			else error = errorMessage(err);
		}
	}

	async function loadEvents() {
		try {
			events = await api.events({ client: id, limit: 20 });
		} catch {
			// load() shows a real error; the events just stay as they were.
		}
	}

	// The live feed pushes the client's status as the daemon polls the peers, so while it's open
	// this page doesn't ask for it; when it can't be had, the page polls as it always did.
	$effect(() => useLive());
	$effect(() => {
		if (!live.open) return poll(load, 5000);
	});
	$effect(() => {
		const s = live.status;
		if (!live.open || !s) return;
		const c = s.clients.find((c) => c.id === id);
		if (c) {
			client = c;
			missing = false;
			now = Date.now();
		} else {
			// Not in the feed: just added (its status is on the way), or gone. Asking says which.
			void load();
		}
	});
	// The client's events are loaded once, and again if the feed was away, and then each one
	// arrives as it's recorded. A connection or a disconnection also changes its history.
	$effect(() => {
		void live.reconnects;
		void loadEvents();
	});
	$effect(() => {
		const mine = id;
		return onLiveEvent((e) => {
			if (e.client_id !== mine) return;
			events = [e, ...events.filter((x) => x.id !== e.id)].slice(0, 20);
			if (e.kind === 'client.connected' || e.kind === 'client.disconnected') void loadHistory();
		});
	});

	async function loadHistory() {
		try {
			const asked = chartRange.value;
			const [series, recent] = await Promise.all([
				api.clientTraffic(id, asked),
				api.clientSessions(id, undefined, 20)
			]);
			sessions = recent;
			// A slower response to an earlier range mustn't replace the current one.
			if (asked !== chartRange.value) return;
			traffic = series;
			trafficShown = asked;
		} catch {
			// load() above already shows a real error; the history sections just stay as they were.
		}
	}
	// Traffic and session history change far less often than live peer status (the 1 minute
	// range is the exception), so this polls on its own cadence; changing the range restarts it
	// for an immediate refetch.
	$effect(() => poll(loadHistory, refreshMs(chartRange.value)));

	// A client that was just added shows its QR code at once.
	$effect(() => {
		if (page.state.justAdded) void showQR();
	});

	function reset() {
		error = '';
		warning = '';
		success = '';
	}

	async function fetchConfig(): Promise<string | undefined> {
		configError = '';
		try {
			return await api.clientConfig(id);
		} catch (err) {
			configError = errorMessage(err);
			return undefined;
		}
	}

	async function showQR() {
		const conf = await fetchConfig();
		if (conf) {
			qr = await QRCode.toDataURL(conf, { errorCorrectionLevel: 'L', margin: 2, width: 360 });
			void load();
		}
	}

	async function download() {
		const conf = await fetchConfig();
		if (!conf || !client) return;
		saveText(configFileName(client.name), conf);
		void load();
	}

	async function run(action: () => Promise<{ warning?: string } | void>, done: string) {
		reset();
		busy = true;
		try {
			const res = await action();
			warning = (res && res.warning) || '';
			success = done;
			await load();
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}

	// The rename dialog saved the new name: show it, and say so.
	async function renamed() {
		reset();
		success = 'Renamed.';
		await load();
	}
</script>

<svelte:head><title>{client?.name ?? 'Client'} · Drawbridge</title></svelte:head>

<a class="text-sm text-indigo-700 hover:underline dark:text-indigo-300" href={resolve('/clients')}
	>← Clients</a
>

{#if missing}
	<p class="card">This client doesn't exist; it may have been deleted.</p>
{:else if client}
	{@const state = clientState(client, now)}
	<div class="flex flex-wrap items-center justify-between gap-3">
		<div class="flex items-center gap-4">
			<h1 class="text-2xl font-semibold tracking-tight">{client.name}</h1>
			<StateBadge {state} />
		</div>
		<div class="flex flex-wrap gap-2">
			{#if client.enabled}
				<button
					type="button"
					class="btn"
					disabled={busy}
					onclick={() => run(() => api.pauseClient(id), 'Paused. The client is out of the tunnel.')}
					>Pause</button
				>
			{:else}
				<button
					type="button"
					class="btn btn-primary"
					disabled={busy}
					onclick={() =>
						run(
							() => api.resumeClient(id),
							'Resumed. The client reconnects within about 15 seconds.'
						)}>Resume</button
				>
			{/if}
			<button type="button" class="btn" onclick={() => (showRename = true)}>Rename</button>
			<button
				type="button"
				class="btn text-red-700 dark:text-red-400"
				onclick={() => (showDelete = true)}>Delete</button
			>
		</div>
	</div>

	<Result {error} {warning} {success} />

	<div class="grid gap-6 lg:grid-cols-2">
		<section class="card flex flex-col gap-3" aria-labelledby="config-heading">
			<h2 id="config-heading" class="font-semibold">Connect a Device</h2>
			<p class="text-sm text-neutral-600 dark:text-neutral-400">
				In the WireGuard app, tap + and scan the QR code, or import the file. The config holds the
				client's private key, so every view is recorded in the log.
			</p>
			<div class="flex flex-wrap gap-2">
				<button type="button" class="btn btn-primary" onclick={showQR}>Show QR code</button>
				<button type="button" class="btn" onclick={download}>Download .conf</button>
			</div>
			{#if configError}
				<p class="alert-error" role="alert">
					{configError}
					{#if configError.includes('endpoint')}
						<a class="font-medium underline" href={resolve('/settings')}>Set it in Settings</a>.
					{/if}
				</p>
			{/if}
			{#if qr}
				<img
					src={qr}
					alt="QR code of {client.name}'s WireGuard config"
					class="mx-auto w-full max-w-80 rounded-lg bg-white p-2"
				/>
				<button type="button" class="btn self-center" onclick={() => (qr = '')}>Hide</button>
			{/if}
		</section>

		<section class="card flex flex-col gap-3" aria-labelledby="details-heading">
			<h2 id="details-heading" class="font-semibold">Details</h2>
			<dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
				<dt class="text-neutral-500 dark:text-neutral-400">IPv4</dt>
				<dd>{client.ipv4}</dd>
				<dt class="text-neutral-500 dark:text-neutral-400">IPv6</dt>
				<dd>{client.ipv6 || 'Off'}</dd>
				<dt class="text-neutral-500 dark:text-neutral-400">Last handshake</dt>
				<dd>{formatAgo(client.peer?.last_handshake, now)}</dd>
				<dt class="text-neutral-500 dark:text-neutral-400">Endpoint</dt>
				<dd>{client.peer?.endpoint ?? '—'}</dd>
				<dt class="text-neutral-500 dark:text-neutral-400">Traffic</dt>
				<dd>
					{#if client.peer}
						↓ {formatBytes(client.peer.receive_bytes)} received · ↑ {formatBytes(
							client.peer.send_bytes
						)} sent
					{:else}
						—
					{/if}
				</dd>
				<dt class="text-neutral-500 dark:text-neutral-400">Added</dt>
				<dd>{formatTime(client.created_at)}</dd>
				<dt class="text-neutral-500 dark:text-neutral-400">Public key</dt>
				<dd class="flex items-start gap-2">
					<span class="mono">{client.public_key}</span>
					<CopyButton text={client.public_key} />
				</dd>
			</dl>
		</section>
	</div>

	<section class="card flex flex-col gap-3" aria-labelledby="traffic-heading">
		<div class="flex flex-wrap items-start justify-between gap-2">
			<div>
				<h2 id="traffic-heading" class="font-semibold">Bandwidth</h2>
				<p class="text-sm text-neutral-500 dark:text-neutral-400">Network traffic of this client</p>
			</div>
			<RangeSelect id="client-traffic-range" />
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
	</section>

	<section class="card flex flex-col gap-3" aria-labelledby="cumulative-heading">
		<div>
			<h2 id="cumulative-heading" class="font-semibold">Cumulative Traffic</h2>
			<p class="text-sm text-neutral-500 dark:text-neutral-400">
				Total data received and sent since the start of the range
			</p>
		</div>
		<NetworkChart
			x={bandwidth?.x ?? []}
			series={bandwidth ? bandwidthSeries(bandwidth, 'cumulative') : []}
			domain={bandwidth?.domain ?? [0, 0]}
			step={bandwidth?.step ?? 0}
			range={trafficShown}
			format={formatBytes}
			label="Cumulative Traffic"
			total
			legend={false}
			empty={bandwidth ? 'No traffic in this range' : 'Loading…'}
		/>
	</section>

	<section class="card flex flex-col gap-3" aria-labelledby="sessions-heading">
		<h2 id="sessions-heading" class="font-semibold">Sessions</h2>
		{#if sessions.length === 0}
			<p class="text-sm text-neutral-500">No connections recorded yet.</p>
		{:else}
			<ul class="flex flex-col divide-y divide-neutral-100 text-sm dark:divide-neutral-800">
				{#each sessions as s (s.id)}
					<li class="flex flex-wrap justify-between gap-x-3 py-2">
						<span>
							{s.endpoint || 'Unknown endpoint'}
							<span class="text-neutral-500 dark:text-neutral-400">
								· ↓ {formatBytes(s.receive_bytes)} · ↑ {formatBytes(s.send_bytes)}
							</span>
						</span>
						<span class="text-neutral-500 dark:text-neutral-400">
							{formatTime(s.started_at)}
							{#if s.ended_at}
								– {formatTime(s.ended_at)}
							{:else}
								– ongoing
							{/if}
						</span>
					</li>
				{/each}
			</ul>
		{/if}
	</section>

	<ClientDNSLog {id} />

	<section class="card flex flex-col gap-3" aria-labelledby="events-heading">
		<h2 id="events-heading" class="font-semibold">Activity</h2>
		{#if events.length === 0}
			<p class="text-sm text-neutral-500">Nothing yet.</p>
		{:else}
			<ul class="flex flex-col divide-y divide-neutral-100 text-sm dark:divide-neutral-800">
				{#each events as e (e.id)}
					<li class="flex flex-wrap justify-between gap-x-3 py-2">
						<span>
							{eventLabel(e.kind)}
							<span class="text-neutral-500 dark:text-neutral-400">· {eventActor(e)}</span>
							{#if eventDetails(e)}<br /><span class="text-xs text-neutral-500"
									>{eventDetails(e)}</span
								>{/if}
						</span>
						<time class="text-neutral-500 dark:text-neutral-400" datetime={e.time}
							>{formatTime(e.time)}</time
						>
					</li>
				{/each}
			</ul>
		{/if}
	</section>

	<RenameClientModal bind:open={showRename} {id} current={client.name} onrenamed={renamed} />
	<DeleteClientModal bind:open={showDelete} {id} name={client.name} />
{:else if error}
	<p class="alert-error" role="alert">{error}</p>
{:else}
	<p class="text-sm text-neutral-500">Loading…</p>
{/if}
