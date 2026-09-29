import { redirect } from '@sveltejs/kit';
import { resolve } from '$app/paths';
import { api, ApiError } from '$lib/api';
import type { LayoutLoad } from './$types';

// Every page in this group needs a session. Without one, go to first-run setup if the
// admin account doesn't exist yet, and to the login otherwise.
export const load: LayoutLoad = async ({ url }) => {
	try {
		return { me: await api.me() };
	} catch (err) {
		if (err instanceof ApiError && err.status === 401) {
			const { needed } = await api.setupStatus();
			if (needed) redirect(307, resolve('/setup'));
			redirect(307, resolve('/login') + '?next=' + encodeURIComponent(url.pathname));
		}
		throw err;
	}
};
