import { expect, type Page } from '@playwright/test';
import { execFileSync } from 'node:child_process';
import { join } from 'node:path';
import { stateDir } from '../playwright.config';

const bin = process.env.DRAWBRIDGE_BIN ?? '../dist/drawbridge';

/** Runs a drawbridge CLI command against the test daemon, and returns its output. */
export function cli(...args: string[]): string {
	return execFileSync(bin, [...args, '--control', join(stateDir, 'control.sock')], {
		encoding: 'utf8'
	});
}

/** The pending setup token, from `drawbridge admin setup-token`. */
export function setupToken(): string {
	const m = cli('admin', 'setup-token').match(/Setup token: (\S+)/);
	if (!m) throw new Error('no setup token');
	return m[1];
}

export const admin = { username: 'admin', password: 'a long password' };

/**
 * Collects script errors and CSP violations, so a test can check there were none. The
 * browser also logs every 4xx response, which the tests cause on purpose (a wrong password,
 * or checking whether the admin is logged in), so those don't count.
 */
export function watchConsole(page: Page): string[] {
	const problems: string[] = [];
	page.on('console', (m) => {
		if (m.type() === 'error' && !/responded with a status of 4\d\d/.test(m.text())) {
			problems.push(m.text());
		}
	});
	page.on('pageerror', (e) => problems.push(e.message));
	return problems;
}

export async function login(page: Page, password = admin.password) {
	await page.goto('/login');
	await page.getByLabel('Username').fill(admin.username);
	await page.getByLabel('Password').fill(password);
	await page.getByRole('button', { name: 'Log In' }).click();
	await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();
}

/** Follows a link in the main navigation. */
export async function navigate(page: Page, name: string) {
	await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name }).click();
}

/** Opens the user menu (the account icon at the right of the header), and returns it. */
export async function openUserMenu(page: Page) {
	await page.getByRole('button', { name: 'Account menu' }).click();
	return page.getByRole('group', { name: 'Account' });
}

/** Logs out through the user menu. */
export async function logOut(page: Page) {
	const menu = await openUserMenu(page);
	await menu.getByRole('button', { name: 'Log Out' }).click();
}

// The fake backend never moves a peer's counters, and a real flush takes a minute, so the chart
// tests mock the traffic endpoints. The windows end a bucket ago, as the server's do: it leaves
// out a bucket that's still filling.
function trafficWindow(range: string) {
	const live = range === '1m';
	const step = live ? 5 : ['7d', '30d', '90d'].includes(range) ? 3600 : 60;
	const now = Math.floor(Date.now() / 1000);
	const until = live ? now : Math.floor(now / step) * step - step;
	const at = (buckets: number) => new Date((until - buckets * step) * 1000).toISOString();
	return { step, until: new Date(until * 1000).toISOString(), at };
}

/** A traffic series (the total, or one client's) over a range, as `GET /api/traffic` returns it. */
export function trafficSeries(range: string) {
	const w = trafficWindow(range);
	return {
		step_seconds: w.step,
		until: w.until,
		samples: [5, 4, 3, 2].map((n, i) => ({
			bucket_start: w.at(n),
			receive_bytes: 120_000 * (i + 1),
			send_bytes: 480_000 * (i + 1)
		}))
	};
}

/** Every client's traffic over a range, as `GET /api/traffic/clients` returns it: one idle client. */
export function trafficHistory(range: string) {
	const { step_seconds, until, samples } = trafficSeries(range);
	return {
		step_seconds,
		until,
		clients: [
			{ id: 'c1', name: 'Laptop', samples: [] },
			{ id: 'c2', name: 'Phone', samples }
		]
	};
}

/**
 * Answers every traffic endpoint with the mocks above, and records each range asked for. Routes
 * that pass a client's own history (`/api/clients/{id}/traffic`) get the same series.
 */
export async function mockTraffic(page: Page, requested: string[] = []) {
	const rangeOf = (url: string) => new URL(url).searchParams.get('range') ?? '';
	await page.route('**/api/traffic**', async (route) => {
		const range = rangeOf(route.request().url());
		requested.push(range);
		const clients = new URL(route.request().url()).pathname.endsWith('/clients');
		await route.fulfill({ json: clients ? trafficHistory(range) : trafficSeries(range) });
	});
	await page.route('**/api/clients/*/traffic**', async (route) => {
		const range = rangeOf(route.request().url());
		requested.push(range);
		await route.fulfill({ json: trafficSeries(range) });
	});
	return requested;
}
