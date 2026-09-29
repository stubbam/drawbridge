<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { api } from '$lib/api';
	import { errorMessage } from '$lib/errors';
	import Result from './Result.svelte';

	let { open = $bindable(false) }: { open?: boolean } = $props();

	let dialog = $state<HTMLDialogElement>();
	let name = $state('');
	let error = $state('');
	let adding = $state(false);

	$effect(() => {
		if (open) {
			error = '';
			dialog?.showModal();
		} else {
			dialog?.close();
		}
	});

	// The dialog closes itself on Escape, or on the backdrop click below; keep `open` in
	// sync either way.
	function onClose() {
		open = false;
		name = '';
	}

	function onBackdropClick(e: MouseEvent) {
		if (e.target === dialog) dialog.close();
	}

	async function add(e: SubmitEvent) {
		e.preventDefault();
		adding = true;
		error = '';
		try {
			const res = await api.addClient(name.trim());
			dialog?.close();
			// The client's page shows its QR code at once for a client that was just added.
			await goto(resolve('/(app)/clients/[id]', { id: res.client.id }), {
				state: { justAdded: true }
			});
		} catch (err) {
			error = errorMessage(err);
		} finally {
			adding = false;
		}
	}
</script>

<dialog
	bind:this={dialog}
	onclose={onClose}
	onclick={onBackdropClick}
	class="m-auto w-full max-w-sm rounded-xl border border-neutral-200 bg-white p-0 shadow-lg backdrop:bg-black/40 dark:border-neutral-800 dark:bg-neutral-900"
>
	<form class="flex flex-col gap-3 p-5" onsubmit={add}>
		<h2 class="font-semibold">Add a Client</h2>
		<div>
			<label class="label" for="add-client-name">Name</label>
			<input
				class="input"
				id="add-client-name"
				placeholder="Phone, laptop, Alex's iPad…"
				maxlength="64"
				required
				bind:value={name}
			/>
			<p class="hint">Letters, digits, spaces, and . _ ' -. It gets its own addresses and keys.</p>
		</div>
		<Result {error} />
		<div class="flex justify-end gap-2">
			<button type="button" class="btn" onclick={() => dialog?.close()}>Cancel</button>
			<button class="btn btn-primary" type="submit" disabled={adding}>
				{adding ? 'Adding…' : 'Add'}
			</button>
		</div>
	</form>
</dialog>
