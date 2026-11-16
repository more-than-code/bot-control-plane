import type { CreatedJoin, JoinGrant, JoinToken, OrgRole } from './api-types';
import type { DomainPlan } from './edge-routes';

/** Org page tabs (spec-organizations §4.3). Hosts, Join tokens and Settings are for org owners and admins. */
export type OrgScope = 'projects' | 'hosts' | 'members' | 'tokens' | 'settings';

/** The focusable wrapper around an org section's error Notice: a failed confirm closes and focuses it. */
export const ORG_ERROR_ID = 'org-section-error';

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
	field?: 'createToken' | 'addMember' | 'createProject' | 'addDomain' | 'domain' | 'assignments';
	/** The domain a `domain` or `assignments` message belongs to (api-v1 v1.35). */
	domainId?: string | null;
	/** A domain's Delete plan (202), shown in its row with Confirm. */
	domainPlan?: DomainPlan;
	/** The created join token, shown once in the Add a host dialog. */
	created?: CreatedJoin;
	/** The slug of the project the created token grants access to, for the agent instructions. */
	createdSlug?: string | null;
	/** The grant the created token carries, shown with it. Null when it names no project. */
	createdGrant?: JoinGrant | null;
};

export type JoinTokenState = 'revoked' | 'used' | 'expired' | 'active';

/**
 * A join token's state on the Join tokens tab: revoked, then used, then expired, then active. An expiry that is
 * missing or cannot be parsed counts as expired. Only an active token can be revoked.
 */
export function joinTokenState(token: Pick<JoinToken, 'revoked_at' | 'used_at' | 'expires_at'>, now: number): JoinTokenState {
	if (token.revoked_at) return 'revoked';
	if (token.used_at) return 'used';
	const expires = token.expires_at ? Date.parse(token.expires_at) : Number.NaN;
	if (Number.isNaN(expires) || expires <= now) return 'expired';
	return 'active';
}

/** A created token's grant for its dialog, as "<project> (<level>)". Null when it names no project. */
export function joinGrantText(grant: JoinGrant | null, projectNames: Readonly<Record<string, string>>): string | null {
	if (!grant) return null;
	return `${projectNames[grant.project_id] ?? 'a project'} (${grant.level})`;
}
