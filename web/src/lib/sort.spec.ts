import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Client } from './api';
import { sortClients, storedSort, storeSort, type SortKey } from './sort';

const now = Date.parse('2026-09-26T12:00:00Z');
const peer = (last_handshake?: string) => ({ receive_bytes: 0, send_bytes: 0, last_handshake });
const client = (name: string, ipv4: string, over: Partial<Client> = {}): Client => ({
	id: name,
	name,
	enabled: true,
	ipv4,
	ipv6: '',
	public_key: 'k',
	created_at: '2026-09-26T10:00:00Z',
	...over
});

// In the server's order, by IPv4 address.
const clients = [
	client('tablet', '10.8.0.2', { peer: peer() }), // never connected
	client('laptop', '10.8.0.3', { enabled: false }), // paused
	client('desk', '10.8.0.4', { peer: peer('2026-09-26T11:58:00Z') }), // online
	client('router', '10.8.0.5'), // tunnel down
	client('phone 2', '10.8.0.9', { peer: peer('2026-09-26T11:00:00Z') }), // idle
	client('Phone 10', '10.8.0.10', { peer: peer('2026-09-26T11:59:00Z') }) // online, most recent
];
const names = (key: SortKey) => sortClients(clients, key, now).map((c) => c.name);

describe('sortClients', () => {
	it('sorts by name, with numbers in order and case aside', () => {
		expect(names('name')).toEqual(['desk', 'laptop', 'phone 2', 'Phone 10', 'router', 'tablet']);
	});
	it('sorts by status, most active first, then by name', () => {
		expect(names('status')).toEqual(['desk', 'Phone 10', 'phone 2', 'tablet', 'laptop', 'router']);
	});
	it('sorts by the most recent handshake, with clients that have none last, by name', () => {
		expect(names('handshake')).toEqual([
			'Phone 10',
			'desk',
			'phone 2',
			'laptop',
			'router',
			'tablet'
		]);
	});
	it('sorts by IPv4 address as numbers, not text', () => {
		expect(names('ip')).toEqual(['tablet', 'laptop', 'desk', 'router', 'phone 2', 'Phone 10']);
	});
	it("doesn't change the list it's given", () => {
		const before = clients.map((c) => c.name);
		sortClients(clients, 'name', now);
		expect(clients.map((c) => c.name)).toEqual(before);
	});
});

describe('storedSort', () => {
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it('remembers the order per page, and ignores one the page no longer offers', () => {
		const stored = new Map<string, string>();
		vi.stubGlobal('localStorage', {
			getItem: (k: string) => stored.get(k) ?? null,
			setItem: (k: string, v: string) => stored.set(k, v)
		});
		expect(storedSort('dashboard', ['name', 'status'], 'name')).toBe('name');
		storeSort('dashboard', 'status');
		expect(storedSort('dashboard', ['name', 'status'], 'name')).toBe('status');
		expect(storedSort('clients', ['name', 'ip'], 'name')).toBe('name');

		storeSort('dashboard', 'ip');
		expect(storedSort('dashboard', ['name', 'status'], 'name')).toBe('name');
	});

	it('falls back when storage is blocked', () => {
		vi.stubGlobal('localStorage', {
			getItem: () => {
				throw new Error('blocked');
			},
			setItem: () => {
				throw new Error('blocked');
			}
		});
		storeSort('dashboard', 'status');
		expect(storedSort('dashboard', ['name', 'status'], 'name')).toBe('name');
	});
});
