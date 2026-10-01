import { describe, expect, it } from 'vitest';
import { parseRange } from './range.svelte';
import { trafficRanges } from './traffic';

describe('parseRange', () => {
	it('keeps a stored range that is still one the charts offer', () => {
		for (const r of trafficRanges) expect(parseRange(r)).toBe(r);
	});

	it('falls back to 24 hours for nothing, or for a range that is gone', () => {
		expect(parseRange(null)).toBe('24h');
		expect(parseRange(undefined)).toBe('24h');
		expect(parseRange('')).toBe('24h');
		expect(parseRange('5y')).toBe('24h');
		expect(parseRange('1w')).toBe('24h');
	});
});
