<script lang="ts">
	import { untrack } from 'svelte';
	import uPlot from 'uplot';
	import 'uplot/dist/uPlot.min.css';
	import { axisSize } from '$lib/chart';
	import { formatChartTime } from '$lib/format';
	import type { ChartSeries } from '$lib/traffic';

	let {
		x,
		series,
		domain,
		step,
		format,
		label,
		empty = 'No traffic in this range'
	}: {
		/** The bucket times, in seconds. */
		x: number[];
		/** One line per client, each with a value for every time in `x`. */
		series: ChartSeries[];
		/** The x axis's start and end, in seconds. */
		domain: [number, number];
		/** Seconds each point covers; a few seconds wide means times show their seconds. */
		step: number;
		/** Formats a value for the y axis and the tooltip. */
		format: (v: number) => string;
		/** The chart's accessible name. The page's heading is what's drawn above it. */
		label: string;
		/** What to say when no client has any traffic. */
		empty?: string;
	} = $props();

	const height = 220;

	let container: HTMLDivElement | undefined;
	let chart: uPlot | undefined;
	// The x axis reads this when it draws, so a new window needs no rebuild.
	let currentDomain: [number, number] = [0, 0];
	// Bumped by a change of theme, which rebuilds the chart.
	let themeTick = $state(0);

	// uPlot sets its colors via JS options, not CSS, so a theme change needs a full rebuild, not
	// just a restyle. Reading the actual "dark" class (rather than importing theme.mode) also
	// catches "auto" mode following a system preference change.
	function colors() {
		const dark = document.documentElement.classList.contains('dark');
		return {
			text: dark ? '#a3a3a3' : '#525252',
			grid: dark ? '#404040' : '#e5e5e5',
			// The cursor's dots get a ring in the card's own color.
			card: dark ? '#171717' : '#ffffff'
		};
	}

	// A line's fill fades from its color at the top to nothing at the floor.
	const fade = (color: string) => (u: uPlot) => {
		const g = u.ctx.createLinearGradient(0, u.bbox.top, 0, u.bbox.top + u.bbox.height);
		g.addColorStop(0, color + '44');
		g.addColorStop(1, color + '08');
		return g;
	};

	// What follows the cursor: the time, then each client's value there. uPlot's own legend sits
	// in the layout; this floats over the plot instead, beside the cursor.
	function tooltip(names: string[], tints: string[]): uPlot.Plugin {
		let tip: HTMLDivElement;
		const text = (cls: string, content: string) => {
			const el = document.createElement('span');
			el.className = cls;
			el.textContent = content;
			return el;
		};
		return {
			hooks: {
				init: (u) => {
					tip = document.createElement('div');
					tip.className =
						'chart-tooltip pointer-events-none absolute z-10 min-w-36 rounded-lg border border-neutral-200 bg-white/95 px-3 py-2 text-sm shadow-lg dark:border-neutral-700 dark:bg-neutral-900/95';
					tip.style.display = 'none';
					u.over.appendChild(tip);
				},
				setCursor: (u) => {
					const { idx, left, top } = u.cursor;
					if (idx == null || left == null || top == null || left < 0) {
						tip.style.display = 'none';
						return;
					}
					const rows = names.map((name, i) => {
						const row = document.createElement('div');
						row.className = 'flex items-center gap-2';
						const bar = document.createElement('span');
						bar.className = 'h-3 w-1 shrink-0 rounded-full';
						bar.style.backgroundColor = tints[i];
						row.append(
							bar,
							text('text-neutral-600 dark:text-neutral-300', name),
							text('ml-auto pl-3 font-medium tabular-nums', format(u.data[i + 1][idx] ?? 0))
						);
						return row;
					});
					tip.replaceChildren(
						text('mb-1 block font-semibold', formatChartTime(u.data[0][idx], step < 60)),
						...rows
					);
					tip.style.display = 'block';
					// Beside the cursor, on whichever side has room, and inside the plot.
					const gap = 14;
					const w = tip.offsetWidth;
					const h = tip.offsetHeight;
					let l = left + gap;
					if (l + w > u.over.clientWidth) l = Math.max(0, left - w - gap);
					const t = Math.max(0, Math.min(top - h / 2, u.over.clientHeight - h));
					tip.style.left = `${l}px`;
					tip.style.top = `${t}px`;
				}
			}
		};
	}

	function build() {
		if (!container || series.length === 0) {
			chart?.destroy();
			chart = undefined;
			return;
		}
		chart?.destroy();
		const c = colors();
		const tints = series.map((s) => s.color);
		const byValue = (_u: uPlot, vals: (number | null)[]) =>
			vals.map((v) => (v == null ? '' : format(v)));
		currentDomain = domain;
		chart = new uPlot(
			{
				width: container.clientWidth || 300,
				height,
				legend: { show: false },
				cursor: {
					y: false,
					drag: { x: false, y: false },
					points: {
						size: 9,
						width: 2,
						fill: (_u, i) => tints[i - 1],
						stroke: () => c.card
					}
				},
				scales: {
					x: { time: true, range: () => currentDomain },
					// From zero, so a quiet chart doesn't stretch noise to fill the plot.
					y: { range: (_u, _min, max) => [0, max > 0 ? max * 1.1 : 1] }
				},
				axes: [
					// Horizontal gridlines only: vertical ones are noise on a time axis.
					{ stroke: c.text, grid: { show: false } },
					{ stroke: c.text, grid: { stroke: c.grid }, values: byValue, size: axisSize }
				],
				series: [
					{},
					...series.map((s) => ({
						label: s.name,
						stroke: s.color,
						width: 2,
						fill: fade(s.color),
						// A monotone curve: smooth, and it never dips below a quiet stretch's zero.
						paths: uPlot.paths.spline?.(),
						points: { show: false }
					}))
				],
				plugins: [
					tooltip(
						series.map((s) => s.name),
						tints
					)
				]
			},
			[x, ...series.map((s) => s.values)],
			container
		);
	}

	// What a rebuild is for: the lines (their names and colors), how wide a point is, and the
	// theme. New values for the same lines go in with setData below, so the chart under the
	// cursor isn't torn down by every refresh.
	let shape = $derived(JSON.stringify([series.map((s) => [s.id, s.name, s.color]), step]));

	$effect(() => {
		void shape;
		void themeTick;
		untrack(build);
		return () => chart?.destroy();
	});

	$effect(() => {
		const data: uPlot.AlignedData = [x, ...series.map((s) => s.values)];
		currentDomain = domain;
		chart?.setData(data);
	});

	$effect(() => {
		if (!container) return;
		const observer = new ResizeObserver(() => {
			if (chart && container && container.clientWidth > 0) {
				chart.setSize({ width: container.clientWidth, height });
			}
		});
		observer.observe(container);
		return () => observer.disconnect();
	});

	$effect(() => {
		const observer = new MutationObserver(() => themeTick++);
		observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] });
		return () => observer.disconnect();
	});
</script>

<div>
	{#if series.length === 0}
		<div
			class="grid place-items-center text-sm text-neutral-500 dark:text-neutral-400"
			style:height="{height}px"
		>
			{empty}
		</div>
	{/if}
	<div
		bind:this={container}
		role="img"
		aria-label="{label} chart"
		hidden={series.length === 0}
	></div>
	{#if series.length > 0}
		<ul
			class="mt-3 flex flex-wrap justify-center gap-x-4 gap-y-1 text-sm text-neutral-600 dark:text-neutral-300"
			aria-label="{label} legend"
		>
			{#each series as s (s.id)}
				<li class="flex items-center gap-1.5">
					<span class="size-2.5 rounded-xs" style:background-color={s.color}></span>
					{s.name}
				</li>
			{/each}
		</ul>
	{/if}
</div>
