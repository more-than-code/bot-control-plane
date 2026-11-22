/**
 * The header's Org ▾ / Project ▾ parts (spec-header-org-project-bell §3.1). Pure, so the rules are tested
 * without rendering: which org and project are current, what each menu lists, and who sees which link.
 * The data is the session's `/v1/me` orgs and projects, as the root layout load passes them.
 */
import type { OrgRole } from './api-types';

export type NavOrg = { id: string; name: string; role: OrgRole };
export type NavProject = { id: string; name: string; org_id: string | null; org_name: string | null };

export type NavLink = { id: string; name: string; href: string; current: boolean };

export type HeaderNav = {
	/**
	 * The current org. `menu` false: the caller is not in it (a project-only member), so it is plain text from
	 * the project's `org_name`. Null when there is none to show (no orgs, or an older API without `org_name`).
	 */
	org: { id: string; name: string; menu: boolean } | null;
	/** The caller's orgs, sorted by name. */
	orgs: NavLink[];
	/** "Organization settings" for owners and admins, "Organization" for members; null outside the caller's orgs. */
	orgPage: { label: 'Organization settings' | 'Organization'; href: string } | null;
	/** The trigger text: the open project's name, "Select a project" or "All projects". */
	projectLabel: string;
	/** The open project's id, or null. */
	projectId: string | null;
	/** The caller's projects in the current org, sorted by name. */
	projects: NavLink[];
	/** Org owners and admins may create a project in the current org (today's rule). */
	canCreateProject: boolean;
};

const byName = (a: { name: string }, b: { name: string }) => a.name.localeCompare(b.name);

function isAdmin(role: OrgRole): boolean {
	return role === 'owner' || role === 'admin';
}

/** The id after `/<prefix>/`, or null. */
function segment(path: string, prefix: string): string | null {
	if (!path.startsWith(`/${prefix}/`)) return null;
	const id = path.slice(prefix.length + 2).split('/')[0];
	return id === '' ? null : id;
}

/**
 * `pageOrgId` is the org a page names when the path does not (the host page's `org`), or null.
 */
export function headerNav(input: {
	path: string;
	orgs: readonly NavOrg[];
	projects: readonly NavProject[];
	pageOrgId?: string | null;
}): HeaderNav {
	const orgs = [...input.orgs].sort(byName);
	const projectId = segment(input.path, 'projects');
	const project = projectId ? (input.projects.find((item) => item.id === projectId) ?? null) : null;
	const pathOrgId = segment(input.path, 'orgs');

	let orgId: string | null;
	if (project) orgId = project.org_id;
	else if (pathOrgId) orgId = pathOrgId;
	else if (input.pageOrgId) orgId = input.pageOrgId;
	else orgId = orgs[0]?.id ?? null;

	const member = orgId ? (orgs.find((org) => org.id === orgId) ?? null) : null;
	let org: HeaderNav['org'] = null;
	if (member) org = { id: member.id, name: member.name, menu: true };
	else if (project?.org_id && project.org_name) org = { id: project.org_id, name: project.org_name, menu: false };

	let projectLabel: string;
	if (project) projectLabel = project.name;
	else if (input.path === '/projects') projectLabel = 'All projects';
	else projectLabel = 'Select a project';

	const inOrg = orgId ? input.projects.filter((item) => item.org_id === orgId) : [...input.projects];
	return {
		org,
		orgs: orgs.map((item) => ({
			id: item.id,
			name: item.name,
			// Members land on the Projects tab; owners and admins on the org page (its default tab).
			href: isAdmin(item.role) ? `/orgs/${item.id}` : `/orgs/${item.id}?tab=projects`,
			current: item.id === member?.id
		})),
		orgPage: member
			? isAdmin(member.role)
				? { label: 'Organization settings', href: `/orgs/${member.id}?tab=settings` }
				: { label: 'Organization', href: `/orgs/${member.id}` }
			: null,
		projectLabel,
		projectId: project?.id ?? null,
		projects: inOrg.sort(byName).map((item) => ({
			id: item.id,
			name: item.name,
			href: `/projects/${item.id}`,
			current: item.id === project?.id
		})),
		canCreateProject: member != null && isAdmin(member.role)
	};
}

/**
 * The phone Project ▾ menu starts with the org items (the caller's orgs, New organization, the org page link,
 * All projects) only when the caller is in some org. New organization (spec-new-org, any signed-in user) is offered
 * wherever the Org menu or the phone org items render, and always on /projects; a project-only member with no org gets the project items alone.
 */
export function mobileOrgItems(nav: HeaderNav): boolean {
	return nav.orgs.length > 0;
}

/** The trigger's accessible names (§3.1). */
export function orgTriggerName(name: string): string {
	return `Organization: ${name}`;
}

export function projectTriggerName(label: string): string {
	return `Project: ${label}`;
}
