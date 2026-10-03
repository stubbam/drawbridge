<script lang="ts">
	// A client's recent DNS queries, from AdGuard Home's query log (docs/PLAN.md §6.3). The server
	// keeps only the queries from exactly this client's addresses. It's read when the page opens
	// and when asked again, not on a timer: each read is a few requests to AdGuard Home.
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { api, type DNSLog, type DNSQuery } from '$lib/api';
	import { adguardLogsUrl } from '$lib/adguard';
	import { errorMessage } from '$lib/errors';
	import { formatClock, formatTime } from '$lib/format';

	let { id }: { id: string } = $props();

	let log = $state<DNSLog>();
	let error = $state('');
	let loading = $state(false);
	let readAt = $state<Date>();

	async function load() {
		loading = true;
		error = '';
		try {
			log = await api.clientDnsLog(id);
			readAt = new Date();
		} catch (err) {
			error = errorMessage(err);
		} finally {
			loading = false;
		}
	}

	onMount(load);

	const link = $derived(
		log?.adguard_url ? adguardLogsUrl(log.adguard_url, location.hostname, log.addresses[0]) : ''
	);

	/** What AdGuard Home answered, in a few words: blocked, the error code, or the addresses. */
	function answer(q: DNSQuery): string {
		if (q.status !== 'NOERROR') return q.status;
		if (q.answers.length === 0) return 'no records';
		const shown = q.answers.slice(0, 2).join(', ');
		return q.answers.length > 2 ? `${shown} +${q.answers.length - 2}` : shown;
	}
</script>

<section class="card flex flex-col gap-3" aria-labelledby="dns-log-heading">
	<div class="flex flex-wrap items-center justify-between gap-2">
		<h2 id="dns-log-heading" class="font-semibold">Recent DNS Queries</h2>
		{#if log && log.state !== 'off'}
			<div class="flex items-center gap-3 text-sm">
				{#if readAt}
					<span class="text-neutral-500 dark:text-neutral-400">
						Read {formatClock(readAt, true)}
					</span>
				{/if}
				<button class="btn" type="button" onclick={load} disabled={loading}>
					{loading ? 'Reading…' : 'Refresh'}
				</button>
			</div>
		{/if}
	</div>

	{#if error}
		<p class="alert-error" role="alert">{error}</p>
	{:else if !log}
		<p class="text-sm text-neutral-500">Loading…</p>
	{:else if log.state === 'off'}
		<p class="hint">
			What this client looks up comes from AdGuard Home's query log.
			<a class="font-medium underline" href={resolve('/settings')}
				>Turn on AdGuard Home in Settings</a
			>
			to see it here.
		</p>
	{:else if log.state === 'error'}
		<p class="alert-error" role="alert">{log.error}</p>
	{:else}
		{#each log.warnings as w (w)}
			<p class="alert-warning" role="status">{w}</p>
		{/each}
		{#if log.queries.length === 0}
			{#if log.warnings.length === 0}
				<p class="text-sm text-neutral-500">
					AdGuard Home's log has no queries from this client's addresses.
				</p>
			{/if}
		{:else}
			<ul class="flex flex-col divide-y divide-neutral-100 text-sm dark:divide-neutral-800">
				{#each log.queries as q (q.time + q.address + q.domain + q.type)}
					<li class="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5 py-2">
						<span class="min-w-0">
							<span class="font-mono text-xs break-all">{q.domain}</span>
							<span class="text-xs text-neutral-500 dark:text-neutral-400">{q.type}</span>
							<br />
							{#if q.blocked}
								<span class="text-xs font-medium text-red-700 dark:text-red-400">Blocked</span>
								{#if q.rule}
									<span class="font-mono text-xs break-all text-neutral-500 dark:text-neutral-400"
										>{q.rule}</span
									>
								{/if}
							{:else}
								<span
									class="text-xs {q.status === 'NOERROR'
										? 'text-neutral-500 dark:text-neutral-400'
										: 'font-medium text-amber-700 dark:text-amber-400'}"
									>{answer(q)}{q.cached ? ' · cached' : ''}</span
								>
							{/if}
						</span>
						<time class="text-neutral-500 dark:text-neutral-400" datetime={q.time}
							>{formatTime(q.time)}</time
						>
					</li>
				{/each}
			</ul>
		{/if}
		{#if link}
			<p class="text-sm">
				<a class="font-medium underline" href={link} target="_blank" rel="noopener external"
					>Open this client's queries in AdGuard Home</a
				>
			</p>
		{/if}
	{/if}
</section>
