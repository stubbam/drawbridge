<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type Settings, type SettingsPatch } from '$lib/api';
	import { errorMessage } from '$lib/errors';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import AdGuardSettings from '$lib/components/AdGuardSettings.svelte';
	import DNSFields from '$lib/components/DNSFields.svelte';
	import Result from '$lib/components/Result.svelte';
	import { dnsFor, dnsModeOf, type DNSMode } from '$lib/dns';

	let saved = $state<Settings>();
	let endpointHost = $state('');
	let endpointPort = $state(0);
	let listenPort = $state(0);
	let mtu = $state(0);
	let keepalive = $state(0);
	let isolation = $state(true);
	let dnsMode = $state<DNSMode>('public');
	let dnsCustom = $state('');
	let dnsUsable = $state<string[]>([]);
	let error = $state('');
	let warning = $state('');
	let success = $state('');
	let busy = $state(false);

	function fill(s: Settings) {
		saved = s;
		endpointHost = s.endpoint_host;
		endpointPort = s.endpoint_port;
		listenPort = s.listen_port;
		mtu = s.mtu;
		keepalive = s.keepalive;
		isolation = s.client_isolation;
		dnsMode = dnsModeOf(s);
		dnsCustom = dnsMode === 'custom' ? s.dns.join(', ') : '';
		dnsUsable = [];
	}

	onMount(() => {
		api.server().then(fill, (err) => (error = errorMessage(err)));
	});

	/** Only the settings that changed, so the server's event log says what happened. */
	function patch(s: Settings): SettingsPatch {
		const p: SettingsPatch = {};
		if (endpointHost.trim() !== s.endpoint_host) p.endpoint_host = endpointHost.trim();
		if (endpointPort !== s.endpoint_port) p.endpoint_port = endpointPort;
		if (listenPort !== s.listen_port) p.listen_port = listenPort;
		if (mtu !== s.mtu) p.mtu = mtu;
		if (keepalive !== s.keepalive) p.keepalive = keepalive;
		if (isolation !== s.client_isolation) p.client_isolation = isolation;
		const dns = dnsFor(s, dnsMode, dnsCustom, dnsUsable);
		if (dns.join() !== s.dns.join()) p.dns = dns;
		return p;
	}

	async function save(e: SubmitEvent) {
		e.preventDefault();
		if (!saved) return;
		error = warning = success = '';
		const p = patch(saved);
		if (Object.keys(p).length === 0) {
			success = 'Nothing changed.';
			return;
		}
		busy = true;
		try {
			const res = await api.updateServer(p);
			fill(res.settings);
			warning = res.warning ?? '';
			success =
				'Saved and applied. Clients get the new endpoint, port, MTU, DNS, and keepalive when they download their config again.';
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>Settings · Drawbridge</title></svelte:head>

<h1 class="text-2xl font-semibold tracking-tight">Settings</h1>

{#if saved}
	<form class="flex flex-col gap-6" onsubmit={save}>
		<section class="card flex flex-col gap-4" aria-labelledby="endpoint-heading">
			<div>
				<h2 id="endpoint-heading" class="font-semibold">Endpoint</h2>
				<p class="hint">
					Where clients connect. Changing it means clients need their config again.
				</p>
			</div>
			<div class="grid gap-4 sm:grid-cols-[1fr_10rem]">
				<div>
					<label class="label" for="endpoint-host">Public address</label>
					<input
						class="input"
						id="endpoint-host"
						placeholder="vpn.example.com"
						autocapitalize="off"
						spellcheck="false"
						bind:value={endpointHost}
					/>
					<p class="hint">A domain name (kept current by DDNS) or a public IP address.</p>
				</div>
				<div>
					<label class="label" for="endpoint-port">Public port</label>
					<input
						class="input"
						id="endpoint-port"
						type="number"
						min="0"
						max="65535"
						required
						bind:value={endpointPort}
					/>
					<p class="hint">0 means the listen port. Set it if the router forwards another port.</p>
				</div>
			</div>
		</section>

		<section class="card flex flex-col gap-4" aria-labelledby="tunnel-heading">
			<h2 id="tunnel-heading" class="font-semibold">Tunnel</h2>
			<div class="grid gap-4 sm:grid-cols-3">
				<div>
					<label class="label" for="listen-port">Listen port (UDP)</label>
					<input
						class="input"
						id="listen-port"
						type="number"
						min="1"
						max="65535"
						required
						bind:value={listenPort}
					/>
					<p class="hint">Changing it disconnects every client until it has the new config.</p>
				</div>
				<div>
					<label class="label" for="mtu">MTU</label>
					<input
						class="input"
						id="mtu"
						type="number"
						min="1280"
						max="1500"
						required
						bind:value={mtu}
					/>
					<p class="hint">1420 fits IPv4 and IPv6 paths. Lower it if large pages stall.</p>
				</div>
				<div>
					<label class="label" for="keepalive">Keepalive (seconds)</label>
					<input
						class="input"
						id="keepalive"
						type="number"
						min="0"
						max="3600"
						required
						bind:value={keepalive}
					/>
					<p class="hint">Keeps phones reachable behind NAT. 0 turns it off.</p>
				</div>
			</div>
			<label class="flex items-start gap-2 text-sm">
				<input type="checkbox" class="mt-0.5" bind:checked={isolation} />
				<span>
					<span class="font-medium">Client isolation</span>
					<span class="hint block"
						>Clients can't reach each other through the tunnel. Applies at once.</span
					>
				</span>
			</label>
		</section>

		<section class="card flex flex-col gap-4" aria-labelledby="dns-heading">
			<div>
				<h2 id="dns-heading" class="font-semibold">DNS for clients</h2>
				<p class="hint">Clients get it in their config, so they need it again after a change.</p>
			</div>
			<DNSFields
				settings={saved}
				bind:mode={dnsMode}
				bind:custom={dnsCustom}
				onchecked={(u) => (dnsUsable = u)}
			/>
		</section>

		<div class="flex flex-col gap-3">
			<Result {error} {warning} {success} />
			<button class="btn btn-primary self-start" type="submit" disabled={busy}>
				{busy ? 'Saving…' : 'Save settings'}
			</button>
		</div>
	</form>

	<AdGuardSettings />

	<section class="card flex flex-col gap-3" aria-labelledby="addressing-heading">
		<h2 id="addressing-heading" class="font-semibold">Addressing</h2>
		<dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
			<dt class="text-neutral-500 dark:text-neutral-400">Interface</dt>
			<dd>{saved.interface}</dd>
			<dt class="text-neutral-500 dark:text-neutral-400">IPv4 subnet</dt>
			<dd>{saved.ipv4_subnet} (server {saved.ipv4_address})</dd>
			<dt class="text-neutral-500 dark:text-neutral-400">IPv6 subnet</dt>
			<dd>{saved.ipv6_subnet ? `${saved.ipv6_subnet} (server ${saved.ipv6_address})` : 'Off'}</dd>
			<dt class="text-neutral-500 dark:text-neutral-400">Clients route</dt>
			<dd>{saved.client_allowed_ips.join(', ')} (everything, over the tunnel)</dd>
			<dt class="text-neutral-500 dark:text-neutral-400">Public key</dt>
			<dd class="flex items-start gap-2">
				<span class="mono">{saved.public_key}</span>
				<CopyButton text={saved.public_key} />
			</dd>
		</dl>
		<p class="hint">Changing the subnets isn't supported yet while clients exist.</p>
	</section>
{:else if error}
	<p class="alert-error" role="alert">{error}</p>
{:else}
	<p class="text-sm text-neutral-500">Loading…</p>
{/if}
