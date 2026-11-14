import { avatarSrc } from '$lib/avatar';
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
	return present(event, { user, orgs, theme: parseTheme(event.cookies.get(THEME_COOKIE)), timeZone });
};
