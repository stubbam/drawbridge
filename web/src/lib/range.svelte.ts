// The chart range the admin picked: one choice for every chart in the app, on the dashboard, a
// client's page, and the Charts page, remembered in this browser. Picking 1 Week on one page means
// the next chart, on this page or another, opens on 1 Week.
import type { TrafficRange } from './api';
import { trafficRanges } from './traffic';

const storageKey = 'drawbridge-chart-range';

/** The range a stored string names, or 24 hours for anything else (a first visit, or an old value). */
export function parseRange(v: string | null | undefined): TrafficRange {
	return trafficRanges.find((r) => r === v) ?? '24h';
}

function readStored(): TrafficRange {
	try {
		return parseRange(localStorage.getItem(storageKey));
	} catch {
		return '24h';
	}
}

// A reassigned $state export must live on an object's property, not the binding itself.
export const chartRange = $state<{ value: TrafficRange }>({ value: readStored() });

/** Switches every chart to a range, and remembers it for next time. */
export function setChartRange(range: TrafficRange) {
	chartRange.value = range;
	try {
		localStorage.setItem(storageKey, range);
	} catch {
		// Private browsing or blocked storage: the choice still applies until the page closes.
	}
}
