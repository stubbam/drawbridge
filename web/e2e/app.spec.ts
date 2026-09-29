import { expect, test, type Locator } from '@playwright/test';
import { readFileSync } from 'node:fs';
import {
	admin,
	cli,
	login,
	logOut,
	navigate,
	openUserMenu,
	setupToken,
	watchConsole
} from './helpers';

// The tests share one daemon and run in order: setup comes first.
test.describe.configure({ mode: 'serial' });

/** Where an element is on the page. */
const box = async (l: Locator) => (await l.boundingBox())!;

test('first-run setup creates the admin account, sets the endpoint, and picks the DNS', async ({
	page
}) => {
	const problems = watchConsole(page);
	await page.goto('/');
	await expect(page).toHaveURL(/\/setup$/);

	await page.getByLabel('Setup token').fill('WRONG-TOKEN');
	await page.getByLabel('Password', { exact: true }).fill(admin.password);
	await page.getByLabel('Password again').fill(admin.password);
	await page.getByRole('button', { name: 'Create Account' }).click();
	await expect(page.getByRole('alert')).toHaveText('the setup token is wrong');

	// People copy the token in whatever case.
	await page.getByLabel('Setup token').fill(setupToken().toLowerCase());
	await page.getByRole('button', { name: 'Create Account' }).click();
	await page.getByLabel('Public address').fill('vpn.example.com');
	await page.getByRole('button', { name: 'Continue' }).click();

	// The fake backend has a resolver on the IPv4 address only, so the check finds that one,
	// picks "This server", and says the IPv6 address has no answer.
	await expect(page.getByRole('heading', { name: 'DNS for Clients' })).toBeVisible();
	const check = page.getByTestId('dns-check');
	await expect(check.getByText('10.8.0.1: answers')).toBeVisible();
	await expect(check.getByText(/fd[0-9a-f:]+: no answer/)).toBeVisible();
	await expect(page.getByRole('radio', { name: /This server/ })).toBeChecked();
	await page.getByRole('button', { name: 'Finish' }).click();

	await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();
	await expect(page.getByText('vpn.example.com:51820')).toBeVisible();
	// Only the address that answered reaches the clients.
	expect(cli('server', 'show')).toMatch(/^DNS:\s+10\.8\.0\.1$/m);
	await navigate(page, 'Logs');
	await expect(page.getByText('Completed setup')).toBeVisible();
	expect(problems).toEqual([]);
});

test('clients: add, QR code, download, pause, rename, delete', async ({ page }) => {
	const problems = watchConsole(page);
	await login(page);
	// The "+ Add Client" button works from any page, including the dashboard.
	await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();

	await page.getByRole('button', { name: 'Add Client' }).click();
	await page.getByLabel('Name').fill("Alex's iPhone");
	await page.getByRole('button', { name: 'Add', exact: true }).click();
	// A new client's page shows its QR code at once.
	await expect(page.getByRole('heading', { name: "Alex's iPhone" })).toBeVisible();
	const qr = page.getByRole('img', { name: /QR code/ });
	await expect(qr).toBeVisible();
	expect(await qr.getAttribute('src')).toMatch(/^data:image\/png;base64,/);

	const downloading = page.waitForEvent('download');
	await page.getByRole('button', { name: 'Download .conf' }).click();
	const download = await downloading;
	expect(download.suggestedFilename()).toBe('Alex-s-iPhone.conf');
	const conf = readFileSync((await download.path())!, 'utf8');
	expect(conf).toContain('[Interface]');
	expect(conf).toContain('Endpoint = vpn.example.com:51820');
	expect(conf).toMatch(/^DNS = 10\.8\.0\.1$/m);

	await page.getByRole('button', { name: 'Pause' }).click();
	await expect(page.getByText('Paused. The client is out of the tunnel.')).toBeVisible();
	await page.getByRole('button', { name: 'Resume' }).click();
	await expect(page.getByRole('status')).toHaveText(/^Resumed. The client reconnects/);

	await page.getByLabel('Rename').fill('Pixel');
	await page.getByRole('button', { name: 'Rename', exact: true }).click();
	await expect(page.getByRole('heading', { name: 'Pixel' })).toBeVisible();

	// The CLI sees what the web did, and the log says who did it.
	expect(cli('client', 'list')).toContain('Pixel');
	await expect(page.getByText('Viewed the config').first()).toBeVisible();

	await page.getByRole('button', { name: 'Delete this client…' }).click();
	await page.getByRole('button', { name: 'Delete', exact: true }).click();
	await expect(page).toHaveURL(/\/clients$/);
	await expect(page.getByText('No clients yet')).toBeVisible();
	expect(problems).toEqual([]);
});

test('the client list pauses and resumes, and finds clients', async ({ page }) => {
	cli('client', 'add', 'laptop');
	cli('client', 'add', 'tablet');
	await login(page);
	await navigate(page, 'Clients');

	const laptop = page.getByRole('listitem').filter({ hasText: 'laptop' });
	await laptop.getByRole('button', { name: 'Pause' }).click();
	await expect(laptop.getByText('Paused', { exact: true })).toBeVisible();
	await expect(laptop.getByRole('button', { name: 'Resume' })).toBeVisible();
	expect(cli('client', 'show', 'laptop')).toMatch(/State:\s+paused/);

	await page.getByLabel('Search clients').fill('tab');
	await expect(page.getByText('1 of 2')).toBeVisible();
	await expect(page.getByRole('link', { name: 'laptop' })).toBeHidden();
});

test('the dashboard lists clients and links its tiles to a filtered list', async ({ page }) => {
	// laptop is paused and tablet is enabled but never connected, from the previous test.
	await login(page);
	await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();

	const dashboardList = page.getByRole('list').filter({ hasText: 'laptop' });
	await expect(dashboardList.getByText('Not connected')).toHaveCount(2);
	await expect(page.getByText('Total, all clients: ↓ 0 B · ↑ 0 B')).toBeVisible();

	// Every row has the same shape. A never-connected client's short total once fit on the
	// first line, next to a badge that overflowed its box; it belongs on a line of its own,
	// and the badges line up in one column.
	const tablet = dashboardList.getByRole('listitem').filter({ hasText: 'tablet' });
	const laptop = dashboardList.getByRole('listitem').filter({ hasText: 'laptop' });
	const name = await box(tablet.getByRole('link', { name: 'tablet' }));
	const session = await box(tablet.getByText('Not connected'));
	const badge = await box(tablet.getByText('Never Connected', { exact: true }));
	const total = await box(tablet.getByText(/^Total:/));
	expect(total.y).toBeGreaterThanOrEqual(name.y + name.height);
	expect(total.y).toBeGreaterThanOrEqual(session.y + session.height);
	expect(session.x).toBeGreaterThanOrEqual(badge.x + badge.width);
	expect((await box(laptop.getByText('Paused', { exact: true }))).x).toBe(badge.x);

	await page.getByRole('button', { name: 'View paused clients' }).click();
	await expect(page).toHaveURL(/\/clients\?state=paused$/);
	const chip = page.getByRole('button', { name: 'Clear the paused filter' });
	await expect(chip).toBeVisible();
	await expect(page.getByRole('link', { name: 'laptop' })).toBeVisible();
	await expect(page.getByRole('link', { name: 'tablet' })).toBeHidden();

	await chip.click();
	await expect(page.getByRole('link', { name: 'tablet' })).toBeVisible();

	await page.goto('/');
	await page.getByRole('button', { name: 'View online clients' }).click();
	await expect(page).toHaveURL(/\/clients\?state=online$/);
	await expect(page.getByText('No client is online.')).toBeVisible();
});

test('the dashboard and the client list sort, and remember the order', async ({ page }) => {
	// laptop (10.8.0.2) is paused and tablet (10.8.0.3) never connected, from earlier tests.
	cli('client', 'add', 'desk'); // 10.8.0.4, never connected
	await login(page);
	const names = page.getByRole('main').getByRole('listitem').getByRole('link');

	// The dashboard offers name (the default) and status: online, idle, never connected,
	// then paused.
	await expect(names).toHaveText(['desk', 'laptop', 'tablet']);
	await page.getByLabel('Sort by').selectOption('status');
	await expect(names).toHaveText(['desk', 'tablet', 'laptop']);
	await page.reload();
	await expect(page.getByLabel('Sort by')).toHaveValue('status');
	await expect(names).toHaveText(['desk', 'tablet', 'laptop']);

	// The Clients page offers more, and remembers its own choice.
	await navigate(page, 'Clients');
	await expect(names).toHaveText(['desk', 'laptop', 'tablet']);
	await page.getByLabel('Sort by').selectOption('ip');
	await expect(names).toHaveText(['laptop', 'tablet', 'desk']);
	await page.getByLabel('Sort by').selectOption('status');
	await expect(names).toHaveText(['desk', 'tablet', 'laptop']);
	await page.getByLabel('Sort by').selectOption('ip');
	await page.reload();
	await expect(names).toHaveText(['laptop', 'tablet', 'desk']);

	// Sorting works on the filtered list too.
	await page.getByLabel('Search clients').fill('t');
	await expect(names).toHaveText(['laptop', 'tablet']);
});

test("the dashboard shows a connected client's endpoint under its name", async ({ page }) => {
	// The fake backend never reports a handshake, so this test supplies a connected client.
	const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
	const phone = {
		id: 'c9',
		name: 'phone',
		enabled: true,
		ipv4: '10.8.0.9',
		ipv6: 'fd00::9',
		public_key: 'k',
		created_at: ago(86400e3),
		peer: {
			endpoint: '[2001:db8::7]:51820',
			last_handshake: ago(10e3),
			receive_bytes: 2e9,
			send_bytes: 1e9,
			session_started_at: ago(60e3),
			session_receive_bytes: 2e6,
			session_send_bytes: 1e6
		}
	};
	await page.route('**/api/clients', (route) => route.fulfill({ json: [phone] }));
	await login(page);

	// The endpoint's address (without the port) is under the name, and the total is on that
	// same line, under the session, on the right.
	const row = page.getByRole('main').getByRole('listitem');
	const name = await box(row.getByRole('link', { name: 'phone' }));
	const endpoint = await box(row.getByText('2001:db8::7', { exact: true }));
	const session = await box(row.getByText(/^This session:/));
	const total = row.getByText(/^Total:/);
	expect(endpoint.x).toBe(name.x);
	expect(endpoint.y).toBeGreaterThanOrEqual(name.y + name.height);
	expect((await box(total)).y).toBe(endpoint.y);
	expect((await box(total)).x).toBe(session.x);
	await expect(total).toHaveCSS('text-align', 'right');
});

test('the mode toggle cycles light, dark, and auto, which follows the browser', async ({
	page
}) => {
	await page.emulateMedia({ colorScheme: 'dark' });
	await login(page);
	const isDark = () => page.locator('html').evaluate((el) => el.classList.contains('dark'));
	const toggle = page.getByRole('button', { name: /^Switch Mode/ });

	// A first visit is in auto: it follows the browser, including a change while it's open.
	await expect(toggle).toHaveAccessibleName('Switch Mode (Auto)');
	expect(await isDark()).toBe(true);
	await page.emulateMedia({ colorScheme: 'light' });
	await expect.poll(isDark).toBe(false);

	// Light and dark ignore the browser, and a reload keeps them: app.html's script applies
	// the stored mode before the app starts.
	await toggle.click();
	await expect(toggle).toHaveAccessibleName('Switch Mode (Light)');
	await page.emulateMedia({ colorScheme: 'dark' });
	await page.reload();
	await expect(toggle).toHaveAccessibleName('Switch Mode (Light)');
	expect(await isDark()).toBe(false);

	await toggle.click();
	await expect(toggle).toHaveAccessibleName('Switch Mode (Dark)');
	await page.emulateMedia({ colorScheme: 'light' });
	await page.reload();
	await expect(toggle).toHaveAccessibleName('Switch Mode (Dark)');
	expect(await isDark()).toBe(true);

	// Back to auto, which follows the browser again, after a reload too.
	await toggle.click();
	await expect(toggle).toHaveAccessibleName('Switch Mode (Auto)');
	expect(await isDark()).toBe(false);
	await page.reload();
	await expect(toggle).toHaveAccessibleName('Switch Mode (Auto)');
	await page.emulateMedia({ colorScheme: 'dark' });
	await expect.poll(isDark).toBe(true);
});

test('settings change the server, and the logs show it', async ({ page }) => {
	const problems = watchConsole(page);
	await login(page);
	await navigate(page, 'Settings');

	await page.getByLabel('MTU').fill('1380');
	await page.getByLabel('Other servers').check();
	await page.getByLabel('Addresses', { exact: true }).fill('1.1.1.1, 2606:4700:4700::1111');
	await page.getByRole('button', { name: 'Save settings' }).click();
	await expect(page.getByText(/Saved and applied/)).toBeVisible();
	expect(cli('server', 'show')).toMatch(/MTU:\s+1380/);

	// The form refuses an out-of-range MTU before it reaches the server (which checks too).
	await page.getByLabel('MTU').fill('900');
	await page.getByRole('button', { name: 'Save settings' }).click();
	expect(await page.getByLabel('MTU').evaluate((el: HTMLInputElement) => el.validity.valid)).toBe(
		false
	);
	expect(cli('server', 'show')).toMatch(/MTU:\s+1380/);

	await navigate(page, 'Logs');
	await expect(page.getByRole('cell', { name: 'mtu: 1420 → 1380' }).first()).toBeVisible();
	await page.getByLabel('Show').selectOption('system');
	await expect(page.getByText('No events.')).toBeVisible();
	expect(problems).toEqual([]);
});

test('logging out, a wrong password, and returning to the page', async ({ page }) => {
	await login(page);
	await logOut(page);
	await expect(page).toHaveURL(/\/login$/);

	await page.goto('/logs');
	await expect(page).toHaveURL(/\/login\?next=%2Flogs$/);
	await page.getByLabel('Username').fill(admin.username);
	await page.getByLabel('Password').fill('not the password');
	await page.getByRole('button', { name: 'Log In' }).click();
	await expect(page.getByRole('alert')).toHaveText('wrong username or password');

	await page.getByLabel('Password').fill(admin.password);
	await page.getByRole('button', { name: 'Log In' }).click();
	await expect(page).toHaveURL(/\/logs$/);
	await expect(page.getByText('Failed login')).toBeVisible();

	// next= never leaves the site.
	await logOut(page);
	await page.goto('/login?next=' + encodeURIComponent('//evil.example/x'));
	await page.getByLabel('Username').fill(admin.username);
	await page.getByLabel('Password').fill(admin.password);
	await page.getByRole('button', { name: 'Log In' }).click();
	await expect(page).toHaveURL(/127\.0\.0\.1:\d+\/$/);
});

test('the account page changes the password and lists sessions', async ({ page }) => {
	await login(page);
	// Account isn't in the main nav; it's reached through the user menu.
	let menu = await openUserMenu(page);
	await expect(menu.getByText(admin.username, { exact: true })).toBeVisible();

	// Clicking outside the menu closes it without navigating anywhere.
	await page.getByRole('heading', { name: 'Dashboard' }).click();
	await expect(menu).toBeHidden();

	menu = await openUserMenu(page);
	await menu.getByRole('link', { name: 'Settings' }).click();
	await expect(page).toHaveURL(/\/account$/);
	await expect(page.getByText('(this one)')).toBeVisible();

	await page.getByLabel('Current password').fill(admin.password);
	await page.getByLabel('New password', { exact: true }).fill('another long password');
	await page.getByLabel('New password again').fill('another long password');
	await page.getByRole('button', { name: 'Change Password' }).click();
	await expect(page.getByText(/Password changed/)).toBeVisible();

	// Put it back, for the other tests.
	await page.getByLabel('Current password').fill('another long password');
	await page.getByLabel('New password', { exact: true }).fill(admin.password);
	await page.getByLabel('New password again').fill(admin.password);
	await page.getByRole('button', { name: 'Change Password' }).click();
	await expect(page.getByText(/Password changed/)).toBeVisible();
});
