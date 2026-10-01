import { expect, test } from '@playwright/test';
import { login, navigate, watchConsole } from './helpers';

// The tests share one daemon with app.spec.ts (playwright.config.ts: workers: 1), and run
// after its setup test, so the admin account already exists.
test.describe.configure({ mode: 'serial' });

// The fake backend never moves a peer's counters, so the lines come from a mocked response.
// The history ends a minute ago, as the server's does (it leaves out a bucket still filling).
function history(range: string) {
	const live = range === '1m';
	const until = Math.floor(Date.now() / 1000 / 60) * 60 - 60;
	const at = (secondsAgo: number) => new Date((until - secondsAgo) * 1000).toISOString();
	return {
		step_seconds: live ? 5 : 60,
		until: new Date(until * 1000).toISOString(),
		clients: [
			// Idle in the range: no line, and no place in the legend.
			{ id: 'c1', name: 'Laptop', samples: [] },
			{
				id: 'c2',
				name: 'Phone',
				samples: live
					? [40, 35, 30, 25, 20].map((s, i) => ({
							bucket_start: at(s),
							receive_bytes: 1000 * (i + 1),
							send_bytes: 50_000 * (i + 1)
						}))
					: [300, 240, 180, 120].map((s, i) => ({
							bucket_start: at(s),
							receive_bytes: 120_000 * (i + 1),
							send_bytes: 480_000 * (i + 1)
						}))
			}
		]
	};
}

test('the Charts icon sits between Clients and Server Settings and opens the Charts page', async ({
	page
}) => {
	const problems = watchConsole(page);
	await login(page);
	const links = page.getByRole('navigation', { name: 'Main' }).getByRole('link');
	await expect(
		links.evaluateAll((els) => els.map((e) => e.getAttribute('aria-label')))
	).resolves.toEqual(['Clients', 'Charts', 'Server Settings', 'Logs']);

	await navigate(page, 'Charts');
	await expect(page).toHaveURL(/\/charts$/);
	await expect(page.getByRole('heading', { name: 'Charts', level: 1 })).toBeVisible();
	for (const title of ['Received', 'Sent', 'Cumulative Received', 'Cumulative Sent']) {
		await expect(page.getByRole('heading', { name: title, exact: true })).toBeVisible();
	}
	// The daemon here has moved no traffic, so every chart says so instead of drawing a floor.
	await expect(page.getByText('No traffic in this range')).toHaveCount(4);
	await expect(page.locator('.u-over')).toHaveCount(0);
	expect(problems).toEqual([]);
});

test('the charts draw a line per client that moved traffic, with a legend and a hover tooltip', async ({
	page
}) => {
	const problems = watchConsole(page);
	const requested: string[] = [];
	await page.route('**/api/traffic/clients**', async (route) => {
		const range = new URL(route.request().url()).searchParams.get('range') ?? '';
		requested.push(range);
		await route.fulfill({ json: history(range) });
	});

	await login(page);
	await navigate(page, 'Charts');
	await expect(page.locator('.u-over')).toHaveCount(4);
	expect(requested[0]).toBe('24h');

	// Only the client that moved traffic is a line, and it's in every chart's legend.
	for (const title of ['Received', 'Sent', 'Cumulative Received', 'Cumulative Sent']) {
		const legend = page.getByRole('list', { name: `${title} legend`, exact: true });
		await expect(legend).toHaveText('Phone');
	}

	// A tooltip follows the cursor: the time, and the client's value there.
	const received = page.getByRole('img', { name: 'Received chart', exact: true });
	await received.hover({ position: { x: 400, y: 100 } });
	const tip = page.locator('.chart-tooltip:visible');
	await expect(tip).toHaveCount(1);
	await expect(tip).toContainText('Phone');
	await expect(tip).toContainText(/bps/);
	await page.mouse.move(0, 0);
	await expect(page.locator('.chart-tooltip:visible')).toHaveCount(0);

	// The cumulative charts' tooltips are in bytes.
	await page
		.getByRole('img', { name: 'Cumulative Sent chart', exact: true })
		.hover({ position: { x: 400, y: 100 } });
	await expect(page.locator('.chart-tooltip:visible')).toContainText(/\d (B|KB|MB)\b/);

	// Every range is one request, and 1m, the live range, has its own data.
	for (const range of ['1m', '1h', '12h', '7d', '30d', '90d']) {
		await page.getByLabel('Range').selectOption(range);
		await expect.poll(() => requested.at(-1)).toBe(range);
	}
	await page.getByLabel('Range').selectOption('1m');
	await expect(page.locator('.u-over')).toHaveCount(4);
	await received.hover({ position: { x: 400, y: 100 } });
	await expect(page.locator('.chart-tooltip:visible')).toContainText('Phone');
	expect(problems).toEqual([]);
});
