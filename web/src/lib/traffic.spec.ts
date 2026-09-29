import { describe, expect, it } from 'vitest';
import { toUplotData } from './traffic';

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
