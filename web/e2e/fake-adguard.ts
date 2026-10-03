import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';

export interface FakeAdGuard {
	/** Where its API is, as an admin would type it: no path, so `/control` is added. */
	url: string;
	/** The calls it accepted, like "GET /control/status". */
	accepted: string[];
	/** How many calls had the wrong account. */
	refused: () => number;
	close: () => Promise<void>;
}

/**
 * A pretend AdGuard Home with only what the connection test asks it: who it is, and how its
 * query log is set. The daemon calls it, not the browser, and both run on this machine. It
 * hides each client's address in its log, so the test has a warning to show.
 */
export async function startFakeAdGuard(user: string, password: string): Promise<FakeAdGuard> {
	const accepted: string[] = [];
	let refused = 0;
	const want = 'Basic ' + Buffer.from(`${user}:${password}`).toString('base64');
	const server = createServer((req, res) => {
		if (req.headers.authorization !== want) {
			refused++;
			res.writeHead(401).end();
			return;
		}
		accepted.push(`${req.method} ${req.url}`);
		res.setHeader('Content-Type', 'application/json');
		if (req.method === 'GET' && req.url === '/control/status') {
			res.end(
				JSON.stringify({
					version: 'v0.107.79',
					running: true,
					protection_enabled: true,
					dns_addresses: ['127.0.0.1'],
					dns_port: 53,
					http_port: 3000
				})
			);
		} else if (req.method === 'GET' && req.url === '/control/querylog/config') {
			res.end(JSON.stringify({ enabled: true, anonymize_client_ip: true, interval: 86400000 }));
		} else {
			res.writeHead(404).end();
		}
	});
	await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
	const { port } = server.address() as AddressInfo;
	return {
		url: `http://127.0.0.1:${port}`,
		accepted,
		refused: () => refused,
		close: () => new Promise((resolve) => server.close(() => resolve()))
	};
}
