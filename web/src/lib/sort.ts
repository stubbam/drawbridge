import type { Client } from './api';
import { clientState, type ClientState } from './format';

// The orders the client lists offer. The server returns clients by IPv4 address; the
// dashboard and the Clients page sort them in the browser, each in its own natural
// direction, and remember the choice per page in this browser.
export type SortKey = 'name' | 'status' | 'handshake' | 'ip';

export const sortLabels: Record<SortKey, string> = {
	name: 'Name',
	status: 'Status',
	handshake: 'Last Handshake',
	ip: 'IP Address'
};

// Most active first.
const stateOrder: ClientState[] = ['online', 'idle', 'never', 'paused', 'down'];

// "phone 2" before "phone 10", and case doesn't matter (names are unique regardless of case).
const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

function byName(a: Client, b: Client): number {
	return collator.compare(a.name, b.name);
}

function ipv4Number(ip: string): number {
	return ip.split('.').reduce((n, octet) => n * 256 + Number(octet), 0);
}

function byIP(a: Client, b: Client): number {
	return ipv4Number(a.ipv4) - ipv4Number(b.ipv4);
}

function handshakeTime(c: Client): number {
	return c.peer?.last_handshake ? Date.parse(c.peer.last_handshake) : -Infinity;
}

/**
 * Returns the clients in a new array, sorted by key: names A to Z; status from online to
 * idle, never connected, paused, and tunnel down; the most recent handshake first, with
 * clients that have none last; or IPv4 address, lowest first. Ties go by name.
 */
export function sortClients(clients: readonly Client[], key: SortKey, now: number): Client[] {
	const compare: Record<SortKey, (a: Client, b: Client) => number> = {
		name: byName,
		status: (a, b) =>
			stateOrder.indexOf(clientState(a, now)) - stateOrder.indexOf(clientState(b, now)),
		// Not a subtraction: two clients without a handshake (-Infinity) would give NaN.
		handshake: (a, b) => {
			const ta = handshakeTime(a);
			const tb = handshakeTime(b);
			return ta === tb ? 0 : ta > tb ? -1 : 1;
		},
		ip: byIP
	};
	return [...clients].sort((a, b) => compare[key](a, b) || byName(a, b) || byIP(a, b));
}

const storagePrefix = 'drawbridge-sort-';

/** The order this browser last chose on a page, if it's still one the page offers. */
export function storedSort(page: string, options: readonly SortKey[], fallback: SortKey): SortKey {
	try {
		const v = localStorage.getItem(storagePrefix + page);
		return options.find((o) => o === v) ?? fallback;
	} catch {
		return fallback;
	}
}

/** Remembers a page's order for next time. */
export function storeSort(page: string, key: SortKey) {
	try {
		localStorage.setItem(storagePrefix + page, key);
	} catch {
		// Private browsing or blocked storage: the order still applies until the page closes.
	}
}
