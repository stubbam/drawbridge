<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { api, type Settings } from '$lib/api';
	import { errorMessage } from '$lib/errors';
	import Result from '$lib/components/Result.svelte';

	// Step 1 creates the admin account; step 2 sets the endpoint clients connect to.
	let step = $state<1 | 2>(1);
	let token = $state('');
	let username = $state('admin');
	let password = $state('');
	let confirm = $state('');
	let endpoint = $state('');
	let settings = $state<Settings>();
	let error = $state('');
	let busy = $state(false);

	onMount(async () => {
		try {
			if (!(await api.setupStatus()).needed) await goto(resolve('/login'));
		} catch (err) {
			error = errorMessage(err);
		}
	});

	async function createAccount(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		if (password !== confirm) {
			error = "The passwords don't match.";
			return;
		}
		busy = true;
		try {
			await api.setup(token, username, password);
			settings = await api.server();
			endpoint = settings.endpoint_host;
			step = 2;
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}

	async function saveEndpoint(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		busy = true;
		try {
			if (endpoint.trim()) await api.updateServer({ endpoint_host: endpoint.trim() });
			await goto(resolve('/'));
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>Set Up · Drawbridge</title></svelte:head>

{#if step === 1}
	<form class="card flex flex-col gap-4" onsubmit={createAccount}>
		<div>
			<h2 class="text-lg font-semibold">Create the Admin Account</h2>
			<p class="hint">Step 1 of 2</p>
		</div>
		<div>
			<label class="label" for="token">Setup token</label>
			<input
				class="input font-mono"
				id="token"
				autocomplete="one-time-code"
				placeholder="ABCDE-FGHJK-LMNPQ-RSTUV"
				required
				bind:value={token}
			/>
			<p class="hint">
				Printed when Drawbridge was installed. To see it again, run
				<code>sudo drawbridge admin setup-token</code> on the server.
			</p>
		</div>
		<div>
			<label class="label" for="username">Username</label>
			<input
				class="input"
				id="username"
				autocomplete="username"
				required
				maxlength="32"
				bind:value={username}
			/>
		</div>
		<div>
			<label class="label" for="password">Password</label>
			<input
				class="input"
				id="password"
				type="password"
				autocomplete="new-password"
				required
				minlength="10"
				bind:value={password}
			/>
			<p class="hint">At least 10 characters.</p>
		</div>
		<div>
			<label class="label" for="confirm">Password again</label>
			<input
				class="input"
				id="confirm"
				type="password"
				autocomplete="new-password"
				required
				bind:value={confirm}
			/>
		</div>
		<Result {error} />
		<button class="btn btn-primary" type="submit" disabled={busy}>
			{busy ? 'Creating…' : 'Create Account'}
		</button>
	</form>
{:else}
	<form class="card flex flex-col gap-4" onsubmit={saveEndpoint}>
		<div>
			<h2 class="text-lg font-semibold">Where Clients Connect</h2>
			<p class="hint">Step 2 of 2</p>
		</div>
		<div>
			<label class="label" for="endpoint">Public address</label>
			<input
				class="input"
				id="endpoint"
				placeholder="vpn.example.com"
				autocapitalize="off"
				spellcheck="false"
				bind:value={endpoint}
			/>
			<p class="hint">
				The domain name (or public IP address) that points to your home. Clients connect to it on
				UDP port {settings?.listen_port ?? 51820}, which your router must forward to this server.
				You can change it later in Settings.
			</p>
		</div>
		<Result {error} />
		<div class="flex gap-2">
			<button class="btn btn-primary flex-1" type="submit" disabled={busy}>Finish</button>
		</div>
	</form>
{/if}
