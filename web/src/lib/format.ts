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

/**
 * Formats a chart's timestamp (seconds since the epoch) in the browser's time zone: "Sep 30,
 * 6:24 PM", or with seconds when `seconds` is set, for samples a few seconds wide.
 */
export function formatChartTime(sec: number, seconds = false): string {
	return new Date(sec * 1000).toLocaleString(undefined, {
		month: 'short',
		day: 'numeric',
		hour: 'numeric',
		minute: '2-digit',
		second: seconds ? '2-digit' : undefined
	});
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

/** Formats a time in the browser's time zone. */
export function formatTime(iso: string): string {
	return new Date(iso).toLocaleString();
}

const eventLabels: Record<string, string> = {
	'client.added': 'Added a client',
	'client.renamed': 'Renamed a client',
	'client.paused': 'Paused a client',
	'client.resumed': 'Resumed a client',
	'client.deleted': 'Deleted a client',
	'client.config_viewed': 'Viewed the config',
	'server.settings_changed': 'Changed server settings',
	'auth.setup_completed': 'Completed setup',
	'auth.setup_failed': 'Failed setup (wrong token)',
	'auth.login': 'Logged in',
	'auth.login_failed': 'Failed login',
	'auth.logout': 'Logged out',
	'auth.password_changed': 'Changed the password',
	'auth.password_reset': 'Reset the password (CLI)',
	'auth.admin_created': 'Created the admin account (CLI)',
	'auth.session_revoked': 'Revoked a session',
	'tunnel.drift_corrected': 'Corrected drift',
	'client.connected': 'Connected',
	'client.disconnected': 'Disconnected',
	'client.roamed': 'Roamed'
};

/** Describes an event's kind for people; unknown kinds show as they are. */
export function eventLabel(kind: string): string {
	return eventLabels[kind] ?? kind;
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
