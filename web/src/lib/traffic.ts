import type { TrafficRange, TrafficSample } from './api';

export const rangeLabels: Record<TrafficRange, string> = {
	'24h': '24 Hours',
	'7d': '7 Days',
	'90d': '90 Days'
};

export const trafficRanges: TrafficRange[] = ['24h', '7d', '90d'];

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
