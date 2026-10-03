import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { DrawbridgeEvent, StreamStatus } from './api';
import type { Source } from './live.svelte';

/** A stand-in for EventSource, which the test drives by hand. */
class FakeSource implements Source {
	static all: FakeSource[] = [];
	readyState = 0;
	onopen: (() => void) | null = null;
	onerror: (() => void) | null = null;
	closed = false;
	private listeners = new Map<string, (e: MessageEvent<string>) => void>();
	constructor(readonly url: string) {
		FakeSource.all.push(this);
	}
	addEventListener(type: string, listener: (e: MessageEvent<string>) => void) {
		this.listeners.set(type, listener);
	}
	close() {
		this.closed = true;
		this.readyState = 2;
	}
	open() {
		this.readyState = 1;
		this.onopen?.();
	}
	send(type: string, data: unknown) {
		const text = typeof data === 'string' ? data : JSON.stringify(data);
		this.listeners.get(type)?.({ data: text } as MessageEvent<string>);
	}
	/** The connection drops, and the browser is going to reconnect. */
	drop() {
		this.readyState = 0;
		this.onerror?.();
	}
	/** The browser gives up on the connection (a 401, say). */
	giveUp() {
		this.readyState = 2;
		this.onerror?.();
	}
}

const status = (clients: number): StreamStatus => ({
	server: { tunnel_up: true, clients, paused: 0, online: 0 },
	clients: []
});

const event = (id: number, kind = 'client.added'): DrawbridgeEvent => ({
	id,
	time: '2026-10-03T12:00:00Z',
	kind,
	category: 'admin',
	actor: 'admin',
	via: 'web'
});

let hidden = false;
let visibility: (() => void) | undefined;

/** Each test gets a fresh copy of the module, since the feed is one per page. */
async function load() {
	vi.resetModules();
	const apiModule = await import('./api');
	const liveModule = await import('./live.svelte');
	liveModule.setEventSource((url) => new FakeSource(url));
	return { ...liveModule, api: apiModule.api };
}

function setHidden(value: boolean) {
	hidden = value;
	visibility?.();
}

beforeEach(() => {
	vi.useFakeTimers();
	FakeSource.all = [];
	hidden = false;
	visibility = undefined;
	vi.stubGlobal('document', {
		get hidden() {
			return hidden;
		},
		addEventListener: (_: string, fn: () => void) => (visibility = fn),
		removeEventListener: () => (visibility = undefined)
	});
});

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

describe('the live feed', () => {
	it('opens when a page asks, takes status messages, and says when it is open', async () => {
		const { live, useLive } = await load();
		expect(FakeSource.all).toHaveLength(0);
		const release = useLive();
		expect(FakeSource.all.map((s) => s.url)).toEqual(['/api/stream']);
		expect(live.open).toBe(false);

		const source = FakeSource.all[0];
		source.open();
		source.send('status', status(3));
		expect(live.open).toBe(true);
		expect(live.status?.server.clients).toBe(3);
		source.send('status', 'not json');
		expect(live.status?.server.clients).toBe(3);
		release();
	});

	it('hands each event to the pages that listen, until they stop', async () => {
		const { useLive, onLiveEvent } = await load();
		const release = useLive();
		const heard: number[] = [];
		const other: number[] = [];
		const stop = onLiveEvent((e) => heard.push(e.id));
		onLiveEvent((e) => other.push(e.id));
		const source = FakeSource.all[0];
		source.open();
		source.send('event', event(7));
		source.send('event', 'garbage');
		stop();
		source.send('event', event(8));
		expect(heard).toEqual([7]);
		expect(other).toEqual([7, 8]);
		release();
	});

	it('is one connection for every page, and survives moving from one page to the next', async () => {
		const { useLive } = await load();
		const first = useLive();
		const second = useLive();
		expect(FakeSource.all).toHaveLength(1);
		first();
		vi.advanceTimersByTime(5000);
		expect(FakeSource.all[0].closed).toBe(false); // the other page still wants it

		// A page unmounts and the next mounts in the same moment: no reconnect.
		second();
		const next = useLive();
		vi.advanceTimersByTime(5000);
		expect(FakeSource.all).toHaveLength(1);
		expect(FakeSource.all[0].closed).toBe(false);

		// Nobody wants it: it closes after a moment.
		next();
		vi.advanceTimersByTime(500);
		expect(FakeSource.all[0].closed).toBe(false);
		vi.advanceTimersByTime(1000);
		expect(FakeSource.all[0].closed).toBe(true);
	});

	it('closes when the tab is hidden, which stops it keeping the session alive, and reopens', async () => {
		const { live, useLive } = await load();
		const release = useLive();
		FakeSource.all[0].open();
		expect(live.open).toBe(true);

		setHidden(true);
		expect(FakeSource.all[0].closed).toBe(true);
		expect(live.open).toBe(false);
		expect(FakeSource.all).toHaveLength(1);

		setHidden(false);
		expect(FakeSource.all).toHaveLength(2);
		FakeSource.all[1].open();
		expect(live.open).toBe(true);
		release();
	});

	it('does not open in a hidden tab', async () => {
		hidden = true;
		const { useLive } = await load();
		const release = useLive();
		expect(FakeSource.all).toHaveLength(0);
		setHidden(false);
		expect(FakeSource.all).toHaveLength(1);
		release();
	});

	it('counts a reconnection, but not the first connection', async () => {
		const { live, useLive } = await load();
		const release = useLive();
		const source = FakeSource.all[0];
		source.open();
		expect(live.reconnects).toBe(0);

		source.drop(); // the browser reconnects by itself
		expect(live.open).toBe(false);
		source.open();
		expect(live.open).toBe(true);
		expect(live.reconnects).toBe(1);
		release();
	});

	it('asks who it is when the browser gives up, and tries again later', async () => {
		const { live, useLive, api } = await load();
		const me = vi.spyOn(api, 'me').mockRejectedValue(new Error('not logged in'));
		const release = useLive();
		const source = FakeSource.all[0];
		source.open();
		source.giveUp();
		expect(live.open).toBe(false);
		expect(me).toHaveBeenCalledTimes(1);

		vi.advanceTimersByTime(9000);
		expect(FakeSource.all).toHaveLength(1);
		vi.advanceTimersByTime(2000);
		expect(FakeSource.all).toHaveLength(2);
		FakeSource.all[1].open();
		expect(live.open).toBe(true);
		expect(live.reconnects).toBe(1);
		release();
	});

	it('does not try again in a hidden tab, or when nobody wants it', async () => {
		const { useLive, api } = await load();
		vi.spyOn(api, 'me').mockResolvedValue({} as never);
		const release = useLive();
		FakeSource.all[0].giveUp();
		setHidden(true);
		vi.advanceTimersByTime(30_000);
		expect(FakeSource.all).toHaveLength(1);
		setHidden(false);
		expect(FakeSource.all).toHaveLength(2);

		FakeSource.all[1].giveUp();
		release();
		vi.advanceTimersByTime(30_000);
		expect(FakeSource.all).toHaveLength(2);
	});

	it('ignores what a closed connection says after it was replaced', async () => {
		const { live, useLive, onLiveEvent } = await load();
		const heard: number[] = [];
		onLiveEvent((e) => heard.push(e.id));
		const release = useLive();
		const old = FakeSource.all[0];
		old.open();
		setHidden(true);
		setHidden(false);
		FakeSource.all[1].open();
		FakeSource.all[1].send('status', status(2));

		old.send('status', status(9));
		old.send('event', event(1));
		old.onerror?.();
		expect(live.status?.server.clients).toBe(2);
		expect(heard).toEqual([]);
		expect(live.open).toBe(true);
		release();
	});
});
