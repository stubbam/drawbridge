import type { TrafficHistory, TrafficRange, TrafficSeries } from './api';

export const rangeLabels: Record<TrafficRange, string> = {
	'1m': '1 Minute',
	'1h': '1 Hour',
	'12h': '12 Hours',
	'24h': '24 Hours',
	'7d': '1 Week',
	'30d': '30 Days',
	'90d': '90 Days'
};

export const trafficRanges: TrafficRange[] = ['1m', '1h', '12h', '24h', '7d', '30d', '90d'];

/** How long each range is, in seconds. */
export const rangeSeconds: Record<TrafficRange, number> = {
	'1m': 60,
	'1h': 3600,
	'12h': 12 * 3600,
	'24h': 24 * 3600,
	'7d': 7 * 24 * 3600,
	'30d': 30 * 24 * 3600,
	'90d': 90 * 24 * 3600
};

/**
 * How often to refetch a range. The daemon samples every 5 s and keeps the last minute of those
 * polls, so only that range has anything new sooner than the minute-wide buckets every other
 * range reads.
 */
export function refreshMs(range: TrafficRange): number {
	return range === '1m' ? 5000 : 60000;
}

/** What a chart plots for one client: the values line up with `ChartData.x`. */
export interface ChartSeries {
	id: string;
	name: string;
	/** A `#rrggbb` color, the same for this client on every chart and every range. */
	color: string;
	values: number[];
}

/** One client's bytes at each point of `ChartData.x`. */
export interface ClientBuckets {
	id: string;
	name: string;
	color: string;
	/** Bytes the server received from the client (the API's `receive_bytes`). */
	received: number[];
	/** Bytes the server sent the client (`send_bytes`). */
	sent: number[];
}

/** A traffic history laid out for the charts page: every client on one shared time axis. */
export interface ChartData {
	/** The point times, in seconds: each is where its point starts. */
	x: number[];
	/** The x axis's start and end, in seconds. */
	domain: [number, number];
	/** Seconds each point covers, which is what turns its bytes into a rate. */
	step: number;
	/** The clients that moved any traffic in the range, in the server's order (by name). */
	clients: ClientBuckets[];
}

const palette = [
	'#3b82f6',
	'#f59e0b',
	'#10b981',
	'#ef4444',
	'#8b5cf6',
	'#06b6d4',
	'#ec4899',
	'#84cc16',
	'#f97316',
	'#6366f1'
];

/**
 * The color of the client at `index` in the server's list of all clients, which doesn't change
 * with the range or with which clients are idle: a client keeps its color. Ten distinct hues,
 * then ones spread around the color wheel.
 */
export function clientColor(index: number): string {
	if (index < palette.length) return palette[index];
	const h = (index * 137.508) % 360;
	const f = (n: number) => {
		const k = (n + h / 30) % 12;
		const c = 0.55 - 0.3 * Math.max(-1, Math.min(k - 3, 9 - k, 1));
		return Math.round(c * 255)
			.toString(16)
			.padStart(2, '0');
	};
	return `#${f(0)}${f(8)}${f(4)}`;
}

/** How many points a chart aims to have: enough for a smooth curve, few enough to read. */
const targetPoints = 144;

/** The widths, in seconds, a chart point can be: what a range is averaged into. */
const niceSteps = [5, 10, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400];

/**
 * How many seconds one point of a chart covers. A stored bucket is a minute or an hour, which is
 * too fine for a long range: a day of minutes is 1,440 points in a few hundred pixels, and the
 * lines blur into a solid band. So a range is averaged into about 144 points, in the smallest
 * round width that is a whole number of stored buckets (a day becomes ten minutes, a week two
 * hours, 90 days one day). A short range keeps its own buckets.
 */
export function displayStep(native: number, span: number): number {
	if (!(native > 0)) return span / targetPoints;
	const want = Math.max(native, span / targetPoints);
	for (const s of niceSteps) {
		const k = s / native;
		if (s >= want && k >= 1 && Math.abs(k - Math.round(k)) < 1e-9) return s;
	}
	return Math.ceil(want / native) * native;
}

/** The time axis of a chart: where its points are, and which point a sample belongs to. */
interface Layout {
	/** The point times, in seconds. */
	x: number[];
	domain: [number, number];
	/** Seconds each point covers. */
	step: number;
	pointOf: (t: number) => number | undefined;
}

/**
 * Lays out the axis for a history that ends at `until` and takes `stepSeconds` a sample, whose
 * samples are at `times`.
 *
 * A stored range is averaged into about 144 points (displayStep), counted back from `until` so
 * every point is whole. A 1m range keeps the polls themselves as its points: the server sends a
 * sample at every poll, and the polls aren't on a grid.
 */
function layout(range: TrafficRange, stepSeconds: number, until: string, times: number[]): Layout {
	const end = Date.parse(until) / 1000;
	const span = rangeSeconds[range];
	const start = end - span;
	if (range === '1m') {
		const x = [...new Set(times.filter((t) => t >= start && t <= end))].sort((a, b) => a - b);
		const at = new Map(x.map((t, i) => [t, i]));
		return { x, domain: [start, end], step: stepSeconds, pointOf: (t) => at.get(t) };
	}
	const step = displayStep(stepSeconds, span);
	const n = Math.ceil(span / step - 1e-9);
	const first = end - n * step;
	return {
		x: Array.from({ length: n }, (_, j) => first + j * step),
		domain: [start, end],
		step,
		pointOf: (t) => {
			const j = Math.floor((t - first) / step + 1e-9);
			return j >= 0 && j < n ? j : undefined;
		}
	};
}

const sampleTime = (s: { bucket_start: string }) => Date.parse(s.bucket_start) / 1000;

/**
 * Lays a traffic history out for the charts page: one shared time axis, with each client's bytes
 * in every point (zero where it moved nothing, which also means a quiet stretch reads as a dip to
 * zero and not a line drawn across it). Clients with no traffic in the range are left out, since
 * a line along the floor says nothing.
 */
export function buildChartData(h: TrafficHistory, range: TrafficRange): ChartData {
	const times = h.clients.flatMap((c) => c.samples.map(sampleTime));
	const { x, domain, step, pointOf } = layout(range, h.step_seconds, h.until, times);
	const clients: ClientBuckets[] = [];
	h.clients.forEach((c, i) => {
		const received = new Array<number>(x.length).fill(0);
		const sent = new Array<number>(x.length).fill(0);
		let moved = false;
		for (const s of c.samples) {
			const j = pointOf(sampleTime(s));
			if (j === undefined) continue;
			received[j] += s.receive_bytes;
			sent[j] += s.send_bytes;
			if (s.send_bytes > 0 || s.receive_bytes > 0) moved = true;
		}
		if (moved) clients.push({ id: c.id, name: c.name, color: clientColor(i), received, sent });
	});
	return { x, domain, step, clients };
}

/** One series (a client's, or every client's summed) laid out the same way. */
export interface SeriesData {
	x: number[];
	domain: [number, number];
	step: number;
	/** Bytes the server received in each point (the API's `receive_bytes`). */
	received: number[];
	/** Bytes the server sent in each point (`send_bytes`). */
	sent: number[];
}

/** Lays one series out for a bandwidth chart: the dashboard's total, or a client's own. */
export function buildSeriesData(w: TrafficSeries, range: TrafficRange): SeriesData {
	const { x, domain, step, pointOf } = layout(
		range,
		w.step_seconds,
		w.until,
		w.samples.map(sampleTime)
	);
	const received = new Array<number>(x.length).fill(0);
	const sent = new Array<number>(x.length).fill(0);
	for (const s of w.samples) {
		const j = pointOf(sampleTime(s));
		if (j === undefined) continue;
		received[j] += s.receive_bytes;
		sent[j] += s.send_bytes;
	}
	return { x, domain, step, received, sent };
}

/** Converts bytes per bucket to bits per second. */
export function toRates(bytes: number[], step: number): number[] {
	return bytes.map((b) => (b * 8) / step);
}

/** The running total of bytes per bucket: how much has moved since the start of the range. */
export function toCumulative(bytes: number[]): number[] {
	let sum = 0;
	return bytes.map((b) => (sum += b));
}

/** Which way a chart looks at a client's traffic, from the server's side. */
export type Direction = 'received' | 'sent';

/** The series for a chart of one direction, as a rate or as a running total. */
export function chartSeries(
	data: ChartData,
	direction: Direction,
	kind: 'rate' | 'cumulative'
): ChartSeries[] {
	return data.clients.map((c) => ({
		id: c.id,
		name: c.name,
		color: c.color,
		values: kind === 'rate' ? toRates(c[direction], data.step) : toCumulative(c[direction])
	}));
}

/** Received is green and sent is rose, on every bandwidth chart. */
export const receivedColor = '#10b981';
export const sentColor = '#e11d48';

/**
 * The two lines of a bandwidth chart, as rates or as running totals: what the server received
 * and what it sent. When nothing moved in the range there are none, so the chart says so instead
 * of drawing two lines along the floor.
 */
export function bandwidthSeries(d: SeriesData, kind: 'rate' | 'cumulative'): ChartSeries[] {
	if (!d.received.some((v) => v > 0) && !d.sent.some((v) => v > 0)) return [];
	const values = (bytes: number[]) =>
		kind === 'rate' ? toRates(bytes, d.step) : toCumulative(bytes);
	return [
		{ id: 'received', name: 'Received', color: receivedColor, values: values(d.received) },
		{ id: 'sent', name: 'Sent', color: sentColor, values: values(d.sent) }
	];
}
