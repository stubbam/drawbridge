import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError, onUnauthorized, setFetch } from './api';

function respond(status: number, body?: unknown, headers: Record<string, string> = {}) {
	const init: ResponseInit = { status, headers: { ...headers } };
	if (body !== undefined && typeof body !== 'string') {
		(init.headers as Record<string, string>)['Content-Type'] = 'application/json';
		return new Response(JSON.stringify(body), init);
	}
	return new Response(body as string | undefined, init);
}

afterEach(() => {
	setFetch((...args) => fetch(...args));
	onUnauthorized(undefined);
});

describe('api', () => {
	it('sends the CSRF header, and JSON bodies', async () => {
		const fetchFn = vi.fn(async () => respond(201, { client: { id: 'c1' } }));
		setFetch(fetchFn);
		await api.addClient('phone');
		expect(fetchFn).toHaveBeenCalledWith('/api/clients', {
			method: 'POST',
			headers: { 'X-Drawbridge': '1', 'Content-Type': 'application/json' },
			body: '{"name":"phone"}',
			credentials: 'same-origin'
		});
	});

	it('reports the server error message and Retry-After', async () => {
		setFetch(async () =>
			respond(429, { error: 'too many failed attempts' }, { 'Retry-After': '4' })
		);
		const err = await api.login('admin', 'x').catch((e: unknown) => e);
		expect(err).toBeInstanceOf(ApiError);
		expect((err as ApiError).status).toBe(429);
		expect((err as ApiError).message).toBe('too many failed attempts');
		expect((err as ApiError).retryAfter).toBe(4);
	});

	it('calls the unauthorized handler, except for a failed login', async () => {
		const handler = vi.fn();
		onUnauthorized(handler);
		setFetch(async () => respond(401, { error: 'not logged in' }));
		await expect(api.clients()).rejects.toThrow('not logged in');
		expect(handler).toHaveBeenCalledTimes(1);
		await expect(api.login('admin', 'wrong')).rejects.toThrow();
		expect(handler).toHaveBeenCalledTimes(1);
	});

	it('returns plain text for configs, and nothing for 204', async () => {
		setFetch(async () =>
			respond(200, '[Interface]\n', { 'Content-Type': 'text/plain; charset=utf-8' })
		);
		expect(await api.clientConfig('c1')).toBe('[Interface]\n');
		setFetch(async () => new Response(null, { status: 204 }));
		expect(await api.logout()).toBeUndefined();
	});

	it('builds the event query', async () => {
		const fetchFn = vi.fn(async () => respond(200, []));
		setFetch(fetchFn);
		await api.events({ category: 'admin', before: 42, client: '' });
		await api.events();
		expect(fetchFn.mock.calls.map((c) => (c as unknown[])[0])).toEqual([
			'/api/events?category=admin&before=42',
			'/api/events'
		]);
	});

	it('builds the traffic and session-history requests', async () => {
		const fetchFn = vi.fn(async () => respond(200, []));
		setFetch(fetchFn);
		await api.traffic();
		await api.traffic('7d');
		await api.clientTraffic('c1');
		await api.clientTraffic('c1', '90d');
		await api.clientTraffic('c1', '1m');
		await api.clientSessions('c1');
		await api.clientSessions('c1', '2026-09-28T00:00:00Z', 10);
		expect(fetchFn.mock.calls.map((c) => (c as unknown[])[0])).toEqual([
			'/api/traffic?range=24h',
			'/api/traffic?range=7d',
			'/api/clients/c1/traffic?range=24h',
			'/api/clients/c1/traffic?range=90d',
			'/api/clients/c1/traffic?range=1m',
			'/api/clients/c1/sessions',
			'/api/clients/c1/sessions?before=2026-09-28T00%3A00%3A00Z&limit=10'
		]);
	});
});
