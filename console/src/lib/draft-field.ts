/**
 * A text field that keeps what the user typed when the page data is re-read (the Refresh button, a background
 * refresh): the draft follows the stored value only when that value itself changes (a save, someone else's edit),
 * never because the same value was loaded again. Kept out of the components so it is tested without a DOM.
 */
export function createDraftFollower(initial: string): (stored: string, draft: string) => string {
	let seen = initial;
	return (stored, draft) => {
		if (stored === seen) return draft;
		seen = stored;
		return stored;
	};
}
