<script lang="ts">
	// Read-only API tokens (docs/PLAN.md §6.5), for a dashboard such as Homepage that can't log in.
	// The secret is shown once, when the token is made, and is kept only here, in memory, until the
	// admin says they have it.
	import { onMount } from 'svelte';
	import { api, type APIToken, type NewAPITokenResult } from '$lib/api';
	import { errorMessage } from '$lib/errors';
	import { formatAgo, formatTime } from '$lib/format';
	import { homepageWidget } from '$lib/tokens';
	import CopyButton from './CopyButton.svelte';
	import Result from './Result.svelte';

	let { username }: { username: string } = $props();

	let tokens = $state<APIToken[]>([]);
	let listError = $state('');
	let name = $state('');
	let password = $state('');
	let error = $state('');
	let busy = $state(false);
	let made = $state<NewAPITokenResult>();
	let confirming = $state('');
	let now = $state(Date.now());

	async function load() {
		try {
			tokens = await api.apiTokens();
			now = Date.now();
		} catch (err) {
			listError = errorMessage(err);
		}
	}
	onMount(load);

	async function make(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		busy = true;
		try {
			made = await api.createApiToken(name.trim(), password);
			name = password = '';
			await load();
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}

	async function revoke(t: APIToken) {
		listError = '';
		try {
			await api.revokeApiToken(t.id);
			confirming = '';
			if (made?.token.id === t.id) made = undefined;
			await load();
		} catch (err) {
			listError = errorMessage(err);
		}
	}
</script>

<section class="card flex flex-col gap-4" aria-labelledby="tokens-heading">
	<div>
		<h2 id="tokens-heading" class="font-semibold">API Tokens</h2>
		<p class="hint">
			A token lets a dashboard such as Homepage read the server's status, the clients, and their
			traffic without logging in. It can't read a client's config or DNS log, can't change anything,
			and can't make tokens. It's shown once, and it works from the same networks as this page.
			Revoke one here the moment it leaks. Changing your password doesn't revoke tokens, but
			resetting it from the command line does.
		</p>
	</div>

	{#if made}
		<div class="alert-warning flex flex-col gap-3" role="status" data-testid="new-token">
			<p class="font-medium">
				Copy the token for "{made.token.name}" now. It won't be shown again.
			</p>
			<div class="flex flex-wrap items-center gap-2">
				<code
					class="mono rounded bg-white/60 px-2 py-1 text-sm break-all dark:bg-black/30"
					data-testid="new-token-secret">{made.secret}</code
				>
				<CopyButton text={made.secret} />
			</div>
			<details>
				<summary class="cursor-pointer text-sm font-medium">For Homepage</summary>
				<div class="mt-2 flex flex-col gap-2">
					<p class="text-sm">
						In <span class="mono">services.yaml</span>, under the service:
					</p>
					<pre
						class="mono overflow-x-auto rounded bg-white/60 p-2 text-xs dark:bg-black/30">{homepageWidget(
							location.origin,
							made.secret
						)}</pre>
					<div>
						<CopyButton text={homepageWidget(location.origin, made.secret)} label="Copy this" />
					</div>
					<p class="text-xs">
						Homepage has to trust this server's certificate and be allowed in:
						<a
							class="font-medium underline"
							href="https://github.com/stuffam/drawbridge/blob/main/docs/api-tokens.md"
							target="_blank"
							rel="noopener external">how, and what its errors mean</a
						>.
					</p>
				</div>
			</details>
			<button class="btn self-start" type="button" onclick={() => (made = undefined)}>
				I've copied it
			</button>
		</div>
	{/if}

	<Result error={listError} />
	{#if tokens.length === 0}
		<p class="text-sm text-neutral-500">No tokens yet.</p>
	{:else}
		<ul class="flex flex-col divide-y divide-neutral-100 text-sm dark:divide-neutral-800">
			{#each tokens as t (t.id)}
				<li class="flex flex-wrap items-center justify-between gap-3 py-2">
					<div>
						<p class="font-medium">
							{t.name}
							<span class="mono ml-1 text-xs font-normal text-neutral-500 dark:text-neutral-400"
								>{t.prefix}…</span
							>
						</p>
						<p class="text-xs text-neutral-500 dark:text-neutral-400">
							Read-only · made {formatTime(t.created_at)} ·
							{t.last_used_at ? `last used ${formatAgo(t.last_used_at, now)}` : 'never used'}
						</p>
					</div>
					{#if confirming === t.id}
						<div class="flex items-center gap-2">
							<span class="text-xs">Anything using it stops working.</span>
							<button class="btn btn-danger px-2.5 py-1" type="button" onclick={() => revoke(t)}
								>Revoke</button
							>
							<button class="btn px-2.5 py-1" type="button" onclick={() => (confirming = '')}
								>Cancel</button
							>
						</div>
					{:else}
						<button
							class="btn px-2.5 py-1"
							type="button"
							aria-label={`Revoke ${t.name}`}
							onclick={() => (confirming = t.id)}>Revoke…</button
						>
					{/if}
				</li>
			{/each}
		</ul>
	{/if}

	<form class="flex max-w-xl flex-col gap-3" onsubmit={make}>
		<h3 class="text-sm font-semibold">Make a Token</h3>
		<input type="hidden" autocomplete="username" value={username} />
		<div>
			<label class="label" for="token-name">Name</label>
			<input
				class="input"
				id="token-name"
				maxlength="64"
				required
				placeholder="Homepage"
				autocomplete="off"
				bind:value={name}
			/>
			<p class="hint">What it's for, so you know which to revoke.</p>
		</div>
		<div>
			<label class="label" for="token-password">Your password</label>
			<input
				class="input"
				id="token-password"
				type="password"
				autocomplete="current-password"
				required
				bind:value={password}
			/>
			<p class="hint">Again, because a token outlasts this login.</p>
		</div>
		<Result {error} />
		<button class="btn btn-primary self-start" type="submit" disabled={busy}>
			{busy ? 'Making…' : 'Make Token'}
		</button>
	</form>
</section>
