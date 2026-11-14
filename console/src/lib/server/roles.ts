import type { Org, OrgRole, Role } from '../api-types';

const RANK: Record<Role, number> = { viewer: 1, admin: 2, owner: 3 };
const ORG_RANK: Record<OrgRole, number> = { member: 1, admin: 2, owner: 3 };

const ORG_ROLES = new Set<OrgRole>(['owner', 'admin', 'member']);

export function isOrgRole(value: unknown): value is OrgRole {
	return typeof value === 'string' && ORG_ROLES.has(value as OrgRole);
}

export function isRole(value: unknown): value is Role {
	return value === 'owner' || value === 'admin' || value === 'viewer';
}

export function roleAtLeast(role: Role | null | undefined, minimum: Role): boolean {
	if (!role || !isRole(role)) return false;
	return RANK[role] >= RANK[minimum];
}

/** Hide a control unless every source agrees the caller has it. */
export function uiRole(localsRole: Role | null, payloadRole: Role | null): Role | null {
	if (localsRole && payloadRole) return RANK[localsRole] <= RANK[payloadRole] ? localsRole : payloadRole;
	return payloadRole ?? localsRole;
}

/** Hide an org control unless the session and the org read agree: the lower of the two roles. */
export function uiOrgRole(localsRole: OrgRole | null, payloadRole: OrgRole | null): OrgRole | null {
	if (localsRole && payloadRole) return ORG_RANK[localsRole] <= ORG_RANK[payloadRole] ? localsRole : payloadRole;
	return payloadRole ?? localsRole;
}

/** The caller's role in an org, from the session's `/v1/me` orgs. Null when not a member or the id is null. */
export function orgRoleOf(orgs: readonly Org[], orgId: string | null | undefined): OrgRole | null {
	if (!orgId) return null;
	return orgs.find((org) => org.id === orgId)?.role ?? null;
}

export function orgRoleAtLeast(role: OrgRole | null | undefined, minimum: OrgRole): boolean {
	if (!role || !Object.hasOwn(ORG_RANK, role)) return false;
	return ORG_RANK[role] >= ORG_RANK[minimum];
}

/** Org owner or admin: manages the org's hosts, join tokens and host access (api-v1 v1.24 item 2). */
export function isOrgAdmin(orgs: readonly Org[], orgId: string | null | undefined): boolean {
	return orgRoleAtLeast(orgRoleOf(orgs, orgId), 'admin');
}
