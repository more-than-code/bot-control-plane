import { avatarSrc } from '$lib/avatar';
import { hasUnseenRelease, SEEN_VERSION_COOKIE } from '$lib/changelog';
import { latestVersion } from '$lib/server/changelog';
import { present } from '$lib/server/redact';
import { parseTheme, THEME_COOKIE } from '$lib/theme';
import { parseTimeZone, systemTimeZone, TIME_ZONE_COOKIE } from '$lib/time-zone';
import type { LayoutServerLoad } from './$types';

export const load: LayoutServerLoad = (event) => {
	const user = event.locals.user
		? { id: event.locals.user.id, name: event.locals.user.name, avatarUrl: avatarSrc(event.locals.user.avatar_url) }
		: null;
	const timeZone = parseTimeZone(event.cookies.get(TIME_ZONE_COOKIE)) ?? systemTimeZone();
	// The org switcher in the header (api-v1 v1.24 `/v1/me` orgs): names only.
	const orgs = event.locals.user ? event.locals.orgs.map((org) => ({ id: org.id, name: org.name })) : [];
	// "What's new" in the account menu (spec-update-awareness §2.4): the version, and a dot while the newest release is
	// unseen. Read here so the dot is in the server HTML. /whats-new itself counts as seen; its page stores the cookie.
	// Reading url.pathname is deliberate: it makes this load re-run on every navigation, so leaving /whats-new
	// (whose page has just stored the cookie) clears the dot without a reload.
	const version = latestVersion();
	const unseen = event.url.pathname !== '/whats-new' && hasUnseenRelease(version, event.cookies.get(SEEN_VERSION_COOKIE));
	const whatsNew = event.locals.user ? { version, unseen } : null;
	return present(event, { user, orgs, theme: parseTheme(event.cookies.get(THEME_COOKIE)), timeZone, whatsNew });
};
