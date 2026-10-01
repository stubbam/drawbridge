<script lang="ts">
	import { api, type TrafficHistory, type TrafficRange } from '$lib/api';
	import { errorMessage } from '$lib/errors';
	import { formatBitrate, formatBytes } from '$lib/format';
	import { poll } from '$lib/poll';
	import { chartRange } from '$lib/range.svelte';
	import { buildChartData, chartSeries, refreshMs } from '$lib/traffic';
	import NetworkChart from '$lib/components/NetworkChart.svelte';
	import RangeSelect from '$lib/components/RangeSelect.svelte';

	// The range `history` holds, which lags the chosen range until the new range's data arrives.
	let shown = $state<TrafficRange>(chartRange.value);
	let history = $state<TrafficHistory>();
	let error = $state('');

	async function load() {
		const asked = chartRange.value;
		try {
			const h = await api.trafficByClient(asked);
			// A slower response to an earlier range mustn't replace the current one.
			if (asked !== chartRange.value) return;
			history = h;
			shown = asked;
			error = '';
		} catch (err) {
			error = errorMessage(err);
		}
	}

	// Changing the range restarts this poll, for an immediate refetch.
	$effect(() => poll(load, refreshMs(chartRange.value)));

	let data = $derived(history ? buildChartData(history, shown) : undefined);

	// The two rates and the two running totals, each for one direction: what the server received
	// from the clients and what it sent them, as the rest of Drawbridge counts them.
	const charts = [
		{
			id: 'received',
			title: 'Received',
			note: 'Data rate received from each client, in bits per second',
			direction: 'received',
			kind: 'rate',
			format: formatBitrate
		},
		{
			id: 'sent',
			title: 'Sent',
			note: 'Data rate sent to each client, in bits per second',
			direction: 'sent',
			kind: 'rate',
			format: formatBitrate
		},
		{
			id: 'cumulative-received',
			title: 'Cumulative Received',
			note: 'Total data received from each client since the start of the range',
			direction: 'received',
			kind: 'cumulative',
			format: formatBytes
		},
		{
			id: 'cumulative-sent',
			title: 'Cumulative Sent',
			note: 'Total data sent to each client since the start of the range',
			direction: 'sent',
			kind: 'cumulative',
			format: formatBytes
		}
	] as const;
</script>

<div class="flex flex-wrap items-center justify-between gap-2">
	<h1 class="text-2xl font-semibold tracking-tight">Charts</h1>
	<RangeSelect id="charts-range" />
</div>

{#if error && !history}
	<p class="alert-error" role="alert">{error}</p>
{/if}

<div class="flex flex-col gap-6">
	{#each charts as c (c.id)}
		<section class="card flex flex-col gap-3" aria-labelledby="{c.id}-heading">
			<div>
				<h2 id="{c.id}-heading" class="font-semibold">{c.title}</h2>
				<p class="text-sm text-neutral-500 dark:text-neutral-400">{c.note}</p>
			</div>
			<NetworkChart
				x={data?.x ?? []}
				series={data ? chartSeries(data, c.direction, c.kind) : []}
				domain={data?.domain ?? [0, 0]}
				step={data?.step ?? 0}
				range={shown}
				format={c.format}
				label={c.title}
				empty={data ? 'No traffic in this range' : 'Loading…'}
			/>
		</section>
	{/each}
</div>
