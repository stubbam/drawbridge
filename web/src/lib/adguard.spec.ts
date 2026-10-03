import { describe, expect, it } from 'vitest';
import type { AdGuardConnection } from './api';
import { adguardLogsUrl, adguardRequest, needsPassword } from './adguard';

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

describe('adguardLogsUrl', () => {
	it("opens a local install on the host the admin is browsing, at AdGuard Home's port", () => {
		expect(adguardLogsUrl('http://127.0.0.1:3000/control', 'pi.example.com', '10.8.0.2')).toBe(
			'http://pi.example.com:3000/#logs?search=%2210.8.0.2%22'
		);
		expect(adguardLogsUrl('http://localhost:3000/control', '192.168.4.10')).toBe(
			'http://192.168.4.10:3000/#logs'
		);
		expect(adguardLogsUrl('http://[::1]:3000/control', '[2001:db8::10]', 'fd00::2')).toBe(
			'http://[2001:db8::10]:3000/#logs?search=%22fd00%3A%3A2%22'
		);
	});

	it('keeps the address of an AdGuard Home that is somewhere else', () => {
		expect(adguardLogsUrl('https://dns.example.com/control', 'pi.example.com', '10.8.0.2')).toBe(
			'https://dns.example.com/#logs?search=%2210.8.0.2%22'
		);
		expect(adguardLogsUrl('http://192.0.2.5:3000/control/', 'pi.example.com')).toBe(
			'http://192.0.2.5:3000/#logs'
		);
	});

	it('keeps a path the web interface is served under', () => {
		expect(adguardLogsUrl('https://example.com/adguard/control', 'pi.example.com')).toBe(
			'https://example.com/adguard/#logs'
		);
	});

	it('gives nothing for an address that is not a URL', () => {
		expect(adguardLogsUrl('not a url', 'pi.example.com')).toBe('');
	});
});
