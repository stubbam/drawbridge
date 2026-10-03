import { expect, test } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { cli, login, navigate, watchConsole } from './helpers';

// The tests share one daemon with app.spec.ts (playwright.config.ts: workers: 1), and run
// after its setup test, so the admin account already exists.
test.describe.configure({ mode: 'serial' });

test('the log filters by category, event, client, and time', async ({ page }) => {
	const problems = watchConsole(page);
	cli('client', 'add', 'Log Probe');
	cli('client', 'pause', 'Log Probe');

	await login(page);
	await navigate(page, 'Logs');
	const rows = page.locator('tbody tr');
	await expect(rows.first()).toBeVisible();
	const everything = await rows.count();
	expect(everything).toBeGreaterThan(2);

	// One client's events, and only theirs.
	await page.getByLabel('Client', { exact: true }).selectOption({ label: 'Log Probe' });
	await expect(rows).toHaveCount(2);
	await expect(rows.nth(0)).toContainText('Paused a client: Log Probe');
	await expect(rows.nth(1)).toContainText('Added a client: Log Probe');

	// One kind of event: the choices follow the category, and a category that doesn't hold the
	// chosen event clears it.
	const event = page.getByLabel('Event', { exact: true });
	await event.selectOption({ label: 'Added a client' });
	await expect(rows).toHaveCount(1);
	await expect(rows.first()).toContainText('Added a client: Log Probe');
	await page.getByLabel('Client', { exact: true }).selectOption({ label: 'Any' });
	await expect(rows.first()).toContainText('Added a client');
	await page.getByLabel('Show', { exact: true }).selectOption('connection');
	await expect(event).toHaveValue('');
	await expect(event.locator('option')).toHaveText(['Any', 'Connected', 'Disconnected', 'Roamed']);
	await expect(rows).toHaveText('No events.');
	await page.getByLabel('Show', { exact: true }).selectOption('');
	await expect(rows).toHaveCount(everything);

	// A time asks the server for events since then, counted back from now.
	const asked = page.waitForRequest((r) => /\/api\/events\?.*from=/.test(r.url()));
	await page.getByLabel('When', { exact: true }).selectOption({ label: 'Last Hour' });
	const from = new Date(new URL((await asked).url()).searchParams.get('from')!);
	expect(Math.abs(Date.now() - 3_600_000 - from.getTime())).toBeLessThan(2 * 60_000);
	await expect(rows.filter({ hasText: 'Added a client: Log Probe' })).toHaveCount(1);

	cli('client', 'delete', '--yes', 'Log Probe');
	expect(problems).toEqual([]);
});

test('Export CSV saves every event the filters match', async ({ page }) => {
	const problems = watchConsole(page);
	await login(page);
	await navigate(page, 'Logs');
	await page.getByLabel('Event', { exact: true }).selectOption({ label: 'Logged in' });
	await expect(page.locator('tbody tr').first()).toContainText('Logged in');
	const shown = await page.locator('tbody tr').count();

	const downloading = page.waitForEvent('download');
	await page.getByRole('button', { name: 'Export CSV' }).click();
	const download = await downloading;
	expect(download.suggestedFilename()).toBe('drawbridge-events.csv');

	const lines = (await readFile((await download.path())!, 'utf8')).trim().split('\n');
	expect(lines[0]).toBe('time,event,category,actor,via,source_ip,client_id,client_name,details');
	// It's the filter, not the page: every row is a login, and none is missing.
	expect(lines.length - 1).toBeGreaterThanOrEqual(shown);
	for (const line of lines.slice(1)) expect(line).toMatch(/^\d{4}-\d\d-\d\dT[\d:]+Z,auth\.login,/);
	expect(problems).toEqual([]);
});
