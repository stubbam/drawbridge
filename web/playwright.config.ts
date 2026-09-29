import { defineConfig, devices } from '@playwright/test';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

// The end-to-end tests drive the real web app, embedded in the real binary, against
// `drawbridge serve --backend fake`: everything but the kernel (docs/PLAN.md §12).
// `make test-e2e` builds the binary and sets DRAWBRIDGE_BIN.
export const port = 51899; // Not 51821, so a real Drawbridge on the same machine is left alone.
export const stateDir = join(tmpdir(), 'drawbridge-e2e');
const bin = process.env.DRAWBRIDGE_BIN ?? '../dist/drawbridge';

export default defineConfig({
	testDir: 'e2e',
	fullyParallel: false,
	workers: 1,
	forbidOnly: !!process.env.CI,
	reporter: process.env.CI ? [['list'], ['github']] : 'list',
	use: {
		baseURL: `https://127.0.0.1:${port}`,
		// The daemon's certificate is self-signed.
		ignoreHTTPSErrors: true,
		trace: 'retain-on-failure'
	},
	projects: [
		{
			name: 'chromium',
			use: {
				...devices['Desktop Chrome'],
				// A preinstalled Chromium, where Playwright's own download isn't available.
				launchOptions: { executablePath: process.env.PLAYWRIGHT_CHROMIUM_PATH || undefined }
			}
		}
	],
	webServer: {
		command: `sh e2e/serve.sh "${bin}" "${stateDir}" ${port}`,
		url: `https://127.0.0.1:${port}/healthz`,
		ignoreHTTPSErrors: true,
		reuseExistingServer: false,
		stdout: 'pipe',
		stderr: 'pipe',
		timeout: 30_000
	}
});
