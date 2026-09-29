<script lang="ts">
	let { children } = $props();
</script>

<main class="mx-auto flex min-h-screen max-w-md flex-col justify-center gap-6 p-6">
	<div class="text-center">
		<h1 class="text-3xl font-semibold tracking-tight">Drawbridge</h1>
		<p class="text-sm text-neutral-500 dark:text-neutral-400">WireGuard VPN Server</p>
	</div>
	{@render children()}
</main>
