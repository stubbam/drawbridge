import type { TrafficRange, TrafficSample } from './api';

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

const rangeSeconds: Record<TrafficRange, number> = {
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

/**
 * The x axis for a range: the whole window ending now, in seconds. Without it the axis
 * shrinks to wherever the data happens to start, so a short history fills the plot and every
 * range looks like a different scale.
 */
export function xRange(range: TrafficRange, nowMs: number): [number, number] {
	const end = Math.floor(nowMs / 1000);
	return [end - rangeSeconds[range], end];
}

/**
 * Converts traffic samples into uPlot's aligned-data format: a timestamps series (in
 * seconds, uPlot's default) plus one series each for received and sent bytes.
 */
export function toUplotData(samples: TrafficSample[]): [number[], number[], number[]] {
	const x: number[] = [];
	const rx: number[] = [];
	const tx: number[] = [];
	for (const s of samples) {
		x.push(Date.parse(s.bucket_start) / 1000);
		rx.push(s.receive_bytes);
		tx.push(s.send_bytes);
	}
	return [x, rx, tx];
}
