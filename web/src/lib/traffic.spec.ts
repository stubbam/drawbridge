import { describe, expect, it } from 'vitest';
import { rangeLabels, refreshMs, toUplotData, trafficRanges, xRange } from './traffic';

describe('toUplotData', () => {
	it('converts samples into aligned timestamp/rx/tx arrays', () => {
		const [x, rx, tx] = toUplotData([
			{ bucket_start: '2026-09-28T10:00:00Z', receive_bytes: 100, send_bytes: 50 },
			{ bucket_start: '2026-09-28T10:01:00Z', receive_bytes: 200, send_bytes: 75 }
		]);
		expect(x).toEqual([
			Date.parse('2026-09-28T10:00:00Z') / 1000,
			Date.parse('2026-09-28T10:01:00Z') / 1000
		]);
		expect(rx).toEqual([100, 200]);
		expect(tx).toEqual([50, 75]);
	});

	it('returns empty arrays for no data', () => {
		expect(toUplotData([])).toEqual([[], [], []]);
	});
});

describe('xRange', () => {
	const now = Date.parse('2026-09-29T12:00:00.500Z');
	const end = Date.parse('2026-09-29T12:00:00Z') / 1000;

	it('spans the whole window ending now, whatever the data covers', () => {
		expect(xRange('1m', now)).toEqual([end - 60, end]);
		expect(xRange('1h', now)).toEqual([end - 3600, end]);
		expect(xRange('12h', now)).toEqual([end - 12 * 3600, end]);
		expect(xRange('24h', now)).toEqual([end - 86400, end]);
		expect(xRange('7d', now)).toEqual([end - 7 * 86400, end]);
		expect(xRange('30d', now)).toEqual([end - 30 * 86400, end]);
		expect(xRange('90d', now)).toEqual([end - 90 * 86400, end]);
	});
});

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
		const spans = trafficRanges.map((r) => {
			const [start, end] = xRange(r, 0);
			return end - start;
		});
		expect(spans).toEqual([...spans].sort((a, b) => a - b));
	});
});

describe('refreshMs', () => {
	it('refetches only the 1 minute range faster than the stored buckets change', () => {
		expect(refreshMs('1m')).toBe(5000);
		for (const r of trafficRanges.filter((r) => r !== '1m')) expect(refreshMs(r)).toBe(60000);
	});
});
