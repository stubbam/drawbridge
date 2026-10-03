import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';

/** A persistent client, as much of it as the tests look at. */
export interface FakePersistent {
	name: string;
	ids: string[];
	use_global_settings?: boolean;
	[field: string]: unknown;
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
					return json({ enabled: true, anonymize_client_ip: true, interval: 86400000 });
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
		setDown: (d) => void (down = d),
		close: () => new Promise((resolve) => server.close(() => resolve()))
	};
}
