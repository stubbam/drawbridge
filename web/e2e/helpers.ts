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
