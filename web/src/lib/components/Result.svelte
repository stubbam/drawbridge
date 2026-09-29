<script lang="ts">
	// Shows the outcome of a change: an error, the server's warning (the change is saved
	// but not in effect yet), or a success message.
	let {
		error = '',
		warning = '',
		success = ''
	}: { error?: string; warning?: string; success?: string } = $props();
</script>

{#if error}
	<p class="alert-error" role="alert">{error}</p>
{:else if warning}
	<p class="alert-warning" role="status">{warning}</p>
{:else if success}
	<p class="alert-success" role="status">{success}</p>
{/if}
