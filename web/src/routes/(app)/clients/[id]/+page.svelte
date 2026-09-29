<script lang="ts">
	import QRCode from 'qrcode';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import {
		api,
		ApiError,
		type Client,
		type ClientSession,
		type DrawbridgeEvent,
		type TrafficRange,
		type TrafficSample
	} from '$lib/api';
	import { errorMessage } from '$lib/errors';
	import {
		clientState,
		configFileName,
		eventActor,
		eventDetails,
		eventLabel,
		formatAgo,
		formatBytes,
		formatTime
	} from '$lib/format';
	import { poll } from '$lib/poll';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import RangeSelect from '$lib/components/RangeSelect.svelte';
	import Result from '$lib/components/Result.svelte';
	import StateBadge from '$lib/components/StateBadge.svelte';
	import TrafficChart from '$lib/components/TrafficChart.svelte';

	let id = $derived(page.params.id ?? '');
	let client = $state<Client>();
	let events = $state<DrawbridgeEvent[]>([]);
	let traffic = $state<TrafficSample[]>([]);
	let trafficRange = $state<TrafficRange>('24h');
	// The range `traffic` holds, which lags trafficRange until the new range's data arrives.
	let trafficShown = $state<TrafficRange>('24h');
	let sessions = $state<ClientSession[]>([]);
	let missing = $state(false);
	let now = $state(Date.now());
	let error = $state('');
	let warning = $state('');
	let success = $state('');
	let busy = $state(false);
	let qr = $state('');
	let configError = $state('');
	let newName = $state('');
	let confirmDelete = $state(false);

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
	$effect(() => poll(load, 5000));

	async function loadHistory() {
		try {
			const asked = trafficRange;
			const [samples, recent] = await Promise.all([
				api.clientTraffic(id, asked),
				api.clientSessions(id, undefined, 20)
			]);
			sessions = recent;
			// A slower response to an earlier range mustn't replace the current one.
			if (asked !== trafficRange) return;
			traffic = samples;
			trafficShown = asked;
		} catch {
			// load() above already shows a real error; the history sections just stay as they were.
		}
	}
	// Traffic and session history change far less often than live peer status, so this
	// polls on its own, slower cadence; changing the range restarts it for an immediate
	// refetch.
	$effect(() => {
		void trafficRange;
		return poll(loadHistory, 60000);
	});

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
		const url = URL.createObjectURL(new Blob([conf], { type: 'text/plain' }));
		const a = document.createElement('a');
		a.href = url;
		a.download = configFileName(client.name);
		// Some browsers only download from a link in the document, and cancel a download
		// whose URL is revoked at once.
		document.body.append(a);
		a.click();
		a.remove();
		setTimeout(() => URL.revokeObjectURL(url), 10_000);
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

	async function rename(e: SubmitEvent) {
		e.preventDefault();
		await run(() => api.renameClient(id, newName.trim()), 'Renamed.');
		if (!error) newName = '';
	}

	async function remove() {
		reset();
		busy = true;
		try {
			await api.deleteClient(id);
			await goto(resolve('/clients'));
		} catch (err) {
			error = errorMessage(err);
			busy = false;
		}
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
		<div class="flex flex-wrap items-center justify-between gap-2">
			<h2 id="traffic-heading" class="font-semibold">Traffic</h2>
			<RangeSelect id="client-traffic-range" bind:value={trafficRange} />
		</div>
		<TrafficChart data={traffic} range={trafficShown} title={client.name} />
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

	<section class="card flex flex-col gap-4" aria-labelledby="manage-heading">
		<h2 id="manage-heading" class="font-semibold">Manage</h2>
		<form class="flex flex-col gap-2 sm:flex-row sm:items-end" onsubmit={rename}>
			<div class="flex-1">
				<label class="label" for="rename">Rename</label>
				<input
					class="input"
					id="rename"
					placeholder={client.name}
					maxlength="64"
					required
					bind:value={newName}
				/>
				<p class="hint">The name is only for you: the config and the keys stay the same.</p>
			</div>
			<button class="btn sm:mb-5" type="submit" disabled={busy}>Rename</button>
		</form>
		<div class="border-t border-neutral-100 pt-4 dark:border-neutral-800">
			{#if confirmDelete}
				<p class="mb-2 text-sm">
					Delete <strong>{client.name}</strong>? Its config stops working at once, and this can't be
					undone.
				</p>
				<div class="flex gap-2">
					<button type="button" class="btn btn-danger" disabled={busy} onclick={remove}
						>Delete</button
					>
					<button type="button" class="btn" onclick={() => (confirmDelete = false)}>Cancel</button>
				</div>
			{:else}
				<button
					type="button"
					class="btn text-red-700 dark:text-red-400"
					onclick={() => (confirmDelete = true)}>Delete this client…</button
				>
			{/if}
		</div>
	</section>
{:else if error}
	<p class="alert-error" role="alert">{error}</p>
{:else}
	<p class="text-sm text-neutral-500">Loading…</p>
{/if}
