import { expect, test } from '@playwright/test';
import { cli, admin, login, navigate, watchConsole } from './helpers';

// The tests share one daemon with app.spec.ts (playwright.config.ts: workers: 1), and run after
// its setup test, so the admin account already exists.
test.describe.configure({ mode: 'serial' });

test('make an API token, use it from somewhere that has no login, and revoke it', async ({
	page,
	playwright,
	baseURL
}) => {
	const problems = watchConsole(page);
	cli('client', 'add', 'Token Probe');
	try {
		await login(page);
		const clients: { id: string; name: string }[] = await (
			await page.request.get('/api/clients')
		).json();
		const probe = clients.find((c) => c.name === 'Token Probe')!;

		await page.goto('/account');
		const section = page.getByRole('region', { name: 'API Tokens' });
		await expect(section).toContainText('No tokens yet.');

		// It takes the password again, and a wrong one makes nothing.
		await section.getByLabel('Name', { exact: true }).fill('Homepage');
		await section.getByLabel('Your password').fill('not the password');
		await section.getByRole('button', { name: 'Make Token' }).click();
		await expect(section.getByRole('alert')).toContainText('current password is wrong');
		await expect(section).toContainText('No tokens yet.');
		await expect(section.getByTestId('new-token')).toHaveCount(0);

		// The right one shows the secret once, with the widget for Homepage.
		await section.getByLabel('Your password').fill(admin.password);
		await section.getByRole('button', { name: 'Make Token' }).click();
		const box = section.getByTestId('new-token');
		await expect(box).toContainText("It won't be shown again");
		const secret = (await box.getByTestId('new-token-secret').innerText()).trim();
		expect(secret).toMatch(/^dbt_[A-Za-z0-9_-]{43}$/);
		await box.getByText('For Homepage').click();
		await expect(box).toContainText(`url: ${baseURL}/api/server/status`);
		await expect(box).toContainText(`Authorization: Bearer ${secret}`);
		const row = section.getByRole('listitem');
		await expect(row).toHaveCount(1);
		await expect(row).toContainText('Homepage');
		await expect(row).toContainText(`${secret.slice(0, 8)}…`);
		await expect(row).toContainText('never used');
		await expect(section.getByLabel('Your password')).toHaveValue('');

		// A client with no cookies, only the token, reads what a dashboard shows.
		const dashboard = await playwright.request.newContext({
			baseURL,
			ignoreHTTPSErrors: true,
			extraHTTPHeaders: { Authorization: `Bearer ${secret}` }
		});
		const status = await dashboard.get('/api/server/status');
		expect(status.status()).toBe(200);
		expect(await status.json()).toMatchObject({ tunnel_up: true });
		expect((await dashboard.get('/api/clients')).status()).toBe(200);
		expect((await dashboard.get(`/api/clients/${probe.id}/traffic`)).status()).toBe(200);

		// And nothing more: not the private key, not what it browsed, not the log, not a change,
		// not another token.
		for (const path of [
			`/api/clients/${probe.id}/config`,
			`/api/clients/${probe.id}/dns-log`,
			'/api/events',
			'/api/server',
			'/api/auth/tokens',
			'/api/auth/me'
		]) {
			expect((await dashboard.get(path)).status(), path).toBe(403);
		}
		const headers = { 'X-Drawbridge': '1' };
		const made = await dashboard.post('/api/clients', { headers, data: { name: 'Sneaky' } });
		expect(made.status()).toBe(403);
		const minted = await dashboard.post('/api/auth/tokens', {
			headers,
			data: { name: 'Another', password: admin.password }
		});
		expect(minted.status()).toBe(403);
		const wrong = await playwright.request.newContext({
			baseURL,
			ignoreHTTPSErrors: true,
			extraHTTPHeaders: {
				Authorization: `Bearer ${secret.slice(0, -1)}${secret.endsWith('A') ? 'B' : 'A'}`
			}
		});
		expect((await wrong.get('/api/server/status')).status()).toBe(401);

		// The page doesn't keep the secret: after a reload only the prefix is left, and the token
		// shows it has been used.
		await page.reload();
		expect(await page.content()).not.toContain(secret);
		await expect(row).toContainText('last used');
		await expect(row).not.toContainText('never used');
		const listed = await (await page.request.get('/api/auth/tokens')).text();
		expect(listed).not.toContain(secret);

		// Revoking asks first, and then the token stops at once.
		await row.getByRole('button', { name: 'Revoke Homepage' }).click();
		await expect(section).toContainText('Anything using it stops working.');
		await section.getByRole('button', { name: 'Cancel' }).click();
		expect((await dashboard.get('/api/server/status')).status()).toBe(200);
		await row.getByRole('button', { name: 'Revoke Homepage' }).click();
		await section.getByRole('button', { name: 'Revoke', exact: true }).click();
		await expect(section).toContainText('No tokens yet.');
		expect((await dashboard.get('/api/server/status')).status()).toBe(401);

		// The log says what happened, and never the secret.
		await navigate(page, 'Logs');
		const rows = page.locator('tbody tr');
		await expect(rows.filter({ hasText: 'Made an API token' })).toHaveCount(1);
		await expect(rows.filter({ hasText: 'Revoked an API token' })).toHaveCount(1);
		await expect(
			rows.filter({ hasText: 'Failed to make an API token (wrong password)' })
		).toHaveCount(1);
		await expect(rows.filter({ hasText: 'Homepage' }).first()).toBeVisible();
		expect(await page.locator('tbody').innerText()).not.toContain(secret);
		expect(problems).toEqual([]);
		await dashboard.dispose();
		await wrong.dispose();
	} finally {
		cli('client', 'delete', '--yes', 'Token Probe');
	}
});
