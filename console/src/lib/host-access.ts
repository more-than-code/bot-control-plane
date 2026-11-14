import type { AccessLevel, HostAccessSummary, HostPrograms } from './api-types';

/** A project the access form can name: id, display name and the slug the host's runner.toml lists. */
export type AccessProject = { id: string; name: string; slug: string };

export const ACCESS_LEVEL_TEXT: Record<AccessLevel, string> = {
	metrics: 'Status, charts and processes.',
	deploy: "Also runs the project's catalog on this host."
};

export const ACCESS_HELP =
	'Projects use a host through the organization. Org owners and admins grant each project metrics or deploy access here.';

/** Org projects that do not have access to the host yet, by name. */
export function grantableProjects(projects: readonly AccessProject[], rows: readonly Pick<HostAccessSummary, 'project_id'>[]): AccessProject[] {
	const granted = new Set(rows.map((row) => row.project_id));
	return projects.filter((project) => !granted.has(project.id)).sort((a, b) => a.name.localeCompare(b.name));
}

/**
 * True when the host reported its allowlist and a project with deploy access is not on it: runs there would be
 * refused by the host. Unknown (no list, an older agent) is false, so nothing is claimed.
 */
export function missingFromAllowlist(programs: Pick<HostPrograms, 'projects'> | null, level: AccessLevel, slug: string | null): boolean {
	if (level !== 'deploy' || slug == null || programs?.projects == null) return false;
	return !programs.projects.includes(slug);
}

/** "Daikou (deploy), Defect Drainer (metrics)" for the org Hosts table. */
export function accessSummary(rows: readonly HostAccessSummary[] | null): string {
	if (rows == null) return 'n/a';
	if (rows.length === 0) return 'No projects';
	return [...rows]
		.sort((a, b) => a.project_name.localeCompare(b.project_name))
		.map((row) => `${row.project_name} (${row.level})`)
		.join(', ');
}

/** A failed access write, as the action returns it. */
export type AccessActionData = {
	scope: string;
	message: string;
	/** The host the failure belongs to, so only that host's editor shows it. */
	accessHostId: string;
	/**
	 * A remove refused with 409 because the project still has services on the host. The editor then offers
	 * the second confirm, which posts `reassign=1` (`?reassign=org`: the services move to the organization).
	 */
	accessConflict: { hostId: string; projectId: string } | null;
};

/** The access failure in an action result, or null. */
export function accessFailureOf(form: unknown): AccessActionData | null {
	if (form == null || typeof form !== 'object' || !('accessHostId' in form)) return null;
	return form as AccessActionData;
}
