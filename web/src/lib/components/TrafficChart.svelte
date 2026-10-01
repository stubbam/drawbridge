<script lang="ts">
	import uPlot from 'uplot';
	import 'uplot/dist/uPlot.min.css';
	import type { TrafficRange, TrafficSample } from '$lib/api';
	import { axisSize } from '$lib/chart';
	import { formatBytes } from '$lib/format';
	import { toUplotData, xRange } from '$lib/traffic';

	let {
		data,
		range,
		label
	}: {
		data: TrafficSample[];
		/** The range `data` was loaded for. */ range: TrafficRange;
		/** The chart's accessible name. The page's heading is what's drawn above it. */
		label: string;
	} = $props();

	let container: HTMLDivElement | undefined;
	let chart: uPlot | undefined;

	// uPlot sets its colors via JS options, not CSS, so a theme change needs a full
	// rebuild, not just a restyle. Reading the actual "dark" class (rather than importing
	// theme.mode) also catches "auto" mode following a system preference change.
	function colors() {
		const dark = document.documentElement.classList.contains('dark');
		return {
			text: dark ? '#a3a3a3' : '#525252',
			grid: dark ? '#404040' : '#e5e5e5',
			receive: dark ? '#818cf8' : '#4f46e5',
			send: dark ? '#34d399' : '#059669'
		};
	}

	function build() {
		if (!container) return;
		chart?.destroy();
		const c = colors();
		const byBytes = (_u: uPlot, vals: (number | null)[]) =>
			vals.map((v) => (v == null ? '' : formatBytes(v)));
		chart = new uPlot(
			{
				width: container.clientWidth || 300,
				height: 220,
				scales: { x: { time: true, range: () => xRange(range, Date.now()) } },
				axes: [
					{ stroke: c.text, grid: { stroke: c.grid } },
					{ stroke: c.text, grid: { stroke: c.grid }, values: byBytes, size: axisSize }
				],
				series: [
					{},
					{
						label: 'Received',
						stroke: c.receive,
						width: 2,
						value: (_u, v) => (v == null ? '—' : formatBytes(v))
					},
					{
						label: 'Sent',
						stroke: c.send,
						width: 2,
						value: (_u, v) => (v == null ? '—' : formatBytes(v))
					}
				]
			},
			toUplotData(data),
			container
		);
	}

	$effect(() => {
		// Re-reads `data` and `range`, so this reruns whenever either changes.
		build();
		return () => chart?.destroy();
	});

	$effect(() => {
		if (!container) return;
		const observer = new ResizeObserver(() => {
			if (chart && container) chart.setSize({ width: container.clientWidth, height: 220 });
		});
		observer.observe(container);
		return () => observer.disconnect();
	});

	$effect(() => {
		const observer = new MutationObserver(build);
		observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] });
		return () => observer.disconnect();
	});
</script>

<div bind:this={container} role="img" aria-label="{label} chart"></div>
