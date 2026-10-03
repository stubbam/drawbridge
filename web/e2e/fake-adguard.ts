import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';

/** A persistent client, as much of it as the tests look at. */
export interface FakePersistent {
	name: string;
	ids: string[];
	use_global_settings?: boolean;
	[field: string]: unknown;
}

/** A query in the fake's log. */
export interface FakeQuery {
	client: string;
	domain: string;
	/** A, AAAA, and so on; A when left out. */
	type?: string;
	/** Blocked by this rule, answering 0.0.0.0. */
	blockedBy?: string;
	answers?: string[];
	/** When it happened; now when left out. Later queries come first. */
	time?: Date;
}

export interface FakeAdGuard {
	/** Where its API is, as an admin would type it: no path, so `/control` is added. */
	url: string;
	/** The calls it accepted, like "GET /control/status". */
	accepted: string[];
	/** How many calls had the wrong account. */
	refused: () => number;
	/** Its persistent clients, as they are now. */
	clients: () => FakePersistent[];
	/** Adds a client the way the admin would in AdGuard Home's own UI. */
	addClient: (name: string, ids: string[]) => void;
	removeClient: (name: string) => void;
	/** Puts a query in its log. */
	addQuery: (q: FakeQuery) => void;
	/** Sets whether the log is on, and whether it hides each client's address. */
	setLog: (enabled: boolean, anonymize: boolean) => void;
	/** Makes it unreachable (the connection ends with no answer), or reachable again. */
	setDown: (down: boolean) => void;
	close: () => Promise<void>;
}

/**
 * A pretend AdGuard Home with the parts Drawbridge calls: who it is, how its query log is set,
 * and persistent clients. The daemon calls it, not the browser, and both run on this machine. It
 * hides each client's address in its log, so the test has a warning to show, and, like the real
 * one, it stores a client added without the global settings as not using them.
 */
export async function startFakeAdGuard(user: string, password: string): Promise<FakeAdGuard> {
	const accepted: string[] = [];
	let refused = 0;
	let clients: FakePersistent[] = [];
	let down = false;
	let logOn = true;
	let anonymize = true;
	const log: (FakeQuery & { time: Date })[] = [];
	const want = 'Basic ' + Buffer.from(`${user}:${password}`).toString('base64');

	/** Adds or replaces a client, refusing as AdGuard Home does: a 400 and a line of text. */
	function put(replacing: string | null, given: FakePersistent): string | null {
		const verb = replacing === null ? 'adding client' : 'updating client';
		if (!given.name) return `${verb}: empty name`;
		if (!given.ids?.length) return `${verb}: id required`;
		const at = replacing === null ? -1 : clients.findIndex((c) => c.name === replacing);
		if (replacing !== null && at < 0) return `${verb}: client "${replacing}" is not found`;
		for (const [i, c] of clients.entries()) {
			if (i === at) continue;
			if (c.name === given.name) return `${verb}: another client uses the same name "${c.name}"`;
			const shared = given.ids.find((id) => c.ids.includes(id));
			if (shared) return `${verb}: another client "${c.name}" uses the same IP "${shared}"`;
		}
		const next = { use_global_settings: false, use_global_blocked_services: false, ...given };
		if (at < 0) clients.push(next);
		else clients[at] = next;
		return null;
	}

	const server = createServer((req, res) => {
		if (down) {
			req.socket.destroy();
			return;
		}
		if (req.headers.authorization !== want) {
			refused++;
			res.writeHead(401).end();
			return;
		}
		accepted.push(`${req.method} ${req.url}`);
		const chunks: Buffer[] = [];
		req.on('data', (c: Buffer) => chunks.push(c));
		req.on('end', () => {
			const body = chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : {};
			const json = (v: unknown) => {
				res.setHeader('Content-Type', 'application/json');
				res.end(JSON.stringify(v));
			};
			const refuse = (msg: string) => res.writeHead(400, { 'Content-Type': 'text/plain' }).end(msg);
			const done = (err: string | null) => (err ? refuse(err) : res.end());
			const url = new URL(req.url ?? '/', 'http://fake');
			if (req.method === 'GET' && url.pathname === '/control/querylog') {
				// Like AdGuard Home's, the search matches part of the client's address or the name.
				const search = url.searchParams.get('search') ?? '';
				const limit = Number(url.searchParams.get('limit') ?? 500);
				const olderThan = url.searchParams.get('older_than');
				const found = log
					.filter((q) => !olderThan || q.time < new Date(olderThan))
					.filter((q) => !search || q.client.includes(search) || q.domain.includes(search))
					.sort((a, b) => b.time.getTime() - a.time.getTime())
					.slice(0, limit);
				return json({
					oldest: found.length ? found[found.length - 1].time.toISOString() : '',
					data: found.map((q) => ({
						time: q.time.toISOString(),
						client: q.client,
						question: { class: 'IN', name: q.domain, type: q.type ?? 'A' },
						status: 'NOERROR',
						reason: q.blockedBy ? 'FilteredBlackList' : 'NotFilteredNotFound',
						rules: q.blockedBy ? [{ filter_list_id: 0, text: q.blockedBy }] : [],
						answer: (q.blockedBy ? ['0.0.0.0'] : (q.answers ?? [])).map((value) => ({
							type: q.type ?? 'A',
							value,
							ttl: 60
						})),
						cached: false,
						upstream: '9.9.9.9:53',
						elapsedMs: '12.5'
					}))
				});
			}
			switch (`${req.method} ${req.url}`) {
				case 'GET /control/status':
					return json({
						version: 'v0.107.79',
						running: true,
						protection_enabled: true,
						dns_addresses: ['127.0.0.1'],
						dns_port: 53,
						http_port: 3000
					});
				case 'GET /control/querylog/config':
					return json({ enabled: logOn, anonymize_client_ip: anonymize, interval: 86400000 });
				case 'GET /control/clients':
					return json({
						clients: clients.length ? clients : null,
						auto_clients: [],
						supported_tags: []
					});
				case 'POST /control/clients/add':
					return done(put(null, body));
				case 'POST /control/clients/update':
					return done(put(body.name, body.data));
				case 'POST /control/clients/delete': {
					const at = clients.findIndex((c) => c.name === body.name);
					if (at < 0) return refuse('Client not found');
					clients.splice(at, 1);
					return res.end();
				}
				default:
					res.writeHead(404).end();
			}
		});
	});
	await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
	const { port } = server.address() as AddressInfo;
	return {
		url: `http://127.0.0.1:${port}`,
		accepted,
		refused: () => refused,
		clients: () => structuredClone(clients),
		addClient: (name, ids) => void put(null, { name, ids }),
		removeClient: (name) => void (clients = clients.filter((c) => c.name !== name)),
		addQuery: (q) => void log.push({ ...q, time: q.time ?? new Date() }),
		setLog: (enabled, hide) => void ((logOn = enabled), (anonymize = hide)),
		setDown: (d) => void (down = d),
		close: () => new Promise((resolve) => server.close(() => resolve()))
	};
}
