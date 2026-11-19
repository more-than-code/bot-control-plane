import { avatarSrc } from '$lib/avatar';
import { changelogEntries, latestVersion } from '$lib/server/changelog';
import { importSeenVersion } from '$lib/server/notifications';
import { present } from '$lib/server/redact';
import { parseTheme, THEME_COOKIE } from '$lib/theme';
import { parseTimeZone, systemTimeZone, TIME_ZONE_COOKIE } from '$lib/time-zone';
import type { LayoutServerLoad } from './$types';

export const load: LayoutServerLoad = async (event) => {
	const user = event.locals.user
		? { id: event.locals.user.id, name: event.locals.user.name, avatarUrl: avatarSrc(event.locals.user.avatar_url) }
		: null;
	const timeZone = parseTimeZone(event.cookies.get(TIME_ZONE_COOKIE)) ?? systemTimeZone();
	// The header's Org ▾ / Project ▾ (spec-header-org-project-bell §3.1), from the session's `/v1/me`: no extra call.
	const orgs = event.locals.user ? event.locals.orgs.map((org) => ({ id: org.id, name: org.name, role: org.role })) : [];
	const projects = event.locals.user
		? event.locals.projects.map((project) => ({
				id: project.id,
				name: project.name,
				org_id: project.org_id,
				org_name: project.org_name ?? null
			}))
		: [];
	// "What's new" in the account menu is a plain link with the version; unread releases are on the bell.
	const version = latestVersion();
	const whatsNew = event.locals.user ? { version } : null;
	// The retired seen-version cookie becomes a read mark once (§4 option B).
	if (event.locals.user) await importSeenVersion(event, changelogEntries().map((entry) => entry.version));
	return present(event, { user, orgs, projects, theme: parseTheme(event.cookies.get(THEME_COOKIE)), timeZone, whatsNew });
};
