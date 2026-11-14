import type { CreatedJoin, OrgRole } from './api-types';

/** Org page tabs (spec-organizations §4.3). Hosts, Join tokens and Settings are for org owners and admins. */
export type OrgScope = 'projects' | 'hosts' | 'members' | 'tokens' | 'settings';

/** Unknown or not-allowed tab values fall back to Projects. */
export function orgTabOf(value: string | null, canAdmin: boolean): OrgScope {
	if (value === 'members') return 'members';
	if (value === 'hosts' || value === 'tokens' || value === 'settings') return canAdmin ? value : 'projects';
	return 'projects';
}

/** Roles an org admin may give: the owner changes only through a transfer. */
export const ORG_MEMBER_ROLES = ['admin', 'member'] as const satisfies readonly OrgRole[];

export function cleanOrgMemberRole(value: string): 'admin' | 'member' | null {
	return value === 'admin' || value === 'member' ? value : null;
}

/** An org form action's result. `field` names the dialog whose message it is. */
export type OrgActionData = {
	scope?: OrgScope;
	message?: string | null;
	field?: 'createToken' | 'addMember' | 'createProject';
	/** The created join token, shown once in the Add a host dialog. */
	created?: CreatedJoin;
	/** The slug of the project the created token grants access to, for the agent instructions. */
	createdSlug?: string | null;
};
