import { api, type DrawbridgeEvent, type StreamStatus } from './api';

/**
 * The live feed (`GET /api/stream`, Server-Sent Events): the server's status and every client's,
 * pushed as the daemon polls the peers, and each event as it's recorded. A page that shows
 * either asks for the feed with `$effect(() => useLive())`, and reads `live`.
 *
 * While the feed is open, a page takes its status from `live.status` and doesn't poll. While
 * it isn't (it's connecting, or the browser or the network won't carry it), `live.open` is
 * false and the page polls as it always did, so nothing depends on the stream working.
 */
export const live = $state({
	/** Whether the stream is connected, and so `status` is current. */
	open: false,
	/** The latest `status` message. It's stale when `open` is false. */
	status: undefined as StreamStatus | undefined,
	/**
	 * How many times the stream has reconnected. Events that came while it was away are
	 * missed, so a page that shows events loads them again when this changes.
	 */
	reconnects: 0
});

/** The part of EventSource that the feed uses, so a test can stand in for it. */
export interface Source {
	readyState: number;
	onopen: (() => void) | null;
	onerror: (() => void) | null;
	addEventListener(type: string, listener: (e: MessageEvent<string>) => void): void;
	close(): void;
}

let newSource = (url: string): Source => {
	const es = new EventSource(url);
	const s: Source = {
		get readyState() {
			return es.readyState;
		},
		onopen: null,
		onerror: null,
		addEventListener: (type, listener) =>
			es.addEventListener(type, (e) => listener(e as MessageEvent<string>)),
		close: () => es.close()
	};
	es.onopen = () => s.onopen?.();
	es.onerror = () => s.onerror?.();
	return s;
};

/** Replaces EventSource, for tests. */
export function setEventSource(make: (url: string) => Source) {
	newSource = make;
}

/** EventSource.CLOSED: the browser has given up on the connection, and won't retry it. */
const CLOSED = 2;
/** How long to wait to try again after the browser gives up. */
const retryMs = 10_000;
/** How long the feed stays open with no page using it, so that moving between pages doesn't reconnect. */
const graceMs = 1000;

let listeners: ((e: DrawbridgeEvent) => void)[] = [];

/** Calls fn with each event as it's recorded, until the returned function is called. */
export function onLiveEvent(fn: (e: DrawbridgeEvent) => void): () => void {
	listeners.push(fn);
	return () => {
		listeners = listeners.filter((l) => l !== fn);
	};
}

let users = 0;
let source: Source | undefined;
let retryTimer: ReturnType<typeof setTimeout> | undefined;
let closeTimer: ReturnType<typeof setTimeout> | undefined;
let watching = false;
let everOpened = false;

function connect() {
	clearTimeout(retryTimer);
	const s = newSource('/api/stream');
	source = s;
	s.onopen = () => {
		if (source !== s) return;
		live.open = true;
		if (everOpened) live.reconnects++;
		everOpened = true;
	};
	s.addEventListener('status', (e) => {
		if (source !== s) return;
		try {
			live.status = JSON.parse(e.data) as StreamStatus;
		} catch {
			// A message that isn't JSON isn't one of ours; the next status replaces it anyway.
		}
	});
	s.addEventListener('event', (e) => {
		if (source !== s) return;
		let event: DrawbridgeEvent;
		try {
			event = JSON.parse(e.data) as DrawbridgeEvent;
		} catch {
			return;
		}
		for (const fn of listeners) fn(event);
	});
	s.onerror = () => {
		if (source !== s) return;
		live.open = false;
		if (s.readyState !== CLOSED) return; // the browser is reconnecting by itself
		// The browser gave up: the session has ended, or the server refused. Asking for the
		// account says which, and a 401 takes the app to the login.
		source = undefined;
		void api.me().catch(() => {});
		retryTimer = setTimeout(ensure, retryMs);
	};
}

function disconnect() {
	clearTimeout(retryTimer);
	source?.close();
	source = undefined;
	live.open = false;
}

/** Opens the feed if a page wants it and the tab is showing. */
function ensure() {
	if (users > 0 && !source && !document.hidden) connect();
}

function onVisibility() {
	if (document.hidden) disconnect();
	else ensure();
}

/**
 * Asks for the feed, which stays open while any page that asked is mounted and the tab is
 * showing. A hidden tab closes it, so it stops keeping its session alive, as polling stopped
 * before. Returns the function that gives it back; an effect calls that on cleanup.
 */
export function useLive(): () => void {
	users++;
	clearTimeout(closeTimer);
	if (!watching) {
		document.addEventListener('visibilitychange', onVisibility);
		watching = true;
	}
	ensure();
	let released = false;
	return () => {
		if (released) return;
		released = true;
		users--;
		if (users > 0) return;
		// The next page may be about to ask, so the feed isn't closed at once.
		closeTimer = setTimeout(() => {
			if (users > 0) return;
			disconnect();
			document.removeEventListener('visibilitychange', onVisibility);
			watching = false;
		}, graceMs);
	};
}
