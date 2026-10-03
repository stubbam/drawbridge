import { describe, expect, it } from 'vitest';
import type { AdGuardConnection } from './api';
import { adguardRequest, needsPassword } from './adguard';

const saved: AdGuardConnection = {
	configured: true,
	base_url: 'http://127.0.0.1:3000/control',
	username: 'drawbridge',
	has_password: true,
	enabled: true,
	sync_names: true,
	sync: { state: 'ok', last_sync: null, synced: 0, conflicts: [] }
};
const same = {
	baseUrl: saved.base_url,
	username: saved.username,
	password: '',
	enabled: true,
	syncNames: true
};

describe('adguardRequest', () => {
	it('sends nothing when nothing changed', () => {
		expect(adguardRequest(saved, same)).toEqual({});
	});

	it('sends only what changed, trimmed', () => {
		expect(adguardRequest(saved, { ...same, baseUrl: ' http://192.0.2.7:3000 ' })).toEqual({
			base_url: 'http://192.0.2.7:3000'
		});
		expect(adguardRequest(saved, { ...same, username: ' other ' })).toEqual({ username: 'other' });
	});

	it('sends the switches when they change, and takes no password for them', () => {
		expect(adguardRequest(saved, { ...same, enabled: false })).toEqual({ enabled: false });
		expect(adguardRequest(saved, { ...same, syncNames: false })).toEqual({ sync_names: false });
		expect(needsPassword(saved, { ...same, enabled: false, syncNames: false })).toBe(false);
	});

	it('sends a password only when one is typed, as typed', () => {
		expect(adguardRequest(saved, { ...same, password: ' pa ss ' })).toEqual({
			password: ' pa ss '
		});
	});
});

describe('needsPassword', () => {
	it('is false while the address and the account are the saved ones', () => {
		expect(needsPassword(saved, same)).toBe(false);
	});

	it('is true when the address or the username changes and no password is typed', () => {
		expect(needsPassword(saved, { ...same, baseUrl: 'http://192.0.2.7:3000' })).toBe(true);
		expect(needsPassword(saved, { ...same, username: 'other' })).toBe(true);
	});

	it('is false once a password is typed', () => {
		expect(needsPassword(saved, { ...same, username: 'other', password: 'x' })).toBe(false);
	});

	it('is false when there is no saved password to protect, or no username to send it with', () => {
		expect(needsPassword({ ...saved, has_password: false }, { ...same, username: 'other' })).toBe(
			false
		);
		expect(needsPassword(saved, { ...same, username: '  ' })).toBe(false);
	});
});
