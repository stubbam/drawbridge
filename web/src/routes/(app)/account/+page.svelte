<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { api, type Session } from '$lib/api';
	import { errorMessage } from '$lib/errors';
	import { describeUserAgent, formatAgo, formatTime } from '$lib/format';
	import APITokens from '$lib/components/APITokens.svelte';
	import Result from '$lib/components/Result.svelte';

	let { data } = $props();

	let sessions = $state<Session[]>([]);
	let sessionsError = $state('');
	let current = $state('');
	let next = $state('');
	let again = $state('');
	let error = $state('');
	let success = $state('');
	let busy = $state(false);
	let now = $state(Date.now());

	async function loadSessions() {
		try {
			sessions = await api.sessions();
			now = Date.now();
		} catch (err) {
			sessionsError = errorMessage(err);
		}
	}
	onMount(loadSessions);

	async function changePassword(e: SubmitEvent) {
		e.preventDefault();
		error = success = '';
		if (next !== again) {
			error = "The new passwords don't match.";
			return;
		}
		busy = true;
		try {
			await api.changePassword(current, next);
			current = next = again = '';
			success = 'Password changed. Every other session is logged out.';
			await loadSessions();
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}

	async function revoke(s: Session) {
		sessionsError = '';
		try {
			await api.revokeSession(s.id);
			if (s.current) {
				await goto(resolve('/login'));
				return;
			}
			await loadSessions();
		} catch (err) {
			sessionsError = errorMessage(err);
		}
	}
</script>

<svelte:head><title>Account · Drawbridge</title></svelte:head>

<h1 class="text-2xl font-semibold tracking-tight">Account</h1>

<section class="card flex flex-col gap-2 text-sm" aria-labelledby="user-heading">
	<h2 id="user-heading" class="font-semibold">{data.me.user.username}</h2>
	<p class="text-neutral-600 dark:text-neutral-400">
		Admin since {formatTime(data.me.user.created_at)}. This session ends after an hour without use,
		and at {formatTime(data.me.session.expires_at)} at the latest.
	</p>
</section>

<form class="card flex max-w-xl flex-col gap-4" onsubmit={changePassword}>
	<h2 class="font-semibold">Change the Password</h2>
	<input type="hidden" autocomplete="username" value={data.me.user.username} />
	<div>
		<label class="label" for="current">Current password</label>
		<input
			class="input"
			id="current"
			type="password"
			autocomplete="current-password"
			required
			bind:value={current}
		/>
	</div>
	<div>
		<label class="label" for="new">New password</label>
		<input
			class="input"
			id="new"
			type="password"
			autocomplete="new-password"
			minlength="10"
			required
			bind:value={next}
		/>
		<p class="hint">At least 10 characters.</p>
	</div>
	<div>
		<label class="label" for="again">New password again</label>
		<input
			class="input"
			id="again"
			type="password"
			autocomplete="new-password"
			required
			bind:value={again}
		/>
	</div>
	<Result {error} {success} />
	<button class="btn btn-primary self-start" type="submit" disabled={busy}>Change Password</button>
</form>

<section class="card flex flex-col gap-3" aria-labelledby="sessions-heading">
	<h2 id="sessions-heading" class="font-semibold">Logged-in browsers</h2>
	<Result error={sessionsError} />
	<ul class="flex flex-col divide-y divide-neutral-100 text-sm dark:divide-neutral-800">
		{#each sessions as s (s.id)}
			<li class="flex flex-wrap items-center justify-between gap-3 py-2">
				<div>
					<p class="font-medium" title={s.user_agent}>
						{describeUserAgent(s.user_agent)}
						{#if s.current}<span class="ml-1 text-xs text-emerald-600">(this one)</span>{/if}
					</p>
					<p class="text-xs text-neutral-500 dark:text-neutral-400">
						From {s.ip} · logged in {formatTime(s.created_at)} · last used {formatAgo(
							s.last_seen_at,
							now
						)}
					</p>
				</div>
				<button type="button" class="btn px-2.5 py-1" onclick={() => revoke(s)}>
					{s.current ? 'Log out' : 'Log out this browser'}
				</button>
			</li>
		{/each}
	</ul>
</section>

<APITokens username={data.me.user.username} />
