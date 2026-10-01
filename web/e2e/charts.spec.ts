import { expect, test } from '@playwright/test';
import { login, mockTraffic, navigate, watchConsole } from './helpers';

// The tests share one daemon with app.spec.ts (playwright.config.ts: workers: 1), and run
// after its setup test, so the admin account already exists.
test.describe.configure({ mode: 'serial' });

const titles = ['Received', 'Sent', 'Cumulative Received', 'Cumulative Sent'];

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
	for (const title of titles) {
		await expect(page.getByRole('heading', { name: title, exact: true })).toBeVisible();
	}
	// The daemon here has moved no traffic, so every chart says so instead of drawing a floor.
	await expect(page.getByText('No traffic in this range')).toHaveCount(4);
	await expect(page.locator('.u-over')).toHaveCount(0);
	expect(problems).toEqual([]);
});

test('the charts are stacked, each as wide as the dashboard chart', async ({ page }) => {
	await mockTraffic(page);
	await login(page);
	const dashboard = await page.getByRole('region', { name: 'Bandwidth' }).boundingBox();

	await navigate(page, 'Charts');
	await expect(page.locator('.u-over')).toHaveCount(4);
	const boxes = [];
	for (const title of titles) {
		const box = await page.getByRole('region', { name: title, exact: true }).boundingBox();
		expect(box, title).not.toBeNull();
		boxes.push(box!);
	}
	for (const [i, box] of boxes.entries()) {
		// One column: the same left edge and width as the dashboard's chart, and each below the last.
		expect(box.x).toBeCloseTo(dashboard!.x, 0);
		expect(box.width).toBeCloseTo(dashboard!.width, 0);
		if (i > 0) expect(box.y).toBeGreaterThanOrEqual(boxes[i - 1].y + boxes[i - 1].height);
	}
});

test('the charts draw a line per client that moved traffic, with a legend and a hover tooltip', async ({
	page
}) => {
	const problems = watchConsole(page);
	const requested = await mockTraffic(page);

	await login(page);
	await navigate(page, 'Charts');
	await expect(page.locator('.u-over')).toHaveCount(4);
	expect(requested).toContain('24h');

	// Only the client that moved traffic is a line, and it's in every chart's legend.
	for (const title of titles) {
		const legend = page.getByRole('list', { name: `${title} legend`, exact: true });
		await expect(legend).toHaveText('Phone');
	}

	// A tooltip follows the cursor: the time on a 24-hour clock, and the client's value there.
	const received = page.getByRole('img', { name: 'Received chart', exact: true });
	await received.hover({ position: { x: 600, y: 100 } });
	const tip = page.locator('.chart-tooltip:visible');
	await expect(tip).toHaveCount(1);
	await expect(tip).toContainText('Phone');
	await expect(tip).toContainText(/bps/);
	await expect(tip).toContainText(/\b\d{1,2} [A-Z][a-z]{2} at \d{2}:\d{2}/);
	await expect(tip).not.toContainText(/\d\s?[AP]M\b/i);
	await page.mouse.move(0, 0);
	await expect(page.locator('.chart-tooltip:visible')).toHaveCount(0);

	// The cumulative charts' tooltips are in bytes.
	await page
		.getByRole('img', { name: 'Cumulative Sent chart', exact: true })
		.hover({ position: { x: 600, y: 100 } });
	await expect(page.locator('.chart-tooltip:visible')).toContainText(/\d (B|KB|MB)\b/);

	// Every range is one request, and 1m, the live range, has its own data.
	for (const range of ['1m', '1h', '12h', '7d', '30d', '90d']) {
		await page.getByLabel('Range').selectOption(range);
		await expect.poll(() => requested.at(-1)).toBe(range);
	}
	await page.getByLabel('Range').selectOption('1m');
	await expect(page.locator('.u-over')).toHaveCount(4);
	await received.hover({ position: { x: 600, y: 100 } });
	await expect(page.locator('.chart-tooltip:visible')).toContainText('Phone');
	// A minute is read to the second.
	await expect(page.locator('.chart-tooltip:visible')).toContainText(/ at \d{2}:\d{2}:\d{2}/);
	expect(problems).toEqual([]);
});

test('the range chosen on one chart is the range of every chart, and is remembered', async ({
	page
}) => {
	const problems = watchConsole(page);
	const requested = await mockTraffic(page);
	const range = page.getByLabel('Range');

	// Chosen on the dashboard…
	await login(page);
	await expect(range).toHaveValue('24h');
	await range.selectOption('12h');
	await expect.poll(() => requested.at(-1)).toBe('12h');

	// …it's the Charts page's range too, and its first request asks for it.
	requested.length = 0;
	await navigate(page, 'Charts');
	await expect(range).toHaveValue('12h');
	await expect(page.locator('.u-over')).toHaveCount(4);
	expect(requested[0]).toBe('12h');

	// Chosen there, it's the dashboard's again…
	await range.selectOption('7d');
	await expect.poll(() => requested.at(-1)).toBe('7d');
	await page.getByRole('link', { name: 'Drawbridge' }).click();
	await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();
	await expect(range).toHaveValue('7d');

	// …and it survives a reload, which starts a new page load from this browser's storage.
	await page.reload();
	await expect(range).toHaveValue('7d');
	await expect(page.locator('.u-over')).toHaveCount(1);
	expect(requested.at(-1)).toBe('7d');
	expect(problems).toEqual([]);
});
