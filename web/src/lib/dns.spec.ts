import { describe, expect, it } from 'vitest';
import type { Settings } from './api';
import { dnsFor, dnsModeOf, parseDNSList, publicDNS, serverDNS } from './dns';

const dual = {
	ipv4_address: '10.8.0.1',
	ipv6_address: 'fd3a:5c1e:92b0:1::1',
	dns: []
} as unknown as Settings;
const v4only = { ...dual, ipv6_address: '' } as Settings;

describe('serverDNS and publicDNS', () => {
	it('list the IPv6 address only when the VPN has one', () => {
		expect(serverDNS(dual)).toEqual(['10.8.0.1', 'fd3a:5c1e:92b0:1::1']);
		expect(serverDNS(v4only)).toEqual(['10.8.0.1']);
		expect(publicDNS(dual)).toHaveLength(4);
		expect(publicDNS(v4only)).toEqual(['1.1.1.1', '1.0.0.1']);
	});
});

describe('dnsModeOf', () => {
	it('recognizes each choice from the saved list', () => {
		expect(dnsModeOf({ ...dual, dns: [] })).toBe('none');
		expect(dnsModeOf({ ...dual, dns: serverDNS(dual) })).toBe('server');
		expect(dnsModeOf({ ...dual, dns: ['10.8.0.1'] })).toBe('server');
		expect(dnsModeOf({ ...dual, dns: publicDNS(dual) })).toBe('public');
		expect(dnsModeOf({ ...dual, dns: ['9.9.9.9'] })).toBe('custom');
		expect(dnsModeOf({ ...dual, dns: ['10.8.0.1', '9.9.9.9'] })).toBe('custom');
	});
});

describe('parseDNSList', () => {
	it('splits on commas and whitespace and drops blanks', () => {
		expect(parseDNSList(' 9.9.9.9,  2620:fe::fe\n149.112.112.112 ,')).toEqual([
			'9.9.9.9',
			'2620:fe::fe',
			'149.112.112.112'
		]);
		expect(parseDNSList('')).toEqual([]);
	});
});

describe('dnsFor', () => {
	it('saves only the addresses that answered for "this server"', () => {
		expect(dnsFor(dual, 'server', '', ['10.8.0.1'])).toEqual(['10.8.0.1']);
		expect(dnsFor(dual, 'server', '')).toEqual(serverDNS(dual));
	});
	it('keeps a saved "this server" list when nothing was checked', () => {
		expect(dnsFor({ ...dual, dns: ['10.8.0.1'] }, 'server', '')).toEqual(['10.8.0.1']);
	});
	it('maps the other choices', () => {
		expect(dnsFor(dual, 'public', '')).toEqual(publicDNS(dual));
		expect(dnsFor(dual, 'custom', '9.9.9.9, 149.112.112.112')).toEqual([
			'9.9.9.9',
			'149.112.112.112'
		]);
		expect(dnsFor(dual, 'none', '9.9.9.9')).toEqual([]);
	});
});
