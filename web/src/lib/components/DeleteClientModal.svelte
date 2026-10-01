<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { api } from '$lib/api';
	import { errorMessage } from '$lib/errors';
	import Result from './Result.svelte';

	let {
		open = $bindable(false),
		id,
		name
	}: {
		open?: boolean;
		/** The client being deleted. */
		id: string;
		name: string;
	} = $props();

	let dialog = $state<HTMLDialogElement>();
	let error = $state('');
	let deleting = $state(false);

	$effect(() => {
		if (open) {
			error = '';
			dialog?.showModal();
		} else {
			dialog?.close();
		}
	});

	// The dialog closes itself on Escape, or on the backdrop click below; keep `open` in sync
	// either way.
	function onClose() {
		open = false;
	}

	function onBackdropClick(e: MouseEvent) {
		if (e.target === dialog) dialog.close();
	}

	async function remove() {
		deleting = true;
		error = '';
		try {
			await api.deleteClient(id);
			dialog?.close();
			await goto(resolve('/clients'));
		} catch (err) {
			error = errorMessage(err);
		} finally {
			deleting = false;
		}
	}
</script>

<dialog
	bind:this={dialog}
	onclose={onClose}
	onclick={onBackdropClick}
	aria-labelledby="delete-client-heading"
	class="m-auto w-full max-w-sm rounded-xl border border-neutral-200 bg-white p-0 shadow-lg backdrop:bg-black/40 dark:border-neutral-800 dark:bg-neutral-900"
>
	<div class="flex flex-col gap-3 p-5">
		<h2 id="delete-client-heading" class="font-semibold">Delete Client</h2>
		<p class="text-sm">
			Delete <strong>{name}</strong>? Its config stops working at once, and this can't be undone.
		</p>
		<Result {error} />
		<div class="flex justify-end gap-2">
			<button type="button" class="btn" onclick={() => dialog?.close()}>Cancel</button>
			<button type="button" class="btn btn-danger" disabled={deleting} onclick={remove}>
				{deleting ? 'Deleting…' : 'Delete'}
			</button>
		</div>
	</div>
</dialog>
