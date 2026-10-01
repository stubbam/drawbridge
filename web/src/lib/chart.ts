import type uPlot from 'uplot';
import type { TrafficRange } from './api';
import { formatClock, formatDay } from './format';

/**
 * uPlot's default y-axis width is a fixed 50px, too narrow for a formatted byte string like
 * "150 MB" or "1.5 GB": it doesn't wrap or shrink the text, so it just clips. This is uPlot's own
 * documented recipe for sizing an axis to its labels instead: measure them with the axis's own
 * font, so they always fit.
 *
 * It measures every label and takes the widest, not the longest string: digits aren't all the
 * same width in a proportional font ("1.0 GB" is as long as "4.0 GB" but narrower), and sizing to
 * the first of the longest strings clipped the left edge of the wider ones.
 */
export function axisSize(self: uPlot, values: string[] | null, axisIdx: number): number {
	const axis = self.axes[axisIdx];
	let size = (axis.ticks?.size ?? 0) + (axis.gap ?? 0);
	if (values?.length && axis.font) {
		self.ctx.save();
		self.ctx.font = axis.font[0];
		const widest = Math.max(...values.map((v) => self.ctx.measureText(String(v ?? '')).width));
		self.ctx.restore();
		size += widest / (globalThis.devicePixelRatio || 1);
	}
	return Math.ceil(size);
}

// What a label on the x axis says, which depends on how long the range is: a minute is read to
// the second, up to a day to the minute, and a week or more to the day. No date on the short
// ranges and no year on the long ones.
type TickKind = 'seconds' | 'time' | 'day';

const tickKinds: Record<TrafficRange, TickKind> = {
	'1m': 'seconds',
	'1h': 'time',
	'12h': 'time',
	'24h': 'time',
	'7d': 'day',
	'30d': 'day',
	'90d': 'day'
};

// The least room, in pixels, a label of each kind needs before the next one starts.
const labelRoom: Record<TickKind, number> = { seconds: 70, time: 48, day: 58 };

/** An x-axis label: "20:12:15", "20:15", or "9 Sep", by range. */
export function formatXTick(range: TrafficRange, sec: number): string {
	const d = new Date(sec * 1000);
	switch (tickKinds[range]) {
		case 'seconds':
			return formatClock(d, true);
		case 'time':
			return formatClock(d);
		default:
			return formatDay(d);
	}
}

/** Every multiple of `step` seconds from min to max. */
function multiples(min: number, max: number, step: number): number[] {
	const out: number[] = [];
	for (let t = Math.ceil(min / step) * step; t <= max; t += step) out.push(t);
	return out;
}

/** Every `every` hours on the local clock (0:00, 3:00, 6:00…) from min to max. */
function localHours(min: number, max: number, every: number): number[] {
	const out: number[] = [];
	const first = new Date(min * 1000);
	for (let day = 0; ; day++) {
		for (let h = 0; h < 24; h += every) {
			// The Date constructor, not arithmetic: a local day isn't always 24 hours long.
			const t =
				new Date(first.getFullYear(), first.getMonth(), first.getDate() + day, h).getTime() / 1000;
			if (t > max) return out;
			if (t >= min) out.push(t);
		}
	}
}

/** Local midnights from min to max, every `every` days counted back from the last one. */
function localDays(min: number, max: number, every: number): number[] {
	const out: number[] = [];
	const last = new Date(max * 1000);
	for (let k = 0; ; k++) {
		const t =
			new Date(last.getFullYear(), last.getMonth(), last.getDate() - k * every).getTime() / 1000;
		if (t < min) return out.reverse();
		out.push(t);
	}
}

/**
 * Where the x axis puts a label, in seconds. A minute: every 15 seconds. An hour: every 5 minutes.
 * 12 hours: every hour. 24 hours: every 3 hours. A week: every day, at midnight. 30 days: every 3
 * days. 90 days: every week. The hours and days fall on the local clock and calendar.
 */
export function xTicks(range: TrafficRange, min: number, max: number): number[] {
	if (!(max > min)) return [];
	switch (range) {
		case '1m':
			return multiples(min, max, 15);
		case '1h':
			return multiples(min, max, 300);
		case '12h':
			return localHours(min, max, 1);
		case '24h':
			return localHours(min, max, 3);
		case '7d':
			return localDays(min, max, 1);
		case '30d':
			return localDays(min, max, 3);
		case '90d':
			return localDays(min, max, 7);
	}
}

/**
 * Drops every other label (or two of every three) when they'd run into each other, as on a phone:
 * the plot is `widthPx` wide for min to max, and a label needs `gapPx`. The newest stays.
 */
export function thin(
	ticks: number[],
	min: number,
	max: number,
	widthPx: number,
	gapPx: number
): number[] {
	if (ticks.length < 2 || !(widthPx > 0)) return ticks;
	const room = ((ticks[1] - ticks[0]) / (max - min)) * widthPx;
	const stride = room >= gapPx ? 1 : Math.ceil(gapPx / room);
	return stride === 1 ? ticks : ticks.filter((_, i) => (ticks.length - 1 - i) % stride === 0);
}

/** The x axis of a chart over `range`: labels by the rules above, and no vertical gridlines. */
export function xAxis(range: TrafficRange, stroke: string): uPlot.Axis {
	return {
		stroke,
		// Horizontal gridlines only: vertical ones are noise on a time axis.
		grid: { show: false },
		splits: (u, _axis, min, max) =>
			thin(
				xTicks(range, min, max),
				min,
				max,
				u.bbox.width / devicePixelRatio,
				labelRoom[tickKinds[range]]
			),
		values: (_u, splits) => splits.map((t) => formatXTick(range, t))
	};
}
