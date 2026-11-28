/**
 * The page Refresh button (PageHeader actions on the project, host and org pages): re-runs the page's server load
 * in place, so changes made elsewhere (MCP, another tab, another person) show without a browser reload. One refresh
 * at a time; a click while one runs does nothing. Kept out of the component so it is tested without a DOM.
 */
export function singleRefresh(run: () => Promise<unknown>, onPending: (pending: boolean) => void): () => Promise<void> {
	let pending = false;
	return async () => {
		if (pending) return;
		pending = true;
		onPending(true);
		try {
			await run();
		} catch {
			// The load reports its own failures on the page; a failed refresh leaves the page as it was.
		} finally {
			pending = false;
			onPending(false);
		}
	};
}
