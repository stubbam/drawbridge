import { expect, test } from '@playwright/test';
import { login, watchConsole } from './helpers';

// The tests share one daemon with app.spec.ts (playwright.config.ts: workers: 1), and run
// after its setup test, so the admin account already exists.
test.describe.configure({ mode: 'serial' });

// A real 60s buffered flush isn't usable in CI, so these mock the traffic and session
// endpoints instead of waiting on the fake backend to accumulate anything (it never does:
// nothing in the fake backend advances a peer's counters on its own).
const trafficFixture = [
	{ bucket_start: '2026-09-28T10:00:00Z', receive_bytes: 1000, send_bytes: 500 },
	{ bucket_start: '2026-09-28T10:01:00Z', receive_bytes: 2000, send_bytes: 900 }
];

test('the dashboard shows a total-throughput chart with a working range control', async ({
	page
}) => {
	const problems = watchConsole(page);
	const requestedRanges: string[] = [];
	await page.route('**/api/traffic**', async (route) => {
		requestedRanges.push(new URL(route.request().url()).searchParams.get('range') ?? '');
		await route.fulfill({ json: trafficFixture });
	});

	await login(page);
	await expect(page.getByRole('img', { name: 'All Clients chart' })).toBeVisible();
	expect(requestedRanges).toContain('24h');

	await page.getByLabel('Range').selectOption('7d');
	await expect.poll(() => requestedRanges.at(-1)).toBe('7d');
	expect(problems).toEqual([]);
});

test('a client detail page shows its own traffic chart and session history', async ({ page }) => {
	const problems = watchConsole(page);
	const sessionsFixture = [
		{
			id: 's1',
			started_at: '2026-09-28T10:00:00Z',
			endpoint: '203.0.113.5:51820',
			receive_bytes: 1000,
			send_bytes: 500
		}
	];
	await page.route('**/api/clients/*/traffic**', (route) =>
		route.fulfill({ json: trafficFixture })
	);
	await page.route('**/api/clients/*/sessions**', (route) =>
		route.fulfill({ json: sessionsFixture })
	);

	await login(page);
	await page.getByRole('button', { name: 'Add Client' }).click();
	await page.getByLabel('Name').fill('Traffic Test Client');
	await page.getByRole('button', { name: 'Add', exact: true }).click();
	await expect(page.getByRole('heading', { name: 'Traffic Test Client' })).toBeVisible();

	await expect(page.getByRole('img', { name: 'Traffic Test Client chart' })).toBeVisible();
	await expect(page.getByText('203.0.113.5:51820')).toBeVisible();
	await expect(page.getByText('ongoing')).toBeVisible();

	// Leave the client list as this test found it.
	await page.getByRole('button', { name: 'Delete this client…' }).click();
	await page.getByRole('button', { name: 'Delete', exact: true }).click();
	expect(problems).toEqual([]);
});
