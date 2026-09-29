import { afterEach, describe, expect, it, vi } from 'vitest';
import { poll } from './poll';

afterEach(() => {
	vi.useRealTimers();
});

describe('poll', () => {
	it('runs at once, repeats, keeps going after errors, and stops', async () => {
		vi.useFakeTimers();
		let calls = 0;
		const stop = poll(async () => {
			calls++;
			if (calls === 2) throw new Error('offline');
		}, 1000);
		await vi.advanceTimersByTimeAsync(0);
		expect(calls).toBe(1);
		await vi.advanceTimersByTimeAsync(2000);
		expect(calls).toBe(3);
		stop();
		await vi.advanceTimersByTimeAsync(5000);
		expect(calls).toBe(3);
	});
});
