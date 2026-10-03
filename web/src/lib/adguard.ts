import type { AdGuardConnection, AdGuardRequest } from './api';

/** What the connection form holds. */
export interface AdGuardForm {
	baseUrl: string;
	username: string;
	/** Empty means "keep the saved one": the server never sends it back. */
	password: string;
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
