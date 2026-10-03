import { describe, expect, it } from 'vitest';
import { homepageWidget } from './tokens';

describe('homepageWidget', () => {
	it('reads the status from the address the browser is on, with the token as a bearer token', () => {
		const yaml = homepageWidget('https://pi.example.com:51821', 'dbt_secret');
		expect(yaml).toContain('url: https://pi.example.com:51821/api/server/status');
		expect(yaml).toContain('    Authorization: Bearer dbt_secret\n');
		expect(yaml.startsWith('widget:\n  type: customapi\n')).toBe(true);
	});

	it('maps the fields the status has', () => {
		const yaml = homepageWidget('https://pi.example.com:51821', 'dbt_secret');
		for (const field of ['online', 'clients', 'paused']) {
			expect(yaml).toContain(`- field: ${field}\n`);
		}
	});
});
