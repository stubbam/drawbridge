import { expect, test } from '@playwright/test';
import { login, navigate, watchConsole } from './helpers';
import { startFakeAdGuard } from './fake-adguard';

// The tests share one daemon with app.spec.ts (playwright.config.ts: workers: 1), and run
// after its setup test, so the admin account already exists.
test.describe.configure({ mode: 'serial' });

const password = 'p4ss-for-the-test';

test('connect to AdGuard Home: test it, save it, change it, and remove it', async ({ page }) => {
	const problems = watchConsole(page);
	const adguard = await startFakeAdGuard('drawbridge', password);
	try {
		await login(page);
		await navigate(page, 'Settings');
		const form = page.getByRole('form', { name: 'AdGuard Home' });
		const address = form.getByLabel('Address');
		const username = form.getByLabel('Username');
		const pass = form.getByLabel('Password', { exact: true });
		const result = form.getByTestId('adguard-test');

		// Nothing is saved, and the usual address of a local AdGuard Home is there to start from.
		await expect(address).toHaveValue('http://127.0.0.1:3000/control');
		await expect(username).toHaveValue('');
		await expect(form.getByRole('button', { name: 'Remove…' })).toHaveCount(0);

		// A wrong password is an answer. A second click doesn't ask AdGuard Home again, because it
		// blocks an address after a few refusals.
		await address.fill(adguard.url);
		await username.fill('drawbridge');
		await pass.fill('not the password');
		await form.getByRole('button', { name: 'Test connection' }).click();
		await expect(result).toContainText('refused the account');
		await form.getByRole('button', { name: 'Test connection' }).click();
		await expect(result).toContainText('a moment ago');
		expect(adguard.refused()).toBe(1);

		// The right one: what AdGuard Home said, its warnings, and what the VPN addresses answer.
		await pass.fill(password);
		await expect(result).toHaveCount(0); // a result goes when the values it ran on change
		await form.getByRole('button', { name: 'Test connection' }).click();
		await expect(result).toContainText('Connected to AdGuard Home v0.107.79');
		await expect(result).toContainText('DNS server running');
		await expect(result).toContainText('Protection on');
		await expect(result).toContainText("on, but it hides each client's address");
		await expect(
			result.getByRole('status').filter({ hasText: 'hides the end of each client' })
		).toBeVisible();
		await expect(result.getByRole('listitem').first()).toContainText('answers.');
		await expect(result.getByRole('listitem').last()).toContainText('no answer.');

		// Save it. The password goes to the server and never comes back, to the page or the API.
		await form.getByRole('button', { name: 'Save connection' }).click();
		await expect(form.getByText('Saved.', { exact: true })).toBeVisible();
		await page.reload();
		await expect(address).toHaveValue(`${adguard.url}/control`);
		await expect(username).toHaveValue('drawbridge');
		await expect(pass).toHaveValue('');
		await expect(pass).toHaveAttribute('placeholder', 'Saved');
		expect(await page.content()).not.toContain(password);
		const api = await page.request.get('/api/integrations/adguard');
		expect(await api.json()).toEqual({
			configured: true,
			base_url: `${adguard.url}/control`,
			username: 'drawbridge',
			has_password: true
		});

		// The saved password is used to test the saved connection, with nothing retyped.
		await form.getByRole('button', { name: 'Test connection' }).click();
		await expect(result).toContainText('Connected to AdGuard Home v0.107.79');
		expect(adguard.accepted.at(-1)).toBe('GET /control/querylog/config');

		// Another address takes the password again: the saved one goes only where it was saved to.
		await address.fill('http://127.0.0.1:1');
		await expect(
			form.getByText('Enter it again to use another address or username.')
		).toBeVisible();
		await form.getByRole('button', { name: 'Save connection' }).click();
		await expect(form.getByRole('alert')).toContainText('enter the password again');
		await form.getByRole('button', { name: 'Test connection' }).click();
		await expect(form.getByRole('alert')).toContainText('enter the password again');

		// An address that can't be reached is a result of the test.
		await pass.fill(password);
		await form.getByRole('button', { name: 'Test connection' }).click();
		await expect(result).toContainText("can't reach AdGuard Home");
		await page.reload();

		// The log says the connection changed, and never what the password is.
		await navigate(page, 'Logs');
		const row = page.locator('tbody tr').filter({ hasText: 'Changed the AdGuard Home connection' });
		await expect(row).toHaveCount(1);
		await expect(row).toContainText('password: set');
		expect(await page.locator('tbody').innerText()).not.toContain(password);

		// Remove it: the address and the password are forgotten.
		await navigate(page, 'Settings');
		await form.getByRole('button', { name: 'Remove…' }).click();
		await expect(form.getByText('Forget the address and the password?')).toBeVisible();
		await form.getByRole('button', { name: 'Remove', exact: true }).click();
		await expect(form.getByText('Removed.')).toBeVisible();
		await expect(address).toHaveValue('http://127.0.0.1:3000/control');
		await expect(username).toHaveValue('');
		await expect(pass).toHaveAttribute('placeholder', '');
		await expect(form.getByRole('button', { name: 'Remove…' })).toHaveCount(0);
		expect((await (await page.request.get('/api/integrations/adguard')).json()).configured).toBe(
			false
		);
		expect(problems).toEqual([]);
	} finally {
		await adguard.close();
	}
});
