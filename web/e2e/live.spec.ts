import { expect, test, type Page } from '@playwright/test';
import { cli, login, navigate, watchConsole } from './helpers';

// The tests share one daemon with app.spec.ts (playwright.config.ts: workers: 1), and run
// after its setup test, so the admin account already exists.
test.describe.configure({ mode: 'serial' });

/** The requests the page makes for what the live feed also carries, to see that it doesn't. */
function countStatusRequests(page: Page): string[] {
	const seen: string[] = [];
	page.on('request', (r) => {
		const path = new URL(r.url()).pathname;
		if (
			path === '/api/clients' ||
			path === '/api/server/status' ||
			/^\/api\/clients\/[^/]+$/.test(path)
		)
			seen.push(`${r.method()} ${path}`);
	});
	return seen;
}

test('the dashboard follows changes made elsewhere, and does not poll for them', async ({
	page
}) => {
	const problems = watchConsole(page);
	const asked = countStatusRequests(page);
	await login(page);
	const clients = page.getByRole('button', { name: 'View all clients' });
	const before = Number((await clients.innerText()).match(/\d+/)![0]);

	// Wait out a polling interval, and then some: the feed is open, so nothing asks.
	await page.waitForTimeout(6500);
	const initial = asked.length;
	expect(initial).toBeLessThanOrEqual(2);

	// The CLI adds a client, and the count moves at once, which a 5 second poll couldn't promise.
	cli('client', 'add', 'Live Probe');
	await expect(clients).toContainText(String(before + 1), { timeout: 2500 });
	await expect(page.getByRole('link', { name: 'Live Probe' })).toBeVisible({ timeout: 2500 });
	cli('client', 'pause', 'Live Probe');
	await expect(page.getByRole('button', { name: 'View paused clients' })).toContainText('1', {
		timeout: 2500
	});
	cli('client', 'delete', '--yes', 'Live Probe');
	await expect(clients).toContainText(String(before), { timeout: 2500 });
	await expect(page.getByRole('link', { name: 'Live Probe' })).toHaveCount(0, { timeout: 2500 });

	expect(asked.slice(initial)).toEqual([]);
	expect(problems).toEqual([]);
});

test('the client list and a client page follow changes made elsewhere', async ({ page }) => {
	const problems = watchConsole(page);
	cli('client', 'add', 'Page Probe');
	await login(page);
	await navigate(page, 'Clients');
	await expect(page.getByRole('link', { name: 'Page Probe' })).toBeVisible();
	const asked = countStatusRequests(page);

	await page.getByRole('link', { name: 'Page Probe' }).click();
	await expect(page.getByRole('heading', { name: 'Page Probe', level: 1 })).toBeVisible();
	await expect(page.getByRole('button', { name: 'Pause', exact: true })).toBeVisible();
	await page.waitForTimeout(6500);
	const initial = asked.length;

	// Paused elsewhere: the button turns into Resume, and the new event is in its list.
	cli('client', 'pause', 'Page Probe');
	await expect(page.getByRole('button', { name: 'Resume', exact: true })).toBeVisible({
		timeout: 2500
	});
	await expect(
		page.getByRole('region', { name: 'Activity' }).getByText('Paused a client').first()
	).toBeVisible({ timeout: 2500 });

	// Renamed elsewhere: the heading follows.
	cli('client', 'rename', 'Page Probe', 'Renamed Probe');
	await expect(page.getByRole('heading', { name: 'Renamed Probe', level: 1 })).toBeVisible({
		timeout: 2500
	});

	// Deleted elsewhere: the page says so.
	cli('client', 'delete', '--yes', 'Renamed Probe');
	await expect(page.getByText(/no such client|not found|doesn't exist/i).first()).toBeVisible({
		timeout: 8000
	});

	expect(
		asked.filter((r) => r.startsWith('GET /api/clients/')).length - initial
	).toBeLessThanOrEqual(3);
	expect(problems).toEqual([]);
});

test('the log shows new events as they happen, if the filters allow them', async ({ page }) => {
	const problems = watchConsole(page);
	await login(page);
	await navigate(page, 'Logs');
	const rows = page.locator('tbody tr');
	await expect(rows.first()).toBeVisible();

	cli('client', 'add', 'Tail Probe');
	await expect(rows.first()).toContainText('Added a client: Tail Probe', { timeout: 2500 });

	// With a filter, only what it shows: a pause is listed, an addition isn't.
	await page.getByLabel('Event', { exact: true }).selectOption({ label: 'Paused a client' });
	await expect(rows.first()).toBeVisible();
	const shown = await rows.count();
	cli('client', 'add', 'Tail Probe Two');
	cli('client', 'pause', 'Tail Probe');
	await expect(rows.first()).toContainText('Paused a client: Tail Probe', { timeout: 2500 });
	await expect(rows).toHaveCount(shown + 1);
	await expect(rows.filter({ hasText: 'Tail Probe Two' })).toHaveCount(0);

	cli('client', 'delete', '--yes', 'Tail Probe');
	cli('client', 'delete', '--yes', 'Tail Probe Two');
	expect(problems).toEqual([]);
});

test('the pages poll as they always did when the feed cannot be had', async ({ page }) => {
	// The stream is refused, as a proxy that holds responses back might make it.
	await page.route('**/api/stream', (route) => route.abort());
	const asked = countStatusRequests(page);
	await login(page);
	await navigate(page, 'Clients');
	const initial = asked.filter((r) => r === 'GET /api/clients').length;

	cli('client', 'add', 'Fallback Probe');
	await expect(page.getByRole('link', { name: 'Fallback Probe' })).toBeVisible({ timeout: 9000 });
	expect(asked.filter((r) => r === 'GET /api/clients').length).toBeGreaterThan(initial);
	cli('client', 'delete', '--yes', 'Fallback Probe');
});
