// A typed client for Drawbridge's API (internal/api/openapi.json on the Go side). The
// field names match the server's JSON exactly, so nothing is renamed in between.

/** An error response from the API. */
export class ApiError extends Error {
	constructor(
		/** The HTTP status. */
		readonly status: number,
		message: string,
		/** Seconds to wait before retrying, for a 429. */
		readonly retryAfter?: number
	) {
		super(message);
		this.name = 'ApiError';
	}
}

export interface Settings {
	interface: string;
	listen_port: number;
	endpoint_host: string;
	endpoint_port: number;
	/** host:port as clients see it; empty until endpoint_host is set. */
	endpoint: string;
	public_key: string;
	mtu: number;
	ipv4_subnet: string;
	ipv4_address: string;
	/** Empty when IPv6 is off. */
	ipv6_subnet: string;
	ipv6_address: string;
	dns: string[];
	keepalive: number;
	client_isolation: boolean;
	client_allowed_ips: string[];
	/** Extra sources, besides the home network and the VPN, that may reach this UI. */
	admin_allowed: string[];
}

/** Changes to the settings; omitted fields stay as they are. */
export interface SettingsPatch {
	endpoint_host?: string;
	endpoint_port?: number;
	listen_port?: number;
	mtu?: number;
	dns?: string[];
	/** Reset dns to the server's VPN addresses; use dns for anything else. */
	dns_default?: boolean;
	keepalive?: number;
	client_isolation?: boolean;
	admin_allowed?: string[];
}

export interface SettingsResult {
	settings: Settings;
	warning?: string;
	apply_failed?: boolean;
}

export interface ServerStatus {
	tunnel_up: boolean;
	clients: number;
	paused: number;
	online: number;
}

/** What asking one of the server's VPN addresses for DNS found. */
export interface DNSProbe {
	address: string;
	answered: boolean;
	detail: string;
}

export interface DNSCheck {
	results: DNSProbe[];
	/** The addresses that answered; empty when no resolver runs on the host. */
	usable: string[];
}

export interface Peer {
	endpoint?: string;
	last_handshake?: string;
	/** All-time total; resets only when the peer is recreated (a pause and resume, or the
	 *  tunnel restarting). */
	receive_bytes: number;
	send_bytes: number;
	/** When the client's current connection began. Absent when it has none. */
	session_started_at?: string;
	/** Bytes transferred during the client's current connection. Absent when it has none. */
	session_receive_bytes?: number;
	session_send_bytes?: number;
}

export interface Client {
	id: string;
	name: string;
	enabled: boolean;
	ipv4: string;
	/** Empty when IPv6 is off. */
	ipv6: string;
	public_key: string;
	created_at: string;
	/** Absent when the client isn't in the tunnel: it's paused, or the tunnel is down. */
	peer?: Peer;
}

export interface ClientResult {
	client: Client;
	warning?: string;
	apply_failed?: boolean;
}

export interface User {
	username: string;
	created_at: string;
	last_login_at?: string;
}

export interface Session {
	id: string;
	created_at: string;
	last_seen_at: string;
	expires_at: string;
	ip: string;
	user_agent: string;
	current: boolean;
}

export interface Me {
	user: User;
	session: Session;
}

export interface DrawbridgeEvent {
	id: number;
	time: string;
	kind: string;
	category: 'admin' | 'system' | 'connection';
	actor: string;
	via: 'web' | 'cli' | 'system';
	source_ip?: string;
	client_id?: string;
	client_name?: string;
	data?: Record<string, string>;
}

export interface EventFilter {
	client?: string;
	category?: 'admin' | 'system' | 'connection';
	before?: number;
	limit?: number;
}

/**
 * 1m reads the last minute's polls, which the daemon keeps in memory. 1h, 12h, and 24h read raw
 * samples; 7d, 30d, and 90d read the hourly rollup.
 */
export type TrafficRange = '1m' | '1h' | '12h' | '24h' | '7d' | '30d' | '90d';

export interface TrafficSample {
	bucket_start: string;
	receive_bytes: number;
	send_bytes: number;
}

/** One series of traffic history, with the window it covers: one client's, or every client's summed. */
export interface TrafficSeries {
	/** How long one sample covers, so a sample's bytes over it is a rate. */
	step_seconds: number;
	/** Where the history ends: every sample is complete, and starts before it. */
	until: string;
	/** Oldest first. A stored bucket in which nothing moved isn't saved: it's zero. */
	samples: TrafficSample[];
}

/** One client's samples in a TrafficHistory. */
export interface ClientTrafficSeries {
	id: string;
	name: string;
	/** Oldest first. A stored bucket in which the client moved nothing isn't saved: it's zero. */
	samples: TrafficSample[];
}

/** Every client's traffic over a range, for the charts page. */
export interface TrafficHistory {
	/** How long one sample covers, so a sample's bytes over it is a rate. */
	step_seconds: number;
	/** Where the history ends: every sample is complete, and ends at or before it. */
	until: string;
	/** Every client, sorted by name. */
	clients: ClientTrafficSeries[];
}

export interface ClientSession {
	id: string;
	started_at: string;
	/** Absent while the session is still open. */
	ended_at?: string;
	endpoint: string;
	receive_bytes: number;
	send_bytes: number;
}

type Method = 'GET' | 'POST' | 'PATCH' | 'DELETE';

/** Called when a request finds the session has ended, so the app can go to the login. */
let unauthorized: (() => void) | undefined;

/** Sets what happens when a request finds the session has ended. */
export function onUnauthorized(handler: (() => void) | undefined): void {
	unauthorized = handler;
}

/** The fetch function requests use; tests replace it. */
let fetchFn: typeof fetch = (...args) => fetch(...args);

/** Replaces the fetch function, for tests. */
export function setFetch(fn: typeof fetch): void {
	fetchFn = fn;
}

async function request<T>(method: Method, path: string, body?: unknown): Promise<T> {
	// Every change carries X-Drawbridge, which a cross-site request can't (the CSRF check).
	const headers: Record<string, string> = { 'X-Drawbridge': '1' };
	let payload: string | undefined;
	if (body !== undefined) {
		headers['Content-Type'] = 'application/json';
		payload = JSON.stringify(body);
	}
	const res = await fetchFn(path, { method, headers, body: payload, credentials: 'same-origin' });
	if (res.status === 204) {
		return undefined as T;
	}
	const text = await res.text();
	let data: unknown = text;
	if (text && (res.headers.get('Content-Type') ?? '').startsWith('application/json')) {
		data = JSON.parse(text);
	}
	if (!res.ok) {
		if (res.status === 401 && !path.startsWith('/api/auth/login')) {
			unauthorized?.();
		}
		const message =
			typeof data === 'object' &&
			data !== null &&
			typeof (data as { error?: unknown }).error === 'string'
				? (data as { error: string }).error
				: `${method} ${path} failed with status ${res.status}`;
		const retry = Number(res.headers.get('Retry-After'));
		throw new ApiError(
			res.status,
			message,
			Number.isFinite(retry) && retry > 0 ? retry : undefined
		);
	}
	return data as T;
}

const clientPath = (id: string, suffix = '') => `/api/clients/${encodeURIComponent(id)}${suffix}`;

export const api = {
	setupStatus: () => request<{ needed: boolean }>('GET', '/api/setup'),
	setup: (token: string, username: string, password: string) =>
		request<Me>('POST', '/api/setup', { token, username, password }),
	login: (username: string, password: string) =>
		request<Me>('POST', '/api/auth/login', { username, password }),
	logout: () => request<void>('POST', '/api/auth/logout'),
	me: () => request<Me>('GET', '/api/auth/me'),
	changePassword: (current_password: string, new_password: string) =>
		request<void>('POST', '/api/auth/password', { current_password, new_password }),
	sessions: () => request<Session[]>('GET', '/api/auth/sessions'),
	revokeSession: (id: string) =>
		request<void>('DELETE', `/api/auth/sessions/${encodeURIComponent(id)}`),

	server: () => request<Settings>('GET', '/api/server'),
	updateServer: (patch: SettingsPatch) => request<SettingsResult>('PATCH', '/api/server', patch),
	status: () => request<ServerStatus>('GET', '/api/server/status'),
	dnsCheck: () => request<DNSCheck>('GET', '/api/server/dns-check'),

	clients: () => request<Client[]>('GET', '/api/clients'),
	client: (id: string) => request<Client>('GET', clientPath(id)),
	addClient: (name: string) => request<ClientResult>('POST', '/api/clients', { name }),
	renameClient: (id: string, name: string) =>
		request<ClientResult>('PATCH', clientPath(id), { name }),
	pauseClient: (id: string) => request<ClientResult>('POST', clientPath(id, '/pause')),
	resumeClient: (id: string) => request<ClientResult>('POST', clientPath(id, '/resume')),
	deleteClient: (id: string) => request<ClientResult>('DELETE', clientPath(id)),
	/** The client's WireGuard config. Every download is recorded in the event log. */
	clientConfig: (id: string) => request<string>('GET', clientPath(id, '/config')),

	events: (filter: EventFilter = {}) => {
		const q = new URLSearchParams();
		for (const [k, v] of Object.entries(filter)) {
			if (v !== undefined && v !== '') q.set(k, String(v));
		}
		const qs = q.toString();
		return request<DrawbridgeEvent[]>('GET', '/api/events' + (qs ? '?' + qs : ''));
	},

	/** Every client's traffic history, summed: the dashboard's total-throughput chart. */
	traffic: (range: TrafficRange = '24h') =>
		request<TrafficSeries>('GET', '/api/traffic?range=' + range),
	/** Every client's traffic history, one series per client: the charts page. */
	trafficByClient: (range: TrafficRange = '24h') =>
		request<TrafficHistory>('GET', '/api/traffic/clients?range=' + range),
	/** One client's traffic history. */
	clientTraffic: (id: string, range: TrafficRange = '24h') =>
		request<TrafficSeries>('GET', clientPath(id, '/traffic?range=' + range)),
	/** One client's connection history, newest first: open and closed sessions alike. */
	clientSessions: (id: string, before?: string, limit?: number) => {
		const q = new URLSearchParams();
		if (before) q.set('before', before);
		if (limit) q.set('limit', String(limit));
		const qs = q.toString();
		return request<ClientSession[]>('GET', clientPath(id, '/sessions' + (qs ? '?' + qs : '')));
	}
};
