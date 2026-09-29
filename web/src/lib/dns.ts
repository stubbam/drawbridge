import type { Settings } from './api';

/** "server" means a resolver on this host; "public" is Cloudflare's, the default. */
export type DNSMode = 'server' | 'public' | 'custom' | 'none';

type Addresses = Pick<Settings, 'ipv4_address' | 'ipv6_address'>;

/** The server's own VPN addresses, which serve DNS only if a resolver listens there. */
export function serverDNS(s: Addresses): string[] {
	return [s.ipv4_address, s.ipv6_address].filter(Boolean);
}

/** Cloudflare's resolvers, matching the server's default; IPv6 only when the VPN has it. */
export function publicDNS(s: Pick<Settings, 'ipv6_address'>): string[] {
	const dns = ['1.1.1.1', '1.0.0.1'];
	if (s.ipv6_address) dns.push('2606:4700:4700::1111', '2606:4700:4700::1001');
	return dns;
}

function sameList(a: string[], b: string[]): boolean {
	return a.length === b.length && a.every((x, i) => x === b[i]);
}

/** Which choice the saved DNS servers correspond to. */
export function dnsModeOf(s: Settings): DNSMode {
	if (s.dns.length === 0) return 'none';
	const own = serverDNS(s);
	if (s.dns.every((a) => own.includes(a))) return 'server';
	return sameList(s.dns, publicDNS(s)) ? 'public' : 'custom';
}

/** Splits an admin's list of addresses on commas and whitespace. */
export function parseDNSList(text: string): string[] {
	return text
		.split(/[\s,]+/)
		.map((a) => a.trim())
		.filter(Boolean);
}

/**
 * The DNS servers a choice saves. "This server" saves only the addresses a check found
 * answering, when it ran, so a resolver bound to IPv4 alone doesn't leave clients waiting on
 * an IPv6 address that never replies. Without a check, a saved "this server" list stays as it is.
 */
export function dnsFor(
	s: Settings,
	mode: DNSMode,
	custom: string,
	usable: string[] = []
): string[] {
	switch (mode) {
		case 'server':
			if (usable.length > 0) return usable;
			return dnsModeOf(s) === 'server' ? s.dns : serverDNS(s);
		case 'public':
			return publicDNS(s);
		case 'custom':
			return parseDNSList(custom);
		case 'none':
			return [];
	}
}
