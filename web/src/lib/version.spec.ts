import { describe, expect, it } from 'vitest';
import { fetchVersion, formatVersion } from './version';

function fakeFetch(status: number, body: unknown): typeof fetch {
	return async () => new Response(JSON.stringify(body), { status });
}

describe('formatVersion', () => {
	it('adds the lowercase v prefix and the commit', () => {
		expect(formatVersion({ version: '1.2.3', commit: 'abc1234' })).toBe('v1.2.3 (commit abc1234)');
	});
});

describe('fetchVersion', () => {
	it('returns the version and commit', async () => {
		const info = await fetchVersion(fakeFetch(200, { version: '0.0.0-dev', commit: 'unknown' }));
		expect(info).toEqual({ version: '0.0.0-dev', commit: 'unknown' });
	});

	it('rejects an error status', async () => {
		await expect(fetchVersion(fakeFetch(503, {}))).rejects.toThrow('status 503');
	});

	it('rejects a body without the expected fields', async () => {
		await expect(fetchVersion(fakeFetch(200, { version: 1 }))).rejects.toThrow('unexpected body');
	});
});
