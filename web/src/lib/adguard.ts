import type { AdGuardConnection, AdGuardRequest } from './api';

/** What the connection form holds. */
export interface AdGuardForm {
	baseUrl: string;
	username: string;
	/** Empty means "keep the saved one": the server never sends it back. */
	password: string;
	enabled: boolean;
	syncNames: boolean;
}

/**
 * The request a form makes: only what differs from what's saved, so the server's event log says
 * what changed. A password is sent when one is typed, and never otherwise.
 */
export function adguardRequest(saved: AdGuardConnection, f: AdGuardForm): AdGuardRequest {
	const r: AdGuardRequest = {};
	if (f.baseUrl.trim() !== saved.base_url) r.base_url = f.baseUrl.trim();
	if (f.username.trim() !== saved.username) r.username = f.username.trim();
	if (f.password !== '') r.password = f.password;
	if (f.enabled !== saved.enabled) r.enabled = f.enabled;
	if (f.syncNames !== saved.sync_names) r.sync_names = f.syncNames;
	return r;
}

/**
 * Whether the form changes where the saved password would go, without a password to send. The
 * server refuses that, because the saved password is only sent to the address and account it was
 * saved with, so the form says so before it's asked. Dropping the username drops the password.
 */
export function needsPassword(saved: AdGuardConnection, f: AdGuardForm): boolean {
	if (!saved.has_password || f.password !== '') return false;
	const username = f.username.trim();
	return username !== '' && (f.baseUrl.trim() !== saved.base_url || username !== saved.username);
}

/**
 * A link to AdGuard Home's own query log, searching for one address. The saved address is the one
 * the daemon uses, and for a local install it's 127.0.0.1, which only the host itself can open. So
 * when it's a loopback address, the link goes to the host the admin is browsing Drawbridge on,
 * with AdGuard Home's port. Returns "" for an address that isn't a URL.
 *
 * @param pageHost the browser's `location.hostname`, brackets and all for an IPv6 address.
 */
export function adguardLogsUrl(baseUrl: string, pageHost: string, address?: string): string {
	let u: URL;
	try {
		u = new URL(baseUrl);
	} catch {
		return '';
	}
	const loopback = /^(localhost|127(\.\d{1,3}){3}|\[::1\])$/i.test(u.hostname);
	const host = loopback ? pageHost : u.hostname;
	// The API is at <web address>/control, and the web interface at the web address.
	const path = u.pathname.replace(/\/+$/, '').replace(/\/control$/, '');
	const search = address ? `?search=${encodeURIComponent(`"${address}"`)}` : '';
	return `${u.protocol}//${host}${u.port ? ':' + u.port : ''}${path}/#logs${search}`;
}
