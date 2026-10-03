<script lang="ts">
	// The connection to AdGuard Home: where its API is, and the account Drawbridge uses. It has its
	// own Save, apart from the server settings, because it's a different part of the API. The
	// password is write-only: the server never sends it back, so the field starts empty.
	import { onMount } from 'svelte';
	import { api, type AdGuardConnection, type AdGuardTest } from '$lib/api';
	import { adguardRequest, needsPassword } from '$lib/adguard';
	import { errorMessage } from '$lib/errors';
	import { formatTime } from '$lib/format';
	import Result from './Result.svelte';

	let saved = $state<AdGuardConnection>();
	let baseUrl = $state('');
	let username = $state('');
	let password = $state('');
	let enabled = $state(false);
	let syncNames = $state(true);
	let busy = $state<'' | 'save' | 'test' | 'sync' | 'remove'>('');
	let confirmRemove = $state(false);
	let error = $state('');
	let success = $state('');
	let test = $state<AdGuardTest>();

	function fill(c: AdGuardConnection) {
		saved = c;
		baseUrl = c.base_url;
		username = c.username;
		password = '';
		enabled = c.enabled;
		syncNames = c.sync_names;
	}

	onMount(() => {
		api.adguard().then(fill, (err) => (error = errorMessage(err)));
	});

	const form = $derived({ baseUrl, username, password, enabled, syncNames });
	const askPassword = $derived(saved ? needsPassword(saved, form) : false);

	/** A result describes the values it was run on, so it goes when they change. */
	function edited() {
		test = undefined;
		success = '';
		confirmRemove = false;
	}

	async function run<T>(what: typeof busy, fn: () => Promise<T>): Promise<T | undefined> {
		busy = what;
		error = success = '';
		try {
			return await fn();
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = '';
		}
	}

	async function save(e: SubmitEvent) {
		e.preventDefault();
		if (!saved) return;
		const c = await run('save', async () => {
			const c = await api.saveAdGuard(adguardRequest(saved!, form));
			if (c.enabled && c.sync_names) {
				// The save starts a sync a second later. Ask for it now, to show how it went.
				try {
					c.sync = await api.syncAdGuard();
				} catch {
					// The saved connection stands, and the status below says what the sync is doing.
				}
			}
			return c;
		});
		if (c) {
			fill(c);
			test = undefined;
			success = 'Saved.';
		}
	}

	async function runTest() {
		if (!saved) return;
		const t = await run('test', () => api.testAdGuard(adguardRequest(saved!, form)));
		if (t) test = t;
	}

	async function syncNow() {
		const s = await run('sync', () => api.syncAdGuard());
		if (s && saved) saved = { ...saved, sync: s };
	}

	async function remove() {
		const c = await run('remove', () => api.removeAdGuard());
		if (c) {
			fill(c);
			test = undefined;
			confirmRemove = false;
			success = 'Removed. The address and the password are forgotten.';
		}
	}

	function queryLogText(q: NonNullable<AdGuardTest['query_log']>): string {
		if (!q.enabled) return 'off';
		return q.anonymize_client_ip ? "on, but it hides each client's address" : 'on';
	}
</script>

{#if saved}
	<form class="card flex flex-col gap-4" aria-labelledby="adguard-heading" onsubmit={save}>
		<div>
			<h2 id="adguard-heading" class="font-semibold">AdGuard Home</h2>
			<p class="hint">
				Optional. Lets Drawbridge talk to AdGuard Home's API, for the features that need it.
				Everything else works without it. Use an account made for Drawbridge if you have one
				(AdGuard Home's <span class="mono">AdGuardHome.yaml</span> can list several users).
			</p>
		</div>

		<div class="grid gap-4 sm:grid-cols-[2fr_1fr_1fr]">
			<div>
				<label class="label" for="adguard-url">Address</label>
				<input
					class="input"
					id="adguard-url"
					placeholder="http://127.0.0.1:3000/control"
					autocapitalize="off"
					autocomplete="off"
					spellcheck="false"
					bind:value={baseUrl}
					oninput={edited}
				/>
				<p class="hint">
					Where AdGuard Home's web interface is. <span class="mono">/control</span> is added.
				</p>
			</div>
			<div>
				<label class="label" for="adguard-username">Username</label>
				<input
					class="input"
					id="adguard-username"
					autocapitalize="off"
					autocomplete="off"
					spellcheck="false"
					bind:value={username}
					oninput={edited}
				/>
				<p class="hint">Leave empty if AdGuard Home has no login.</p>
			</div>
			<div>
				<label class="label" for="adguard-password">Password</label>
				<input
					class="input"
					id="adguard-password"
					type="password"
					autocomplete="new-password"
					placeholder={saved.has_password ? 'Saved' : ''}
					bind:value={password}
					oninput={edited}
				/>
				<p class="hint" class:text-amber-700={askPassword} class:dark:text-amber-400={askPassword}>
					{#if askPassword}
						Enter it again to use another address or username.
					{:else if saved.has_password}
						Leave empty to keep the saved one.
					{:else}
						Stored encrypted. Never shown again.
					{/if}
				</p>
			</div>
		</div>

		<div class="flex flex-col gap-3">
			<label class="flex items-start gap-2 text-sm">
				<input type="checkbox" class="mt-0.5" bind:checked={enabled} />
				<span>
					<span class="font-medium">Use AdGuard Home</span>
					<span class="hint block">
						Turn it on once Test connection works. The features that need AdGuard Home use this
						connection.
					</span>
				</span>
			</label>
			<label class="ml-6 flex items-start gap-2 text-sm">
				<input type="checkbox" class="mt-0.5" bind:checked={syncNames} disabled={!enabled} />
				<span>
					<span class="font-medium">Name clients in AdGuard Home</span>
					<span class="hint block">
						Adds each client under its name and its VPN addresses, so AdGuard Home's query log and
						statistics say "phone" and not 10.8.0.23. Drawbridge changes only the clients it adds
						there, and keeps whatever else you set on them.
					</span>
				</span>
			</label>
		</div>

		<div class="flex flex-wrap items-center gap-2">
			<button class="btn btn-primary" type="submit" disabled={busy !== ''}>
				{busy === 'save' ? 'Saving…' : 'Save connection'}
			</button>
			<button class="btn" type="button" onclick={runTest} disabled={busy !== ''}>
				{busy === 'test' ? 'Testing…' : 'Test connection'}
			</button>
			{#if saved.enabled && saved.sync_names}
				<button class="btn" type="button" onclick={syncNow} disabled={busy !== ''}>
					{busy === 'sync' ? 'Syncing…' : 'Sync now'}
				</button>
			{/if}
			{#if saved.configured}
				{#if confirmRemove}
					<span class="text-sm">Forget the address and the password?</span>
					<button class="btn btn-danger" type="button" onclick={remove} disabled={busy !== ''}>
						{busy === 'remove' ? 'Removing…' : 'Remove'}
					</button>
					<button class="btn" type="button" onclick={() => (confirmRemove = false)}>Cancel</button>
				{:else}
					<button class="btn" type="button" onclick={() => (confirmRemove = true)}>Remove…</button>
				{/if}
			{/if}
		</div>

		<Result {error} {success} />

		{#if saved.enabled && saved.sync_names}
			{@const sync = saved.sync}
			<div class="flex flex-col gap-2 text-sm" data-testid="adguard-sync" aria-live="polite">
				{#if sync.state === 'ok'}
					<p>
						{sync.last_sync ? `Synced ${formatTime(sync.last_sync)}: ` : ''}{sync.synced}
						{sync.synced === 1 ? 'client has' : 'clients have'} their name in AdGuard Home.
					</p>
				{:else if sync.state === 'pending'}
					<p class="hint">Waiting for the first sync…</p>
				{:else if sync.state === 'error'}
					<p class="alert-error" role="alert">
						Couldn't sync: {sync.error}. Drawbridge tries again soon.
					</p>
				{:else if sync.state === 'stopped'}
					<p class="alert-error" role="alert">
						{sync.error}. Drawbridge isn't asking again until the connection changes, or Test
						connection or Sync now shows it works.
					</p>
				{/if}
				{#if sync.conflicts.length > 0}
					<div class="alert-warning" role="status">
						<p class="font-medium">
							{sync.conflicts.length === 1 ? 'One client' : `${sync.conflicts.length} clients`} couldn't
							be named, so Drawbridge left AdGuard Home as it is:
						</p>
						<ul class="list-disc pl-5">
							{#each sync.conflicts as c (c.client_id)}
								<li><span class="font-medium">{c.client}</span>: {c.reason}</li>
							{/each}
						</ul>
						<p>Settle them in AdGuard Home, then press Sync now.</p>
					</div>
				{/if}
			</div>
		{/if}

		{#if test}
			<div class="flex flex-col gap-2 text-sm" data-testid="adguard-test" aria-live="polite">
				{#if test.ok}
					<p class="alert-success" role="status">
						Connected to AdGuard Home {test.version}.
					</p>
					<dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
						<dt class="text-neutral-500 dark:text-neutral-400">DNS server</dt>
						<dd>{test.running ? 'running' : 'not running'}</dd>
						<dt class="text-neutral-500 dark:text-neutral-400">Protection</dt>
						<dd>{test.protection_enabled ? 'on' : 'off'}</dd>
						<dt class="text-neutral-500 dark:text-neutral-400">Query log</dt>
						<dd>{test.query_log ? queryLogText(test.query_log) : "couldn't be read"}</dd>
					</dl>
					{#each test.warnings as w (w)}
						<p class="alert-warning" role="status">{w}</p>
					{/each}
				{:else}
					<p class="alert-error" role="alert">{test.error}</p>
				{/if}
				{#if test.dns.length > 0}
					<div>
						<p class="font-medium">On this server's VPN addresses</p>
						<ul class="flex flex-col gap-1">
							{#each test.dns as r (r.address)}
								<li class="text-xs">
									<span class="font-mono">{r.address}</span>:
									<span
										class={r.answered
											? 'font-medium text-emerald-700 dark:text-emerald-400'
											: 'font-medium text-amber-700 dark:text-amber-400'}
									>
										{r.answered ? 'answers.' : 'no answer.'}
									</span>
									<span class="text-neutral-500 dark:text-neutral-400">{r.detail}</span>
								</li>
							{/each}
						</ul>
					</div>
				{/if}
			</div>
		{/if}
	</form>
{:else if error}
	<p class="alert-error" role="alert">{error}</p>
{/if}
