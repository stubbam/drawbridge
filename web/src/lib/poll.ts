/**
 * Runs fn now and then every `ms` milliseconds, skipping ticks while the page is hidden,
 * until the returned function is called. Use it from an effect, which calls the returned
 * function on cleanup: `$effect(() => poll(load, 5000))`.
 */
export function poll(fn: () => Promise<unknown>, ms: number): () => void {
	let stopped = false;
	let timer: ReturnType<typeof setTimeout> | undefined;
	const tick = async () => {
		if (typeof document === 'undefined' || !document.hidden) {
			try {
				await fn();
			} catch {
				// The page shows its own error; keep polling, so it recovers.
			}
		}
		if (!stopped) timer = setTimeout(tick, ms);
	};
	void tick();
	return () => {
		stopped = true;
		clearTimeout(timer);
	};
}
