import { afterEach, describe, expect, it } from 'vitest';
import type { TrafficRange } from './api';
import { formatXTick, thin, xTicks } from './chart';

// Local times, so these read the same in any time zone.
const local = (y: number, mo: number, d: number, h = 0, mi = 0, s = 0) =>
	new Date(y, mo - 1, d, h, mi, s).getTime() / 1000;

/** The labels the axis shows for a window. */
function labels(range: TrafficRange, min: number, max: number): string[] {
	return xTicks(range, min, max).map((t) => formatXTick(range, t));
}

describe('x axis labels', () => {
	it('a minute is labeled every 15 seconds, to the second', () => {
		const min = local(2026, 9, 30, 20, 12, 3);
		expect(labels('1m', min, min + 60)).toEqual(['20:12:15', '20:12:30', '20:12:45', '20:13:00']);
	});

	it('an hour is labeled every 5 minutes, without seconds', () => {
		const min = local(2026, 9, 30, 19, 17);
		expect(labels('1h', min, min + 3600)).toEqual([
			'19:20',
			'19:25',
			'19:30',
			'19:35',
			'19:40',
			'19:45',
			'19:50',
			'19:55',
			'20:00',
			'20:05',
			'20:10',
			'20:15'
		]);
	});

	it('12 hours are labeled every hour on the hour', () => {
		const min = local(2026, 9, 30, 8, 10);
		const got = labels('12h', min, min + 12 * 3600);
		expect(got).toHaveLength(12);
		expect(got[0]).toBe('09:00');
		expect(got[11]).toBe('20:00');
	});

	it('24 hours are labeled every 3 hours, across midnight, with no date', () => {
		const min = local(2026, 9, 29, 12, 10);
		expect(labels('24h', min, min + 86400)).toEqual([
			'15:00',
			'18:00',
			'21:00',
			'00:00',
			'03:00',
			'06:00',
			'09:00',
			'12:00'
		]);
	});

	it('a week is labeled every day, by date and with no year', () => {
		const min = local(2026, 9, 23, 12);
		expect(labels('7d', min, min + 7 * 86400)).toEqual([
			'24 Sep',
			'25 Sep',
			'26 Sep',
			'27 Sep',
			'28 Sep',
			'29 Sep',
			'30 Sep'
		]);
	});

	it('30 days are labeled every 3 days, counted back from the newest', () => {
		const max = local(2026, 9, 30, 12);
		const got = labels('30d', local(2026, 8, 31, 12), max);
		expect(got).toEqual([
			'3 Sep',
			'6 Sep',
			'9 Sep',
			'12 Sep',
			'15 Sep',
			'18 Sep',
			'21 Sep',
			'24 Sep',
			'27 Sep',
			'30 Sep'
		]);
	});

	it('90 days are labeled every week, on the newest day of the week', () => {
		const got = labels('90d', local(2026, 7, 2, 12), local(2026, 9, 30, 12));
		expect(got).toHaveLength(13);
		expect(got[0]).toBe('8 Jul');
		expect(got.at(-1)).toBe('30 Sep');
		expect(got.slice(-4)).toEqual(['9 Sep', '16 Sep', '23 Sep', '30 Sep']);
	});

	it('never uses am or pm', () => {
		const min = local(2026, 9, 30, 0, 5);
		for (const r of ['1m', '1h', '12h', '24h'] as const) {
			for (const l of labels(
				r,
				min,
				min + { '1m': 60, '1h': 3600, '12h': 43200, '24h': 86400 }[r]
			)) {
				expect(l).not.toMatch(/[ap]m/i);
			}
		}
	});

	it('has nothing to label in an empty or backwards window', () => {
		expect(xTicks('24h', 100, 100)).toEqual([]);
		expect(xTicks('24h', 200, 100)).toEqual([]);
		expect(xTicks('24h', NaN, NaN)).toEqual([]);
	});
});

describe('x axis labels across a clock change', () => {
	const tz = process.env.TZ;
	afterEach(() => {
		if (tz === undefined) delete process.env.TZ;
		else process.env.TZ = tz;
	});

	it('stay on the local clock on a day that is 25 hours long', () => {
		// Clocks go back an hour at 02:00 on 1 November 2026 in New York.
		process.env.TZ = 'America/New_York';
		const min = local(2026, 10, 31, 12, 10);
		const max = local(2026, 11, 1, 12, 10);
		expect(max - min).toBe(25 * 3600);
		expect(labels('24h', min, max)).toEqual([
			'15:00',
			'18:00',
			'21:00',
			'00:00',
			'03:00',
			'06:00',
			'09:00',
			'12:00'
		]);
		// And the days stay on midnight, though one of them is an hour longer.
		expect(labels('7d', local(2026, 10, 27, 12), max)).toEqual([
			'28 Oct',
			'29 Oct',
			'30 Oct',
			'31 Oct',
			'1 Nov'
		]);
	});
});

describe('thin', () => {
	it('leaves labels alone when they have room', () => {
		expect(thin([0, 15, 30, 45, 60], 0, 60, 600, 70)).toEqual([0, 15, 30, 45, 60]);
	});

	it('drops labels that would run into each other, and keeps the newest', () => {
		// Five labels in 100 px is 25 px apart, and each needs 70.
		const got = thin([0, 15, 30, 45, 60], 0, 60, 100, 70);
		expect(got).toEqual([15, 60]);
		expect(got.at(-1)).toBe(60);
	});

	it('copes with too few labels, and a plot with no width yet', () => {
		expect(thin([], 0, 60, 100, 70)).toEqual([]);
		expect(thin([30], 0, 60, 100, 70)).toEqual([30]);
		expect(thin([0, 15, 30], 0, 60, 0, 70)).toEqual([0, 15, 30]);
	});
});
