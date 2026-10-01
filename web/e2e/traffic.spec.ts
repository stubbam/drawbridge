import { expect, test } from '@playwright/test';
import { login, mockTraffic, watchConsole } from './helpers';

// The tests share one daemon with app.spec.ts (playwright.config.ts: workers: 1), and run
// after its setup test, so the admin account already exists.
test.describe.configure({ mode: 'serial' });

// The tooltip's heading: a day and month, then a 24-hour time. Never am or pm.
const tooltipTime = /\b\d{1,2} [A-Z][a-z]{2} at \d{2}:\d{2}/;

test('the dashboard shows a bandwidth chart with a working range control', async ({
	page
}) => {
	const problems = watchConsole(page);
	const requestedRanges = await mockTraffic(page);

	await login(page);
	const chart = page.getByRole('img', { name: 'Bandwidth chart' });
	await expect(chart).toBeVisible();
	// The heading above the chart is the only label: the chart doesn't draw its own title.
	await expect(page.getByText('Bandwidth', { exact: true })).toHaveCount(1);
	await expect(page.locator('.u-title')).toHaveCount(0);
	expect(requestedRanges).toContain('24h');

	// A tooltip follows the cursor: the time on a 24-hour clock, what the server received and sent,
	// and their total.
	await chart.hover({ position: { x: 600, y: 100 } });
	const tip = page.locator('.chart-tooltip:visible');
	await expect(tip).toHaveCount(1);
	await expect(tip).toContainText(tooltipTime);
	await expect(tip).not.toContainText(/\d\s?[AP]M\b/i);
	for (const name of ['Received', 'Sent', 'Total']) await expect(tip).toContainText(name);
	await expect(tip).toContainText(/bps/);

	// The range control has no "Range" in front of it, and offers every range.
	await expect(page.getByText('Range', { exact: true })).toHaveCount(0);
	await expect(page.getByLabel('Range').locator('option')).toHaveText([
		'1 Minute',
		'1 Hour',
		'12 Hours',
		'24 Hours',
		'1 Week',
		'30 Days',
		'90 Days'
	]);
	for (const range of ['1m', '1h', '12h', '7d', '30d', '90d']) {
		await page.getByLabel('Range').selectOption(range);
		await expect.poll(() => requestedRanges.at(-1)).toBe(range);
	}
	await expect(page.locator('.u-over')).toHaveCount(1);
	expect(problems).toEqual([]);
});

test('a client detail page has its own charts, session history, and Pause, Rename, and Delete', async ({
	page
}) => {
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
	await mockTraffic(page);
	await page.route('**/api/clients/*/sessions**', (route) =>
		route.fulfill({ json: sessionsFixture })
	);

	await login(page);
	await page.getByRole('button', { name: 'Add Client' }).click();
	await page.getByLabel('Name').fill('Traffic Test Client');
	await page.getByRole('button', { name: 'Add', exact: true }).click();
	await expect(page.getByRole('heading', { name: 'Traffic Test Client' })).toBeVisible();

	// The Bandwidth chart and, below it, a cumulative one.
	await expect(page.getByRole('img', { name: 'Bandwidth chart' })).toBeVisible();
	await expect(page.getByRole('heading', { name: 'Bandwidth' })).toBeVisible();
	await expect(page.getByRole('img', { name: 'Cumulative Traffic chart' })).toBeVisible();
	await expect(page.getByRole('heading', { name: 'Cumulative Traffic' })).toBeVisible();
	await expect(page.locator('.u-title')).toHaveCount(0);
	await expect(page.locator('.u-over')).toHaveCount(2);
	await page
		.getByRole('img', { name: 'Cumulative Traffic chart' })
		.hover({ position: { x: 600, y: 100 } });
	const tip = page.locator('.chart-tooltip:visible');
	await expect(tip).toContainText(/\d (B|KB|MB)\b/);
	for (const name of ['Received', 'Sent', 'Total']) await expect(tip).toContainText(name);
	await page.mouse.move(0, 0);

	// Sessions show the time on a 24-hour clock, with a date like "28 Sep".
	await expect(page.getByText('203.0.113.5:51820')).toBeVisible();
	await expect(page.getByText(/\b28 Sep( 2026)? \d{2}:\d{2}:\d{2}/).first()).toBeVisible();
	await expect(page.getByText('ongoing')).toBeVisible();

	// No Manage box: its buttons are at the top, next to the name, in this order.
	await expect(page.getByRole('heading', { name: 'Manage' })).toHaveCount(0);
	await expect(
		page.getByRole('button', { name: /^(Pause|Resume|Rename|Delete)$/ }).allInnerTexts()
	).resolves.toEqual(['Pause', 'Rename', 'Delete']);

	// Rename opens a popup, which Cancel closes without changing anything.
	await page.getByRole('button', { name: 'Rename', exact: true }).click();
	const rename = page.getByRole('dialog', { name: 'Rename Client' });
	await expect(rename).toBeVisible();
	await expect(rename.getByLabel('Name')).toHaveValue('Traffic Test Client');
	await rename.getByRole('button', { name: 'Cancel' }).click();
	await expect(rename).toBeHidden();
	await expect(page.getByRole('heading', { name: 'Traffic Test Client' })).toBeVisible();

	// Delete asks first. Cancel keeps the client; Delete removes it, back at the client list.
	await page.getByRole('button', { name: 'Delete', exact: true }).click();
	const confirm = page.getByRole('dialog', { name: 'Delete Client' });
	await expect(confirm).toContainText('Traffic Test Client');
	await confirm.getByRole('button', { name: 'Cancel' }).click();
	await expect(confirm).toBeHidden();
	await expect(page.getByRole('heading', { name: 'Traffic Test Client' })).toBeVisible();

	// Leave the client list as this test found it.
	await page.getByRole('button', { name: 'Delete', exact: true }).click();
	await confirm.getByRole('button', { name: 'Delete', exact: true }).click();
	await expect(page).toHaveURL(/\/clients$/);
	expect(problems).toEqual([]);
});
