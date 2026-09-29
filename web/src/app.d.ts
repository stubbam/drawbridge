// See https://svelte.dev/docs/kit/types#app.d.ts
// for information about these interfaces
declare global {
	namespace App {
		// interface Error {}
		// interface Locals {}
		// interface PageData {}
		interface PageState {
			/** Set when navigating to a client that was just added, to show its QR code. */
			justAdded?: boolean;
		}
		// interface Platform {}
	}
}

export {};
