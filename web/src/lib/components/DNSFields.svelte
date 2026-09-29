<script lang="ts">
	// The DNS choice for clients, shared by the setup wizard and Settings. "This server" works
	// only when a resolver answers on the server's VPN addresses, so the check asks them.
	import { onMount } from 'svelte';
	import { api, type DNSCheck, type Settings } from '$lib/api';
	import { errorMessage } from '$lib/errors';
	import { publicDNS, serverDNS, type DNSMode } from '$lib/dns';

	let {
		settings,
		mode = $bindable(),
		custom = $bindable(),
		onchecked,
		autoCheck = false
	}: {
		settings: Settings;
		mode: DNSMode;
		custom: string;
		/** Called with the addresses a check found answering. */
		onchecked?: (usable: string[]) => void;
		/** Check when shown, and pick "This server" if it works. */
		autoCheck?: boolean;
	} = $props();

	let checking = $state(false);
	let result = $state<DNSCheck>();
	let checkError = $state('');

	async function runCheck() {
		checking = true;
		checkError = '';
		try {
			result = await api.dnsCheck();
			onchecked?.(result.usable);
			return result;
		} catch (err) {
			checkError = errorMessage(err);
		} finally {
			checking = false;
		}
	}

	onMount(async () => {
		if (!autoCheck) return;
		const r = await runCheck();
		if (r && r.usable.length > 0) mode = 'server';
	});
</script>

<fieldset class="flex flex-col gap-3 text-sm">
	<legend class="sr-only">DNS servers</legend>
	<label class="flex items-start gap-2">
		<input type="radio" name="dns" value="public" class="mt-0.5" bind:group={mode} />
		<span>
			<span class="font-medium">Public resolvers</span>
			<span class="hint block">
				{publicDNS(settings).join(', ')} (Cloudflare). Works everywhere, and Cloudflare sees the names
				your clients look up.
			</span>
		</span>
	</label>
	<label class="flex items-start gap-2">
		<input type="radio" name="dns" value="server" class="mt-0.5" bind:group={mode} />
		<span>
			<span class="font-medium">This server</span>
			<span class="hint block">
				{serverDNS(settings).join(', ')}. Works only if a DNS resolver on this server listens on
				these addresses, such as AdGuard Home, Pi-hole, Unbound, or dnsmasq. Use the check below to
				find out.
			</span>
		</span>
	</label>
	{#if mode === 'server' || result || checking || checkError}
		<div class="ml-6 flex flex-col gap-2" data-testid="dns-check">
			<div class="flex items-center gap-2">
				<button class="btn" type="button" onclick={runCheck} disabled={checking}>
					{checking ? 'Checking…' : 'Check this server'}
				</button>
			</div>
			{#if checkError}
				<p class="alert-error" role="alert">{checkError}</p>
			{:else if result}
				<ul class="flex flex-col gap-1" aria-live="polite">
					{#each result.results as r (r.address)}
						<li class="text-xs">
							<span class="font-mono">{r.address}</span>:
							<span
								class={r.answered
									? 'font-medium text-emerald-700 dark:text-emerald-400'
									: 'font-medium text-amber-700 dark:text-amber-400'}
							>
								{r.answered ? 'answers' : 'no answer'}
							</span>
							<span class="text-neutral-500 dark:text-neutral-400">. {r.detail}</span>
						</li>
					{/each}
				</ul>
				{#if mode === 'server' && result.usable.length === 0}
					<p class="alert-warning" role="status">
						Nothing answers DNS on this server, so clients couldn't look up names. Pick another
						choice, or install a resolver first.
					</p>
				{/if}
			{/if}
		</div>
	{/if}
	<label class="flex items-start gap-2">
		<input type="radio" name="dns" value="custom" class="mt-0.5" bind:group={mode} />
		<span class="font-medium">Other servers</span>
	</label>
	{#if mode === 'custom'}
		<div class="ml-6">
			<label class="label" for="dns-custom">Addresses</label>
			<input class="input" id="dns-custom" placeholder="9.9.9.9, 2620:fe::fe" bind:value={custom} />
			<p class="hint">IPv4 or IPv6 addresses, separated by commas or spaces.</p>
		</div>
	{/if}
	<label class="flex items-start gap-2">
		<input type="radio" name="dns" value="none" class="mt-0.5" bind:group={mode} />
		<span>
			<span class="font-medium">None</span>
			<span class="hint block"
				>Clients keep using their own DNS, which may leak outside the tunnel.</span
			>
		</span>
	</label>
</fieldset>
