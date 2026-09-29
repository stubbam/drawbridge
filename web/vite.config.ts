import tailwindcss from '@tailwindcss/vite';
import type { ProxyOptions } from 'vite';
import { defineConfig } from 'vitest/config';
import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';

// The Go server (`drawbridge serve`) listens on 51821, over HTTPS with a self-signed
// certificate. `npm run dev` proxies the API to it, so the dev server on Vite's default
// port 5173 behaves like the embedded app. The proxy drops the Origin header, which names
// the dev server rather than the Go server and so would fail the CSRF check.
const goServer: ProxyOptions = {
	target: 'https://127.0.0.1:51821',
	secure: false,
	configure: (proxy) => {
		proxy.on('proxyReq', (req) => req.removeHeader('origin'));
	}
};

export default defineConfig({
	plugins: [
		tailwindcss(),
		sveltekit({
			compilerOptions: {
				// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},
			// A single-page app: the Go server returns 200.html for every path that isn't a
			// file, and the client-side router takes over (internal/api, internal/webui).
			adapter: adapter({ fallback: '200.html' })
		})
	],
	server: {
		proxy: {
			'/api': goServer,
			'/healthz': goServer
		}
	},
	test: {
		expect: { requireAssertions: true },
		projects: [
			{
				extends: './vite.config.ts',
				test: {
					name: 'server',
					environment: 'node',
					include: ['src/**/*.{test,spec}.{js,ts}'],
					exclude: ['src/**/*.svelte.{test,spec}.{js,ts}']
				}
			}
		]
	}
});
