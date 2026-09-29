/** Version information that the Go server reports at `/api/version`. */
export interface VersionInfo {
	/** The bare version, such as "1.2.3". */
	version: string;
	/** The short git commit the server was built from. */
	commit: string;
}

/**
 * Formats version information for people, with the lowercase "v" prefix, the same way as
 * `drawbridge version`: "v1.2.3 (commit abc1234)".
 */
export function formatVersion(info: VersionInfo): string {
	return `v${info.version} (commit ${info.commit})`;
}

/** Fetches the server's version information. */
export async function fetchVersion(fetchFn: typeof fetch = fetch): Promise<VersionInfo> {
	const res = await fetchFn('/api/version');
	if (!res.ok) {
		throw new Error(`GET /api/version failed with status ${res.status}`);
	}
	const body: unknown = await res.json();
	if (!isVersionInfo(body)) {
		throw new Error('GET /api/version returned an unexpected body');
	}
	return { version: body.version, commit: body.commit };
}

function isVersionInfo(value: unknown): value is VersionInfo {
	if (typeof value !== 'object' || value === null) {
		return false;
	}
	const { version, commit } = value as Record<string, unknown>;
	return typeof version === 'string' && typeof commit === 'string';
}
