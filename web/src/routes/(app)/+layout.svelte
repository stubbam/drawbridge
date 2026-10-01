<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { api, onUnauthorized } from '$lib/api';
	import { cycleMode, modeLabels, theme } from '$lib/theme.svelte';
	import AddClientModal from '$lib/components/AddClientModal.svelte';

	let { data, children } = $props();

	// A request that finds the session over (an hour idle, twelve hours at most, or
	// revoked) goes back to the login.
	onUnauthorized(() => {
		// The login page returns here afterward; the query string is why this isn't a bare
		// resolve().
		// eslint-disable-next-line svelte/no-navigation-without-resolve
		void goto(resolve('/login') + '?next=' + encodeURIComponent(page.url.pathname));
	});

	function current(href: string): boolean {
		const path = page.url.pathname;
		return path === href || path.startsWith(href + '/');
	}

	async function logout() {
		showUserMenu = false;
		try {
			await api.logout();
		} finally {
			await goto(resolve('/login'));
		}
	}

	let modeLabel = $derived(`Switch Mode (${modeLabels[theme.mode]})`);

	let showAddClient = $state(false);
	let showUserMenu = $state(false);
	let userMenuEl = $state<HTMLElement>();

	function onWindowClick(e: MouseEvent) {
		if (showUserMenu && userMenuEl && !userMenuEl.contains(e.target as Node)) {
			showUserMenu = false;
		}
	}
	function onWindowKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape') showUserMenu = false;
	}
</script>

<svelte:window onclick={onWindowClick} onkeydown={onWindowKeydown} />

{#snippet tooltip(label: string)}
	<span
		role="tooltip"
		class="pointer-events-none absolute top-full left-1/2 z-10 mt-1.5 -translate-x-1/2 rounded bg-neutral-900 px-1.5 py-0.5 text-[11px] whitespace-nowrap text-white opacity-0 transition-opacity group-hover:opacity-100 dark:bg-neutral-700"
		>{label}</span
	>
{/snippet}

<header class="border-b border-neutral-200 bg-white dark:border-neutral-800 dark:bg-neutral-900">
	<div class="mx-auto flex max-w-5xl flex-wrap items-center gap-x-6 gap-y-1 px-4 py-3">
		<a href={resolve('/')} class="text-2xl font-semibold tracking-tight">Drawbridge</a>
		<nav class="ml-auto flex items-center gap-1" aria-label="Main">
			<a
				href={resolve('/clients')}
				class="group relative flex items-center rounded-md p-2 {current(resolve('/clients'))
					? 'text-neutral-900 dark:text-white'
					: 'text-neutral-500 hover:text-neutral-900 dark:text-neutral-400 dark:hover:text-white'}"
				aria-label="Clients"
			>
				<svg
					class="size-4"
					viewBox="0 0 24 24"
					fill="none"
					stroke="currentColor"
					stroke-width="2"
					stroke-linecap="round"
					stroke-linejoin="round"
					aria-hidden="true"
				>
					<path
						d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"
					/>
				</svg>
				{@render tooltip('Clients')}
			</a>
			<a
				href={resolve('/charts')}
				class="group relative flex items-center rounded-md p-2 {current(resolve('/charts'))
					? 'text-neutral-900 dark:text-white'
					: 'text-neutral-500 hover:text-neutral-900 dark:text-neutral-400 dark:hover:text-white'}"
				aria-label="Charts"
			>
				<svg
					class="size-4"
					viewBox="0 0 24 24"
					fill="none"
					stroke="currentColor"
					stroke-width="2"
					stroke-linecap="round"
					stroke-linejoin="round"
					aria-hidden="true"
				>
					<path d="M3 3v16a2 2 0 0 0 2 2h16" />
					<path d="m19 9-5 5-4-4-3 3" />
				</svg>
				{@render tooltip('Charts')}
			</a>
			<a
				href={resolve('/settings')}
				class="group relative flex items-center rounded-md p-2 {current(resolve('/settings'))
					? 'text-neutral-900 dark:text-white'
					: 'text-neutral-500 hover:text-neutral-900 dark:text-neutral-400 dark:hover:text-white'}"
				aria-label="Server Settings"
			>
				<svg
					class="size-4"
					viewBox="0 0 24 24"
					fill="none"
					stroke="currentColor"
					stroke-width="2"
					stroke-linecap="round"
					stroke-linejoin="round"
					aria-hidden="true"
				>
					<rect width="20" height="8" x="2" y="2" rx="2" ry="2" />
					<rect width="20" height="8" x="2" y="14" rx="2" ry="2" />
					<line x1="6" x2="6.01" y1="6" y2="6" />
					<line x1="6" x2="6.01" y1="18" y2="18" />
				</svg>
				{@render tooltip('Server Settings')}
			</a>
			<a
				href={resolve('/logs')}
				class="group relative flex items-center rounded-md p-2 {current(resolve('/logs'))
					? 'text-neutral-900 dark:text-white'
					: 'text-neutral-500 hover:text-neutral-900 dark:text-neutral-400 dark:hover:text-white'}"
				aria-label="Logs"
			>
				<svg
					class="size-4"
					viewBox="0 0 24 24"
					fill="none"
					stroke="currentColor"
					stroke-width="2"
					stroke-linecap="round"
					stroke-linejoin="round"
					aria-hidden="true"
				>
					<path d="M3 5h1" />
					<path d="M3 12h1" />
					<path d="M3 19h1" />
					<path d="M8 5h1" />
					<path d="M8 12h1" />
					<path d="M8 19h1" />
					<path d="M13 5h8" />
					<path d="M13 12h8" />
					<path d="M13 19h8" />
				</svg>
				{@render tooltip('Logs')}
			</a>
			<!-- The icon shows the current mode; a click moves to the next one. -->
			<button
				type="button"
				class="group relative flex items-center rounded-md p-2 text-neutral-500 hover:text-neutral-900 dark:text-neutral-400 dark:hover:text-white"
				aria-label={modeLabel}
				onclick={cycleMode}
			>
				{#if theme.mode === 'light'}
					<svg
						class="size-4"
						viewBox="0 0 24 24"
						fill="none"
						stroke="currentColor"
						stroke-width="2"
						stroke-linecap="round"
						stroke-linejoin="round"
						aria-hidden="true"
					>
						<circle cx="12" cy="12" r="4" />
						<path d="M12 2v2" />
						<path d="M12 20v2" />
						<path d="m4.93 4.93 1.41 1.41" />
						<path d="m17.66 17.66 1.41 1.41" />
						<path d="M2 12h2" />
						<path d="M20 12h2" />
						<path d="m6.34 17.66-1.41 1.41" />
						<path d="m19.07 4.93-1.41 1.41" />
					</svg>
				{:else if theme.mode === 'dark'}
					<svg
						class="size-4"
						viewBox="0 0 24 24"
						fill="none"
						stroke="currentColor"
						stroke-width="2"
						stroke-linecap="round"
						stroke-linejoin="round"
						aria-hidden="true"
					>
						<path
							d="M20.985 12.486a9 9 0 1 1-9.473-9.472c.405-.022.617.46.402.803a6 6 0 0 0 8.268 8.268c.344-.215.825-.004.803.401"
						/>
					</svg>
				{:else}
					<svg
						class="size-4"
						viewBox="0 0 24 24"
						fill="none"
						stroke="currentColor"
						stroke-width="2"
						stroke-linecap="round"
						stroke-linejoin="round"
						aria-hidden="true"
					>
						<path d="M12 2v2" />
						<path
							d="M14.837 16.385a6 6 0 1 1-7.223-7.222c.624-.147.97.66.715 1.248a4 4 0 0 0 5.26 5.259c.589-.255 1.396.09 1.248.715"
						/>
						<path d="M16 12a4 4 0 0 0-4-4" />
						<path d="m19 5-1.256 1.256" />
						<path d="M20 12h2" />
					</svg>
				{/if}
				{@render tooltip(modeLabel)}
			</button>
			<div class="relative" bind:this={userMenuEl}>
				<button
					type="button"
					class="flex items-center rounded-md p-2 text-neutral-500 hover:text-neutral-900 dark:text-neutral-400 dark:hover:text-white"
					onclick={() => (showUserMenu = !showUserMenu)}
					aria-label="Account menu"
					aria-expanded={showUserMenu}
				>
					<svg
						class="size-4"
						viewBox="0 0 24 24"
						fill="none"
						stroke="currentColor"
						stroke-width="2"
						stroke-linecap="round"
						stroke-linejoin="round"
						aria-hidden="true"
					>
						<path d="M19 21v-2a4 4 0 0 0-4-4H9a4 4 0 0 0-4 4v2" />
						<circle cx="12" cy="7" r="4" />
					</svg>
				</button>
				{#if showUserMenu}
					<div
						role="group"
						aria-label="Account"
						class="absolute right-0 z-20 mt-2 w-56 rounded-lg border border-neutral-200 bg-white p-1 shadow-lg dark:border-neutral-800 dark:bg-neutral-900"
					>
						<p class="px-3 py-2 text-sm font-bold text-neutral-900 dark:text-white">
							{data.me.user.username}
						</p>
						<hr class="my-1 border-neutral-200 dark:border-neutral-800" />
						<a
							href={resolve('/account')}
							class="flex items-center gap-2 rounded-md px-3 py-2 text-sm text-neutral-700 hover:bg-neutral-100 dark:text-neutral-300 dark:hover:bg-neutral-800"
							onclick={() => (showUserMenu = false)}
						>
							<svg
								class="size-4"
								viewBox="0 0 24 24"
								fill="none"
								stroke="currentColor"
								stroke-width="2"
								stroke-linecap="round"
								stroke-linejoin="round"
								aria-hidden="true"
							>
								<path d="M10 15H6a4 4 0 0 0-4 4v2" />
								<path d="m14.305 16.53.923-.382" />
								<path d="m15.228 13.852-.923-.383" />
								<path d="m16.852 12.228-.383-.923" />
								<path d="m16.852 17.772-.383.924" />
								<path d="m19.148 12.228.383-.923" />
								<path d="m19.53 18.696-.382-.924" />
								<path d="m20.772 13.852.924-.383" />
								<path d="m20.772 16.148.924.383" />
								<circle cx="18" cy="15" r="3" />
								<circle cx="9" cy="7" r="4" />
							</svg>
							Settings
						</a>
						<button
							type="button"
							class="flex w-full items-center gap-2 rounded-md px-3 py-2 text-left text-sm text-neutral-700 hover:bg-neutral-100 dark:text-neutral-300 dark:hover:bg-neutral-800"
							onclick={logout}
						>
							<svg
								class="size-4"
								viewBox="0 0 24 24"
								fill="none"
								stroke="currentColor"
								stroke-width="2"
								stroke-linecap="round"
								stroke-linejoin="round"
								aria-hidden="true"
							>
								<path d="m16 17 5-5-5-5" />
								<path d="M21 12H9" />
								<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
							</svg>
							Log Out
						</button>
					</div>
				{/if}
			</div>
			<button
				type="button"
				class="flex items-center gap-1.5 rounded-md px-2 py-1.5 text-sm font-medium text-neutral-900 hover:text-neutral-600 dark:text-white dark:hover:text-neutral-300"
				onclick={() => (showAddClient = true)}
			>
				<svg
					class="size-4 text-neutral-400"
					viewBox="0 0 24 24"
					fill="none"
					stroke="currentColor"
					stroke-width="2"
					stroke-linecap="round"
					stroke-linejoin="round"
					aria-hidden="true"
				>
					<path d="M5 12h14" />
					<path d="M12 5v14" />
				</svg>
				Add Client
			</button>
		</nav>
	</div>
</header>

<AddClientModal bind:open={showAddClient} />

<main class="mx-auto flex max-w-5xl flex-col gap-6 px-4 py-6">
	{@render children()}
</main>
