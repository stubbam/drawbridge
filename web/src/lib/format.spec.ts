import { describe, expect, it } from 'vitest';
import type { Client, DrawbridgeEvent } from './api';
import {
	clientState,
	configFileName,
	describeUserAgent,
	endpointAddress,
	eventActor,
	eventDetails,
	eventLabel,
	formatAgo,
	formatBitrate,
	formatBytes,
	formatChartTime
} from './format';

const now = Date.parse('2026-09-26T12:00:00Z');
const client = (over: Partial<Client>): Client => ({
	id: 'c1',
	name: 'phone',
	enabled: true,
	ipv4: '10.8.0.2',
	ipv6: 'fd00::2',
	public_key: 'k',
	created_at: '2026-09-26T10:00:00Z',
	...over
});

describe('clientState', () => {
	it('tells the states apart', () => {
		const peer = { receive_bytes: 0, send_bytes: 0 };
		expect(clientState(client({ enabled: false }), now)).toBe('paused');
		expect(clientState(client({}), now)).toBe('down');
		expect(clientState(client({ peer }), now)).toBe('never');
		expect(
			clientState(client({ peer: { ...peer, last_handshake: '2026-09-26T11:58:30Z' } }), now)
		).toBe('online');
		expect(
			clientState(client({ peer: { ...peer, last_handshake: '2026-09-26T11:50:00Z' } }), now)
		).toBe('idle');
	});
});

describe('formatBytes', () => {
	it('uses decimal units', () => {
		expect(formatBytes(0)).toBe('0 B');
		expect(formatBytes(999)).toBe('999 B');
		expect(formatBytes(1500)).toBe('1.5 KB');
		expect(formatBytes(20 * 1000 * 1000)).toBe('20 MB');
		expect(formatBytes(3 * 1000 ** 4)).toBe('3.0 TB');
	});
});

describe('endpointAddress', () => {
	it('drops the port, and the brackets around IPv6', () => {
		expect(endpointAddress('203.0.113.5:51820')).toBe('203.0.113.5');
		expect(endpointAddress('[2001:db8::1]:51820')).toBe('2001:db8::1');
		expect(endpointAddress('203.0.113.5')).toBe('203.0.113.5');
	});
});

describe('formatAgo', () => {
	it('rounds to a readable unit', () => {
		expect(formatAgo(undefined, now)).toBe('never');
		expect(formatAgo('2026-09-26T11:59:58Z', now)).toBe('just now');
		expect(formatAgo('2026-09-26T11:59:18Z', now)).toBe('42 s ago');
		expect(formatAgo('2026-09-26T11:55:00Z', now)).toBe('5 min ago');
		expect(formatAgo('2026-09-26T09:00:00Z', now)).toBe('3 h ago');
		expect(formatAgo('2026-09-24T12:00:00Z', now)).toBe('2 d ago');
	});
});

describe('events', () => {
	const e = (over: Partial<DrawbridgeEvent>): DrawbridgeEvent => ({
		id: 1,
		time: '2026-09-26T12:00:00Z',
		kind: 'client.added',
		category: 'admin',
		actor: 'admin',
		via: 'web',
		...over
	});
	it('describes the actor', () => {
		expect(eventActor(e({ source_ip: '192.168.4.20' }))).toBe('admin (web, 192.168.4.20)');
		expect(eventActor(e({ via: 'cli', actor: 'root' }))).toBe('root (CLI)');
		expect(eventActor(e({ via: 'system', actor: 'drawbridge' }))).toBe('Drawbridge');
	});
	it('labels kinds, and passes unknown ones through', () => {
		expect(eventLabel('client.paused')).toBe('Paused a client');
		expect(eventLabel('something.new')).toBe('something.new');
	});
	it('lists details sorted by key', () => {
		expect(eventDetails(e({ data: { mtu: '1420 → 1380', dns: 'a → b' } }))).toBe(
			'dns: a → b; mtu: 1420 → 1380'
		);
		expect(eventDetails(e({}))).toBe('');
	});
});

describe('configFileName', () => {
	it('matches the server', () => {
		expect(configFileName("Alex's iPhone")).toBe('Alex-s-iPhone.conf');
		expect(configFileName('Pixel')).toBe('Pixel.conf');
		expect(configFileName('a very long client name indeed')).toBe('a-very-long-cli.conf');
		expect(configFileName('日本')).toBe('wireguard.conf');
	});
});

describe('describeUserAgent', () => {
	it('names the browser and the system', () => {
		const chromeLinux =
			'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36';
		const safariIPhone =
			'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1';
		const firefoxWindows =
			'Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0';
		const edgeMac =
			'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36 Edg/141.0';
		expect(describeUserAgent(chromeLinux)).toBe('Chrome on Linux');
		expect(describeUserAgent(safariIPhone)).toBe('Safari on iOS');
		expect(describeUserAgent(firefoxWindows)).toBe('Firefox on Windows');
		expect(describeUserAgent(edgeMac)).toBe('Edge on macOS');
		expect(describeUserAgent('curl/8.5.0')).toBe('curl');
		expect(describeUserAgent('')).toBe('Unknown browser');
	});
});

describe('formatBitrate', () => {
	it('uses decimal bit units with three significant digits', () => {
		expect(formatBitrate(0)).toBe('0 bps');
		expect(formatBitrate(272)).toBe('272 bps');
		expect(formatBitrate(999)).toBe('999 bps');
		expect(formatBitrate(1000)).toBe('1 Kbps');
		expect(formatBitrate(62390)).toBe('62.4 Kbps');
		expect(formatBitrate(156100)).toBe('156 Kbps');
		expect(formatBitrate(1_200_000)).toBe('1.2 Mbps');
		expect(formatBitrate(3_710_000)).toBe('3.71 Mbps');
		expect(formatBitrate(4_800_000_000)).toBe('4.8 Gbps');
		expect(formatBitrate(2.5e12)).toBe('2.5 Tbps');
		expect(formatBitrate(5e15)).toBe('5000 Tbps');
	});
});

describe('formatChartTime', () => {
	it('shows the date and the time, with seconds only when asked', () => {
		const sec = Date.parse('2026-09-30T18:24:05Z') / 1000;
		const plain = formatChartTime(sec);
		const withSeconds = formatChartTime(sec, true);
		expect(plain).toMatch(/Sep|30/);
		// The same minute, and the seconds are the only difference.
		expect(withSeconds.length).toBeGreaterThan(plain.length);
		expect(withSeconds).toMatch(/:05\b/);
		expect(plain).not.toMatch(/:05\b/);
	});
});
