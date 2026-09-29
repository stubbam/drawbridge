<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { api } from '$lib/api';
	import { errorMessage } from '$lib/errors';
	import Result from '$lib/components/Result.svelte';

	let username = $state('');
	let password = $state('');
	let error = $state('');
	let busy = $state(false);

	onMount(async () => {
		try {
			if ((await api.setupStatus()).needed) await goto(resolve('/setup'));
		} catch {
			// The form reports connection problems when it's used.
		}
	});

	/** Where to go after logging in: the page that sent us here, if it's on this site. */
	function next(): string {
		const n = page.url.searchParams.get('next');
		if (n) {
			const url = new URL(n, page.url.origin);
			if (url.origin === page.url.origin) return url.pathname + url.search;
		}
		return resolve('/');
	}

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			await api.login(username, password);
			// next() is a path on this site; it's checked above.
			// eslint-disable-next-line svelte/no-navigation-without-resolve
			await goto(next());
		} catch (err) {
			error = errorMessage(err);
			password = '';
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>Log In · Drawbridge</title></svelte:head>

<form class="card flex flex-col gap-4" onsubmit={submit}>
	<h2 class="text-lg font-semibold">Log In</h2>
	<div>
		<label class="label" for="username">Username</label>
		<input
			class="input"
			id="username"
			name="username"
			autocomplete="username"
			required
			bind:value={username}
		/>
	</div>
	<div>
		<label class="label" for="password">Password</label>
		<input
			class="input"
			id="password"
			name="password"
			type="password"
			autocomplete="current-password"
			required
			bind:value={password}
		/>
	</div>
	<Result {error} />
	<button class="btn btn-primary" type="submit" disabled={busy}>
		{busy ? 'Logging In…' : 'Log In'}
	</button>
	<p class="hint">
		Locked out? On the server, <code>sudo drawbridge admin reset-password</code> sets a new password.
	</p>
</form>
