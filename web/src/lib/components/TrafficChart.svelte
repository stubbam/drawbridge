<script lang="ts">
	import uPlot from 'uplot';
	import 'uplot/dist/uPlot.min.css';
	import type { TrafficSample } from '$lib/api';
	import { formatBytes } from '$lib/format';
	import { toUplotData } from '$lib/traffic';

	let { data, title }: { data: TrafficSample[]; title: string } = $props();

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

	// uPlot's default y-axis width is a fixed 50px, too narrow for a formatted byte string
	// like "150 MB" or "1.5 GB" — it doesn't wrap or shrink the text, so it just clips.
	// This is uPlot's own documented recipe for sizing an axis to its longest label
	// instead: measure it with the axis's own font, so it always fits.
	const axisSize: uPlot.Axis.Size = (self, values, axisIdx) => {
		const axis = self.axes[axisIdx];
		let size = (axis.ticks?.size ?? 0) + (axis.gap ?? 0);
		const longest = (values ?? []).reduce((a, b) => (b.length > a.length ? b : a), '');
		if (longest && axis.font) {
			self.ctx.save();
			self.ctx.font = axis.font[0];
			size += self.ctx.measureText(longest).width / devicePixelRatio;
			self.ctx.restore();
		}
		return Math.ceil(size);
	};

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
				title,
				scales: { x: { time: true } },
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
		// Re-reads `data` and `title`, so this reruns whenever either changes.
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

<div bind:this={container} role="img" aria-label="{title} chart"></div>
