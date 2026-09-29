import { ApiError } from './api';

/** A message for people about a failed request. */
export function errorMessage(err: unknown): string {
	if (err instanceof ApiError) {
		return err.message;
	}
	return "Can't reach the Drawbridge server. Check that drawbridge.service is running.";
}
