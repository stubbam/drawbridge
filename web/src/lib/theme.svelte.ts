// The color mode: light, dark, or auto, which follows the browser's preference
// (prefers-color-scheme), including when it changes while the page is open. The toggle in
// the nav cycles through them, and the choice is remembered. The inline script in
// web/src/app.html applies the stored mode before first paint; this module keeps it applied
// afterward. Both set the class that web/src/routes/layout.css's @custom-variant keys off.
const storageKey = 'drawbridge-theme';

export type Mode = 'light' | 'dark' | 'auto';

const order: Mode[] = ['light', 'dark', 'auto'];

export const modeLabels: Record<Mode, string> = { light: 'Light', dark: 'Dark', auto: 'Auto' };

function isMode(v: unknown): v is Mode {
	return v === 'light' || v === 'dark' || v === 'auto';
}

/** The mode after m: light, then dark, then auto, then light again. */
function nextMode(m: Mode): Mode {
	return order[(order.indexOf(m) + 1) % order.length];
}

/** Whether the page is dark in mode m, when the browser does or doesn't prefer dark. */
function isDark(m: Mode, browserPrefersDark: boolean): boolean {
	return m === 'dark' || (m === 'auto' && browserPrefersDark);
}

function readStored(): Mode {
	try {
		const v = localStorage.getItem(storageKey);
		// Nothing stored yet (a first visit) means auto.
		return isMode(v) ? v : 'auto';
	} catch {
		return 'auto';
	}
}

function darkQuery(): MediaQueryList | undefined {
	try {
		return matchMedia('(prefers-color-scheme: dark)');
	} catch {
		return undefined;
	}
}

const query = darkQuery();

// A reassigned $state export must live on an object's property, not the binding itself.
export const theme = $state<{ mode: Mode }>({ mode: readStored() });

function apply() {
	document.documentElement.classList.toggle('dark', isDark(theme.mode, query?.matches ?? false));
}

// In auto, follow the browser when its preference changes, such as a system that switches
// to dark at sunset.
query?.addEventListener('change', () => {
	if (theme.mode === 'auto') apply();
});

/** Moves to the next mode, applies it, and remembers it for next time. */
export function cycleMode() {
	theme.mode = nextMode(theme.mode);
	apply();
	try {
		localStorage.setItem(storageKey, theme.mode);
	} catch {
		// Private browsing or blocked storage: the toggle still works for this page load.
	}
}
