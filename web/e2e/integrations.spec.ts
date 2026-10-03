import { expect, test } from '@playwright/test';
import { cli, login, navigate, watchConsole } from './helpers';
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
		const address = form.getByLabel('Address', { exact: true });
		const username = form.getByLabel('Username', { exact: true });
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
		const saved = await api.json();
		expect(saved).toMatchObject({
			configured: true,
			base_url: `${adguard.url}/control`,
			username: 'drawbridge',
			has_password: true,
			enabled: false
		});
		expect(JSON.stringify(saved)).not.toContain(password);

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

test('turning AdGuard Home on names the clients, and the names follow the clients', async ({
	page
}) => {
	const problems = watchConsole(page);
	const adguard = await startFakeAdGuard('drawbridge', password);
	cli('client', 'add', 'Sync Probe');
	cli('client', 'add', 'Taken');
	// A client of the admin's, with the name the second one will want.
	adguard.addClient('Taken', ['192.0.2.77']);
	try {
		await login(page);
		await navigate(page, 'Settings');
		const form = page.getByRole('form', { name: 'AdGuard Home' });
		const sync = form.getByTestId('adguard-sync');
		await form.getByLabel('Address', { exact: true }).fill(adguard.url);
		await form.getByLabel('Username', { exact: true }).fill('drawbridge');
		await form.getByLabel('Password', { exact: true }).fill(password);

		// Off until it's turned on: the name switch waits for the other, and nothing is sent.
		const names = form.getByLabel('Name clients in AdGuard Home');
		await expect(names).toBeDisabled();
		await form.getByLabel('Use AdGuard Home').check();
		await expect(names).toBeEnabled();
		await expect(names).toBeChecked();
		await expect(sync).toHaveCount(0);
		expect(adguard.clients()).toEqual([expect.objectContaining({ name: 'Taken' })]);

		// Saving turns it on and names the client. The other is the admin's, and is left alone.
		await form.getByRole('button', { name: 'Save connection' }).click();
		await expect(sync).toContainText(/\d+\s+clients?\s+(has|have)\s+their name in AdGuard Home/);
		await expect(sync).toContainText("One client couldn't be named");
		await expect(sync.getByRole('listitem')).toContainText('Taken');
		await expect(sync.getByRole('listitem')).toContainText("didn't make");
		const probe = adguard.clients().find((c) => c.name === 'Sync Probe');
		expect(probe?.ids).toHaveLength(2); // its IPv4 and IPv6 addresses
		expect(probe?.use_global_settings).toBe(true); // or AdGuard Home wouldn't block ads for it
		expect(adguard.clients().find((c) => c.name === 'Taken')?.ids).toEqual(['192.0.2.77']);

		// The admin settles it in AdGuard Home, and Sync now names the client.
		adguard.removeClient('Taken');
		await form.getByRole('button', { name: 'Sync now' }).click();
		await expect(sync).not.toContainText("couldn't be named");
		await expect(sync.getByRole('listitem')).toHaveCount(0);
		expect(adguard.clients().map((c) => c.name)).toContain('Taken');

		// Changes in Drawbridge follow on their own, from the daemon's loop, with no page open.
		cli('client', 'rename', 'Sync Probe', 'Renamed Probe');
		await expect
			.poll(
				() =>
					adguard
						.clients()
						.map((c) => c.name)
						.sort(),
				{ timeout: 15_000 }
			)
			.toContain('Renamed Probe');
		expect(adguard.clients().map((c) => c.name)).not.toContain('Sync Probe');
		cli('client', 'delete', '--yes', 'Renamed Probe');
		cli('client', 'delete', '--yes', 'Taken');
		await expect
			.poll(() => adguard.clients().map((c) => c.name), { timeout: 15_000 })
			.not.toContain('Renamed Probe');
		await expect
			.poll(() => adguard.clients().map((c) => c.name), { timeout: 15_000 })
			.not.toContain('Taken');

		// When the sync can't reach AdGuard Home, Settings says so, and so does the dashboard,
		// which the admin looks at more often. It goes when AdGuard Home is back.
		adguard.setDown(true);
		await form.getByRole('button', { name: 'Sync now' }).click();
		await expect(sync).toContainText("Couldn't sync: can't reach AdGuard Home");
		await page.getByRole('link', { name: 'Drawbridge' }).click();
		const warning = page.getByTestId('adguard-warning');
		await expect(warning).toContainText("Drawbridge can't sync client names to AdGuard Home");
		await warning.getByRole('link', { name: 'See Settings' }).click();
		await expect(page).toHaveURL(/\/settings$/);
		adguard.setDown(false);
		await form.getByRole('button', { name: 'Sync now' }).click();
		await expect(sync).not.toContainText("Couldn't sync");
		await page.getByRole('link', { name: 'Drawbridge' }).click();
		await expect(warning).toHaveCount(0);

		// The log says what was done to AdGuard Home, as the daemon's work.
		await navigate(page, 'Logs');
		const rows = page.locator('tbody tr');
		const logged = (text: string) => rows.filter({ hasText: text });
		await expect(logged('Named a client in AdGuard Home: Sync Probe')).toHaveCount(1);
		await expect(logged('Renamed a client in AdGuard Home: Renamed Probe')).toHaveCount(1);
		await expect(logged('Removed a client from AdGuard Home: Renamed Probe')).toHaveCount(1);
		await expect(logged('Removed a client from AdGuard Home: Taken')).toHaveCount(1);
		await expect(logged("Couldn't name a client in AdGuard Home: Taken")).toHaveCount(1);

		// Clean up: the next tests share this daemon.
		await navigate(page, 'Settings');
		await form.getByRole('button', { name: 'Remove…' }).click();
		await form.getByRole('button', { name: 'Remove', exact: true }).click();
		await expect(form.getByText('Removed.')).toBeVisible();
		expect(problems).toEqual([]);
	} finally {
		await adguard.close();
	}
});
