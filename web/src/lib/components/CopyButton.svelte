<script lang="ts">
	let { text, label = 'Copy' }: { text: string; label?: string } = $props();
	let copied = $state(false);

	async function copy() {
		await navigator.clipboard.writeText(text);
		copied = true;
		setTimeout(() => (copied = false), 1500);
	}
</script>

<button type="button" class="btn px-2 py-1 text-xs" onclick={copy} aria-live="polite">
	{copied ? 'Copied' : label}
</button>
