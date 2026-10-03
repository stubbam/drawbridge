import type { Client, DrawbridgeEvent } from './api';

/** How recent a handshake must be for a client to count as online (as on the server). */
export const ONLINE_WITHIN_MS = 3 * 60 * 1000;

/** A client's state, as the UI shows it. */
export type ClientState = 'online' | 'idle' | 'never' | 'paused' | 'down';

export const stateLabels: Record<ClientState, string> = {
	online: 'Online',
	idle: 'Idle',
	never: 'Never Connected',
	paused: 'Paused',
	down: 'Tunnel Down'
};

/**
 * The client's state: paused; online (a handshake in the last three minutes); idle (an
 * older handshake); never connected; or, when it's enabled but not in the tunnel, down.
 */
export function clientState(c: Client, now: number): ClientState {
	if (!c.enabled) return 'paused';
	if (!c.peer) return 'down';
	if (!c.peer.last_handshake) return 'never';
	return now - Date.parse(c.peer.last_handshake) < ONLINE_WITHIN_MS ? 'online' : 'idle';
}

/** Formats a byte count with decimal units: "0 B", "1.5 KB", "20 MB". */
export function formatBytes(n: number): string {
	const units = ['B', 'KB', 'MB', 'GB', 'TB'];
	let i = 0;
	while (n >= 1000 && i < units.length - 1) {
		n /= 1000;
		i++;
	}
	const digits = i === 0 || n >= 10 ? 0 : 1;
	return `${n.toFixed(digits)} ${units[i]}`;
}

/**
 * Formats a rate in bits per second with decimal units and three significant digits:
 * "0 bps", "272 bps", "62.4 Kbps", "3.71 Mbps".
 */
export function formatBitrate(bps: number): string {
	const units = ['bps', 'Kbps', 'Mbps', 'Gbps', 'Tbps'];
	let i = 0;
	while (bps >= 1000 && i < units.length - 1) {
		bps /= 1000;
		i++;
	}
	return `${Number(bps.toPrecision(3))} ${units[i]}`;
}

const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const pad = (n: number) => String(n).padStart(2, '0');

/**
 * A time of day on a 24-hour clock, in the browser's time zone: "18:24", or "18:24:05" with
 * seconds. Never am or pm.
 */
export function formatClock(d: Date, seconds = false): string {
	const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
	return seconds ? `${hm}:${pad(d.getSeconds())}` : hm;
}

/**
 * A date as the day and the month's English abbreviation: "9 Sep", or "9 Sep 2026" with the year.
 * Written out, not the browser's locale format, so every browser says it the same way.
 */
export function formatDay(d: Date, year = false): string {
	return `${d.getDate()} ${months[d.getMonth()]}${year ? ` ${d.getFullYear()}` : ''}`;
}

/**
 * Formats a chart's timestamp (seconds since the epoch) for its tooltip: "30 Sep at 18:24", or
 * with seconds when `seconds` is set, for samples a few seconds wide.
 */
export function formatChartTime(sec: number, seconds = false): string {
	const d = new Date(sec * 1000);
	return `${formatDay(d)} at ${formatClock(d, seconds)}`;
}

/** Formats how long ago a time was: "just now", "42 s ago", "5 min ago", "3 h ago", "2 d ago". */
export function formatAgo(iso: string | undefined, now: number): string {
	if (!iso) return 'never';
	const s = Math.max(0, Math.round((now - Date.parse(iso)) / 1000));
	if (s < 5) return 'just now';
	if (s < 60) return `${s} s ago`;
	if (s < 3600) return `${Math.floor(s / 60)} min ago`;
	if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
	return `${Math.floor(s / 86400)} d ago`;
}

/**
 * The address part of a peer's endpoint, without the port: "203.0.113.5:51820" is
 * "203.0.113.5", and "[2001:db8::1]:51820" is "2001:db8::1".
 */
export function endpointAddress(endpoint: string): string {
	if (endpoint.startsWith('[')) {
		const end = endpoint.indexOf(']');
		return end > 0 ? endpoint.slice(1, end) : endpoint;
	}
	const colon = endpoint.lastIndexOf(':');
	return colon > 0 ? endpoint.slice(0, colon) : endpoint;
}

/**
 * Formats a time for a list or a log, in the browser's time zone: "30 Sep 18:24:05". The year
 * is added when it isn't the current one, so an old entry doesn't read as this year's.
 */
export function formatTime(iso: string, now: Date = new Date()): string {
	const d = new Date(iso);
	if (Number.isNaN(d.getTime())) return iso;
	return `${formatDay(d, d.getFullYear() !== now.getFullYear())} ${formatClock(d, true)}`;
}

/** Every kind of event the daemon records, with its category and what the log calls it. */
const eventTypes: { kind: string; category: DrawbridgeEvent['category']; label: string }[] = [
	{ kind: 'client.connected', category: 'connection', label: 'Connected' },
	{ kind: 'client.disconnected', category: 'connection', label: 'Disconnected' },
	{ kind: 'client.roamed', category: 'connection', label: 'Roamed' },
	{ kind: 'client.added', category: 'admin', label: 'Added a client' },
	{ kind: 'client.renamed', category: 'admin', label: 'Renamed a client' },
	{ kind: 'client.paused', category: 'admin', label: 'Paused a client' },
	{ kind: 'client.resumed', category: 'admin', label: 'Resumed a client' },
	{ kind: 'client.deleted', category: 'admin', label: 'Deleted a client' },
	{ kind: 'client.config_viewed', category: 'admin', label: 'Viewed the config' },
	{ kind: 'server.settings_changed', category: 'admin', label: 'Changed server settings' },
	{
		kind: 'integration.adguard_changed',
		category: 'admin',
		label: 'Changed the AdGuard Home connection'
	},
	{
		kind: 'integration.adguard_removed',
		category: 'admin',
		label: 'Removed the AdGuard Home connection'
	},
	{
		kind: 'integration.adguard_name_added',
		category: 'system',
		label: 'Named a client in AdGuard Home'
	},
	{
		kind: 'integration.adguard_name_renamed',
		category: 'system',
		label: 'Renamed a client in AdGuard Home'
	},
	{
		kind: 'integration.adguard_name_removed',
		category: 'system',
		label: 'Removed a client from AdGuard Home'
	},
	{
		kind: 'integration.adguard_name_failed',
		category: 'system',
		label: "Couldn't name a client in AdGuard Home"
	},
	{
		kind: 'integration.adguard_sync_failed',
		category: 'system',
		label: 'AdGuard Home sync failed'
	},
	{
		kind: 'integration.adguard_sync_recovered',
		category: 'system',
		label: 'AdGuard Home sync recovered'
	},
	{ kind: 'auth.setup_completed', category: 'admin', label: 'Completed setup' },
	{ kind: 'auth.setup_failed', category: 'admin', label: 'Failed setup (wrong token)' },
	{ kind: 'auth.login', category: 'admin', label: 'Logged in' },
	{ kind: 'auth.login_failed', category: 'admin', label: 'Failed login' },
	{ kind: 'auth.logout', category: 'admin', label: 'Logged out' },
	{ kind: 'auth.password_changed', category: 'admin', label: 'Changed the password' },
	{ kind: 'auth.password_reset', category: 'admin', label: 'Reset the password (CLI)' },
	{ kind: 'auth.admin_created', category: 'admin', label: 'Created the admin account (CLI)' },
	{ kind: 'auth.session_revoked', category: 'admin', label: 'Revoked a session' },
	{ kind: 'auth.token_created', category: 'admin', label: 'Made an API token' },
	{ kind: 'auth.token_revoked', category: 'admin', label: 'Revoked an API token' },
	{
		kind: 'auth.token_failed',
		category: 'admin',
		label: 'Failed to make an API token (wrong password)'
	},
	{
		kind: 'auth.tokens_revoked',
		category: 'admin',
		label: 'Revoked every API token (password reset)'
	},
	{ kind: 'tunnel.drift_corrected', category: 'system', label: 'Corrected drift' }
];

const eventLabels = new Map(eventTypes.map((t) => [t.kind, t.label]));

/** Describes an event's kind for people; unknown kinds show as they are. */
export function eventLabel(kind: string): string {
	return eventLabels.get(kind) ?? kind;
}

/** The kinds of event in a category (or in every category), for the log's filter. */
export function eventKinds(
	category?: DrawbridgeEvent['category'] | ''
): { kind: string; label: string }[] {
	return eventTypes.filter((t) => !category || t.category === category);
}

/** Describes who caused an event: "admin (web, 192.168.4.20)", "root (CLI)", "Drawbridge". */
export function eventActor(e: DrawbridgeEvent): string {
	if (e.via === 'system') return 'Drawbridge';
	if (e.via === 'cli') return `${e.actor} (CLI)`;
	return e.source_ip ? `${e.actor || 'someone'} (web, ${e.source_ip})` : `${e.actor} (web)`;
}

/** The event's details as "key: value" pairs, sorted by key. */
export function eventDetails(e: DrawbridgeEvent): string {
	return Object.keys(e.data ?? {})
		.sort()
		.map((k) => `${k.replaceAll('_', ' ')}: ${e.data![k]}`)
		.join('; ');
}

/**
 * A file name for a client's config. The WireGuard apps name the tunnel after the file, and
 * Linux limits interface names to 15 characters. This matches the server's download name.
 */
export function configFileName(name: string): string {
	let out = '';
	for (const ch of name) {
		if (out.length === 15) break;
		if (/[A-Za-z0-9_=+.-]/.test(ch)) out += ch;
		else if (ch === ' ' || ch === "'") out += '-';
	}
	out = out.replace(/^[-.]+|[-.]+$/g, '');
	return (out || 'wireguard') + '.conf';
}

/** A short description of a browser from its user agent: "Firefox on Windows". */
export function describeUserAgent(ua: string): string {
	if (!ua) return 'Unknown browser';
	const browser = /Edg\//.test(ua)
		? 'Edge'
		: /Firefox\/|FxiOS\//.test(ua)
			? 'Firefox'
			: /Chrome\/|CriOS\//.test(ua)
				? 'Chrome'
				: /Safari\//.test(ua)
					? 'Safari'
					: /^curl\//.test(ua)
						? 'curl'
						: 'A browser';
	const os = /iPhone|iPad/.test(ua)
		? 'iOS'
		: /Android/.test(ua)
			? 'Android'
			: /Mac OS X/.test(ua)
				? 'macOS'
				: /Windows/.test(ua)
					? 'Windows'
					: /Linux/.test(ua)
						? 'Linux'
						: '';
	return os ? `${browser} on ${os}` : browser;
}
