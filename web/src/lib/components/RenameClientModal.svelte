<script lang="ts">
	import { untrack } from 'svelte';
	import { api } from '$lib/api';
	import { errorMessage } from '$lib/errors';
	import Result from './Result.svelte';

	let {
		open = $bindable(false),
		id,
		current,
		onrenamed
	}: {
		open?: boolean;
		/** The client being renamed. */
		id: string;
		/** Its name now. */
		current: string;
		/** Called once the new name is saved. */
		onrenamed: () => void;
	} = $props();

	let dialog = $state<HTMLDialogElement>();
	let input = $state<HTMLInputElement>();
	let name = $state('');
	let error = $state('');
	let saving = $state(false);

	$effect(() => {
		if (open) {
			error = '';
			// Start from the current name, selected; a later poll mustn't reset what's being typed.
			name = untrack(() => current);
			dialog?.showModal();
			input?.select();
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

	async function save(e: SubmitEvent) {
		e.preventDefault();
		saving = true;
		error = '';
		try {
			await api.renameClient(id, name.trim());
			dialog?.close();
			onrenamed();
		} catch (err) {
			error = errorMessage(err);
		} finally {
			saving = false;
		}
	}
</script>

<dialog
	bind:this={dialog}
	onclose={onClose}
	onclick={onBackdropClick}
	aria-labelledby="rename-client-heading"
	class="m-auto w-full max-w-sm rounded-xl border border-neutral-200 bg-white p-0 shadow-lg backdrop:bg-black/40 dark:border-neutral-800 dark:bg-neutral-900"
>
	<form class="flex flex-col gap-3 p-5" onsubmit={save}>
		<h2 id="rename-client-heading" class="font-semibold">Rename Client</h2>
		<div>
			<label class="label" for="rename-client-name">Name</label>
			<input
				class="input"
				id="rename-client-name"
				maxlength="64"
				required
				bind:this={input}
				bind:value={name}
			/>
			<p class="hint">The name is only for you: the config and the keys stay the same.</p>
		</div>
		<Result {error} />
		<div class="flex justify-end gap-2">
			<button type="button" class="btn" onclick={() => dialog?.close()}>Cancel</button>
			<button class="btn btn-primary" type="submit" disabled={saving}>
				{saving ? 'Renaming…' : 'Rename'}
			</button>
		</div>
	</form>
</dialog>
