import { describe, expect, it } from 'vitest';
import type { TrafficHistory, TrafficSeries } from './api';
import {
	buildChartData,
	buildSeriesData,
	chartSeries,
	clientColor,
	displayStep,
	rangeLabels,
	rangeSeconds,
	receivedColor,
	refreshMs,
	sentColor,
	throughputSeries,
	toCumulative,
	toRates,
	trafficRanges
} from './traffic';

describe('trafficRanges', () => {
	it('lists every range shortest first, each with a label', () => {
		expect(trafficRanges.map((r) => rangeLabels[r])).toEqual([
			'1 Minute',
			'1 Hour',
			'12 Hours',
			'24 Hours',
			'1 Week',
			'30 Days',
			'90 Days'
		]);
		const spans = trafficRanges.map((r) => rangeSeconds[r]);
		expect(spans).toEqual([60, 3600, 12 * 3600, 86400, 7 * 86400, 30 * 86400, 90 * 86400]);
	});
});

describe('refreshMs', () => {
	it('refetches only the 1 minute range faster than the stored buckets change', () => {
		expect(refreshMs('1m')).toBe(5000);
		for (const r of trafficRanges.filter((r) => r !== '1m')) expect(refreshMs(r)).toBe(60000);
	});
});

describe('buildChartData', () => {
	const until = '2026-09-29T12:00:00Z';
	const at = (iso: string) => Date.parse(iso) / 1000;
	const sample = (bucket_start: string, receive_bytes: number, send_bytes: number) => ({
		bucket_start,
		receive_bytes,
		send_bytes
	});

	it('puts every client on one grid and reads a bucket with no traffic as zero', () => {
		const h: TrafficHistory = {
			step_seconds: 60,
			until,
			clients: [
				{
					id: 'a',
					name: 'alpha',
					samples: [sample('2026-09-29T11:57:00Z', 100, 400), sample('2026-09-29T11:59:00Z', 50, 0)]
				},
				{ id: 'b', name: 'beta', samples: [sample('2026-09-29T11:58:00Z', 0, 7)] }
			]
		};
		const d = buildChartData(h, '1h');
		expect(d.domain).toEqual([at(until) - 3600, at(until)]);
		expect(d.step).toBe(60);
		// A bucket every minute across the hour, ending at the last whole one before `until`.
		expect(d.x.length).toBe(60);
		expect(d.x[0]).toBe(at(until) - 3600);
		expect(d.x.at(-1)).toBe(at(until) - 60);
		const i = (iso: string) => d.x.indexOf(at(iso));
		const [a, b] = d.clients;
		// The idle minutes between the samples are zero, not gaps the line would cross.
		expect(a.received[i('2026-09-29T11:57:00Z')]).toBe(100);
		expect(a.sent[i('2026-09-29T11:57:00Z')]).toBe(400);
		expect(a.received[i('2026-09-29T11:58:00Z')]).toBe(0);
		expect(a.received[i('2026-09-29T11:59:00Z')]).toBe(50);
		expect(b.sent[i('2026-09-29T11:58:00Z')]).toBe(7);
		expect(a.received.reduce((x, y) => x + y)).toBe(150);
	});

	it('keeps what the server received from a client apart from what it sent it', () => {
		const h: TrafficHistory = {
			step_seconds: 60,
			until,
			clients: [{ id: 'a', name: 'alpha', samples: [sample('2026-09-29T11:59:00Z', 11, 22)] }]
		};
		const [c] = buildChartData(h, '1h').clients;
		expect(c.sent.at(-1)).toBe(22); // send_bytes
		expect(c.received.at(-1)).toBe(11); // receive_bytes
	});

	it('leaves out a client with no traffic in the range, and keeps every client its color', () => {
		const h: TrafficHistory = {
			step_seconds: 3600,
			until,
			clients: [
				{ id: 'a', name: 'alpha', samples: [] },
				{ id: 'b', name: 'beta', samples: [sample('2026-09-29T10:00:00Z', 5, 5)] },
				// Only traffic from before the range: nothing to draw.
				{ id: 'c', name: 'gamma', samples: [sample('2026-06-01T10:00:00Z', 5, 5)] }
			]
		};
		const d = buildChartData(h, '7d');
		expect(d.clients.map((c) => c.name)).toEqual(['beta']);
		// beta is second in the server's list, so it has the second color, idle clients or not.
		expect(d.clients[0].color).toBe(clientColor(1));
		// A week of hours is averaged into two-hour points.
		expect(d.step).toBe(7200);
		expect(d.x.length).toBe(84);
	});

	it('uses the polls themselves as the axis for 1m, and keeps the window to the minute', () => {
		const h: TrafficHistory = {
			step_seconds: 5,
			until: '2026-09-29T12:00:10Z',
			clients: [
				{
					id: 'a',
					name: 'alpha',
					samples: [
						sample('2026-09-29T11:58:00Z', 9, 9), // before the minute
						sample('2026-09-29T12:00:00Z', 0, 0),
						sample('2026-09-29T12:00:04Z', 10, 0), // polls aren't on a 5 s grid
						sample('2026-09-29T12:00:09Z', 0, 20)
					]
				},
				{
					id: 'b',
					name: 'beta',
					samples: [
						sample('2026-09-29T12:00:00Z', 0, 0),
						sample('2026-09-29T12:00:04Z', 0, 0),
						sample('2026-09-29T12:00:09Z', 0, 0)
					]
				}
			]
		};
		const d = buildChartData(h, '1m');
		expect(d.x).toEqual([
			at('2026-09-29T12:00:00Z'),
			at('2026-09-29T12:00:04Z'),
			at('2026-09-29T12:00:09Z')
		]);
		expect(d.clients.map((c) => c.name)).toEqual(['alpha']);
		expect(d.clients[0].received).toEqual([0, 10, 0]);
		expect(d.clients[0].sent).toEqual([0, 0, 20]);
	});

	it('averages a day into ten-minute points: the bytes add up, and the rate is over ten minutes', () => {
		const h: TrafficHistory = {
			step_seconds: 60,
			until,
			clients: [
				{
					id: 'a',
					name: 'alpha',
					samples: [
						// Two minutes of the same ten-minute point, and one in the next.
						sample('2026-09-29T11:41:00Z', 0, 600_000),
						sample('2026-09-29T11:49:00Z', 0, 600_000),
						sample('2026-09-29T11:50:00Z', 0, 300_000)
					]
				}
			]
		};
		const d = buildChartData(h, '24h');
		expect(d.step).toBe(600);
		expect(d.x.length).toBe(144);
		// Counted back from `until`, so the last point is the last ten minutes, whole.
		expect(d.x[0]).toBe(at(until) - 86400);
		expect(d.x.at(-1)).toBe(at(until) - 600);
		const a = d.clients[0];
		const first = d.x.indexOf(at('2026-09-29T11:40:00Z'));
		expect(a.sent[first]).toBe(1_200_000);
		expect(a.sent[first + 1]).toBe(300_000);
		expect(a.sent.reduce((x, y) => x + y)).toBe(1_500_000);
		// 1.2 MB over ten minutes is 16 Kbps, not the 160 Kbps of the minute it would have been.
		expect(chartSeries(d, 'sent', 'rate')[0].values[first]).toBe(16000);
	});

	it('gives every range about 144 points or fewer', () => {
		const point = (range: (typeof trafficRanges)[number], step: number) =>
			buildChartData({ step_seconds: step, until, clients: [] }, range).x.length;
		expect(point('1h', 60)).toBe(60);
		expect(point('12h', 60)).toBe(144);
		expect(point('24h', 60)).toBe(144);
		expect(point('7d', 3600)).toBe(84);
		expect(point('30d', 3600)).toBe(120);
		expect(point('90d', 3600)).toBe(90);
	});

	it('copes with no clients and with no data', () => {
		const d = buildChartData({ step_seconds: 60, until, clients: [] }, '24h');
		expect(d.clients).toEqual([]);
		expect(d.x.length).toBe(144);
		expect(chartSeries(d, 'sent', 'rate')).toEqual([]);
	});
});

describe('chart series', () => {
	it('turns bytes per bucket into bits per second', () => {
		expect(toRates([0, 750, 1500], 60)).toEqual([0, 100, 200]);
		expect(toRates([5], 5)).toEqual([8]);
	});

	it('turns bytes per bucket into a running total from the start of the range', () => {
		expect(toCumulative([5, 0, 7, 3])).toEqual([5, 5, 12, 15]);
		expect(toCumulative([])).toEqual([]);
	});

	it('picks a direction and a kind', () => {
		const data = {
			x: [0, 60, 120],
			domain: [0, 180] as [number, number],
			step: 60,
			clients: [
				{ id: 'a', name: 'alpha', color: '#111111', received: [0, 30, 0], sent: [60, 0, 120] }
			]
		};
		expect(chartSeries(data, 'sent', 'rate')[0].values).toEqual([8, 0, 16]);
		expect(chartSeries(data, 'sent', 'cumulative')[0].values).toEqual([60, 60, 180]);
		expect(chartSeries(data, 'received', 'cumulative')[0].values).toEqual([0, 30, 30]);
		expect(chartSeries(data, 'received', 'rate')[0]).toMatchObject({
			id: 'a',
			name: 'alpha',
			color: '#111111'
		});
	});
});

describe('clientColor', () => {
	it('gives a stable, distinct #rrggbb color to many clients', () => {
		const colors = Array.from({ length: 40 }, (_, i) => clientColor(i));
		for (const c of colors) expect(c).toMatch(/^#[0-9a-f]{6}$/);
		expect(new Set(colors).size).toBe(40);
		expect(clientColor(3)).toBe(clientColor(3));
	});
});

describe('displayStep', () => {
	it('keeps a short range at its own bucket and widens a long one to about 144 points', () => {
		const hour = 3600;
		expect(displayStep(5, 60)).toBe(5);
		expect(displayStep(60, hour)).toBe(60);
		expect(displayStep(60, 12 * hour)).toBe(300);
		expect(displayStep(60, 24 * hour)).toBe(600);
		expect(displayStep(hour, 7 * 24 * hour)).toBe(2 * hour);
		expect(displayStep(hour, 30 * 24 * hour)).toBe(6 * hour);
		expect(displayStep(hour, 90 * 24 * hour)).toBe(24 * hour);
	});

	it('is always a whole number of stored buckets', () => {
		// A raw interval that no round width divides still gets a whole multiple of itself.
		expect(displayStep(7, 86400)).toBe(602);
		expect(displayStep(0.5, 86400)).toBe(600);
		// A raw interval wider than a point: the bucket itself.
		expect(displayStep(900, 3600)).toBe(900);
		// Nonsense from the server doesn't divide by zero.
		expect(displayStep(0, 86400)).toBe(600);
	});
});

describe('buildSeriesData', () => {
	const until = '2026-09-29T12:00:00Z';
	const at = (iso: string) => Date.parse(iso) / 1000;
	const sample = (bucket_start: string, receive_bytes: number, send_bytes: number) => ({
		bucket_start,
		receive_bytes,
		send_bytes
	});

	it('averages a series onto the same points as the per-client charts', () => {
		const w: TrafficSeries = {
			step_seconds: 60,
			until,
			samples: [
				sample('2026-09-29T11:41:00Z', 300_000, 600_000),
				sample('2026-09-29T11:49:00Z', 300_000, 600_000),
				sample('2026-09-29T11:50:00Z', 0, 300_000)
			]
		};
		const d = buildSeriesData(w, '24h');
		expect(d.step).toBe(600);
		expect(d.x.length).toBe(144);
		expect(d.domain).toEqual([at(until) - 86400, at(until)]);
		const first = d.x.indexOf(at('2026-09-29T11:40:00Z'));
		// The server received 600 KB and sent 1.2 MB in that ten minutes; it's all the series has.
		expect(d.received[first]).toBe(600_000);
		expect(d.sent[first]).toBe(1_200_000);
		expect(d.sent[first + 1]).toBe(300_000);
		expect(d.received.reduce((a, b) => a + b)).toBe(600_000);
		expect(d.sent.reduce((a, b) => a + b)).toBe(1_500_000);
	});

	it('keeps the polls themselves as the points of a 1m series', () => {
		const w: TrafficSeries = {
			step_seconds: 5,
			until: '2026-09-29T12:00:10Z',
			samples: [
				sample('2026-09-29T11:58:00Z', 1, 1), // before the minute
				sample('2026-09-29T12:00:00Z', 0, 0),
				sample('2026-09-29T12:00:04Z', 10, 0),
				sample('2026-09-29T12:00:09Z', 0, 20)
			]
		};
		const d = buildSeriesData(w, '1m');
		expect(d.x).toEqual([
			at('2026-09-29T12:00:00Z'),
			at('2026-09-29T12:00:04Z'),
			at('2026-09-29T12:00:09Z')
		]);
		expect(d.step).toBe(5);
		expect(d.received).toEqual([0, 10, 0]);
		expect(d.sent).toEqual([0, 0, 20]);
	});

	it('is empty-but-laid-out with no samples', () => {
		const d = buildSeriesData({ step_seconds: 3600, until, samples: [] }, '7d');
		expect(d.x.length).toBe(84);
		expect(d.received.every((v) => v === 0)).toBe(true);
		expect(throughputSeries(d, 'rate')).toEqual([]);
	});
});

describe('throughputSeries', () => {
	const data = {
		x: [0, 600, 1200],
		domain: [0, 1800] as [number, number],
		step: 600,
		received: [0, 75_000, 0],
		sent: [150_000, 0, 75_000]
	};

	it('has a received line and a sent line, as rates', () => {
		const [received, sent] = throughputSeries(data, 'rate');
		expect(received).toMatchObject({ id: 'received', name: 'Received', color: receivedColor });
		expect(sent).toMatchObject({ id: 'sent', name: 'Sent', color: sentColor });
		// 75 KB over ten minutes is 1 Kbps, and 150 KB is 2 Kbps.
		expect(received.values).toEqual([0, 1000, 0]);
		expect(sent.values).toEqual([2000, 0, 1000]);
	});

	it('has them as running totals too', () => {
		const [received, sent] = throughputSeries(data, 'cumulative');
		expect(received.values).toEqual([0, 75_000, 75_000]);
		expect(sent.values).toEqual([150_000, 150_000, 225_000]);
	});

	it('draws nothing when nothing moved', () => {
		expect(throughputSeries({ ...data, received: [0, 0, 0], sent: [0, 0, 0] }, 'rate')).toEqual([]);
	});
});
