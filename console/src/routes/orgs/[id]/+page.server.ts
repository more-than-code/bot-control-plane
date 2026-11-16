import { error, fail, redirect } from '@sveltejs/kit';
import type { Host, JoinToken, OrgDomain, OrgMember, Project } from '$lib/api-types';
import { cleanOrgMemberRole, orgTabOf, type OrgScope } from '$lib/org-view';
import { buildJoinTokenBody, cleanAccessLevel, removeAccessAction, revokeCredentialAction, setAccessAction, type JoinGrantBody } from '$lib/server/access-actions';
import { addDomain, confirmDomainDelete, deleteDomain, setDomainAssignments, verifyDomain } from '$lib/server/edge-actions';
import { parseDomainList } from '$lib/server/edge-shape';
import { actionFailure, apiFetch, apiList, emptyList, loadFailure, loadList, type ListLoad } from '$lib/server/api';
import { formText } from '$lib/server/form';
import { requireUser } from '$lib/server/guard';
import { present } from '$lib/server/redact';
import { orgRoleAtLeast, orgRoleOf, uiOrgRole } from '$lib/server/roles';
import { parseCreatedJoin, parseHost, parseJoinToken, parseOrg, parseOrgMember, parseProject } from '$lib/server/shape';
import { cleanEmail, cleanName, isUuid } from '$lib/server/validate';
import { backTo } from '$lib/url';
import type { Actions, PageServerLoad, RequestEvent } from './$types';

function orgIdOf(params: { id?: string }): string {
	if (!params.id || !isUuid(params.id)) error(404, 'Not found');
	return params.id;
}

function invalid(event: RequestEvent, scope: OrgScope, message: string, field?: 'createToken' | 'addMember' | 'createProject') {
	return fail(400, present(event, field ? { scope, message, field } : { scope, message }));
}

const empty = {
	org: null,
	message: null as string | null,
	tab: 'projects' as OrgScope,
	canAdmin: false,
	canOwner: false,
	userId: null as string | null,
	projects: emptyList<Project>(),
	hosts: emptyList<Host>(),
	members: emptyList<OrgMember>(),
	tokens: emptyList<JoinToken>(),
	domains: emptyList<OrgDomain>()
};

/** The org's domains (api-v1 v1.35 item 1), for Settings. A failure stays in the Domains card. */
async function loadDomains(event: RequestEvent, orgId: string): Promise<ListLoad<OrgDomain>> {
	try {
		const list = parseDomainList(await apiFetch(event, `/v1/orgs/${orgId}/domains`));
		if (!list) return { ...emptyList<OrgDomain>(), message: 'The API returned an unexpected response.' };
		return { items: list.items, omitted: list.omitted, truncated: false, message: null };
	} catch (err) {
		return { ...emptyList<OrgDomain>(), message: loadFailure(err).message };
	}
}

export const load: PageServerLoad = async (event) => {
	requireUser(event);
	const id = orgIdOf(event.params);
	const userId = event.locals.user?.id ?? null;
	let org;
	try {
		org = parseOrg(await apiFetch(event, `/v1/orgs/${id}`));
	} catch (err) {
		return present(event, { ...empty, userId, message: loadFailure(err).message });
	}
	if (!org) return present(event, { ...empty, userId, message: 'The API returned an unexpected response.' });
	// Show a control only when the session and the org read agree on the role.
	const role = uiOrgRole(orgRoleOf(event.locals.orgs, org.id), org.role);
	const canAdmin = orgRoleAtLeast(role, 'admin');
	const canOwner = role === 'owner';
	const tab = orgTabOf(event.url.searchParams.get('tab'), canAdmin);
	// The Add a host dialog polls this key while it waits for the new host.
	if (tab === 'hosts') event.depends('org:enrol');

	const [projects, hosts, members, tokens, domains] = await Promise.all([
		tab === 'projects' || tab === 'hosts' || tab === 'tokens' || tab === 'settings' ? loadList(event, `/v1/orgs/${id}/projects`, parseProject) : emptyList<Project>(),
		tab === 'hosts' ? loadList(event, `/v1/orgs/${id}/hosts`, parseHost) : emptyList<Host>(),
		tab === 'members' ? loadList(event, `/v1/orgs/${id}/members`, parseOrgMember) : emptyList<OrgMember>(),
		(tab === 'tokens' || tab === 'hosts') && canAdmin ? loadList(event, `/v1/orgs/${id}/join-tokens`, parseJoinToken) : emptyList<JoinToken>(),
		tab === 'settings' && canAdmin ? loadDomains(event, id) : emptyList<OrgDomain>()
	]);
	return present(event, { org, message: null, tab, canAdmin, canOwner, userId, projects, hosts, members, tokens, domains });
};

/** The slug of an org project, for the created token's agent instructions. Null when it cannot be read. */
async function projectSlug(event: RequestEvent, orgId: string, projectId: string): Promise<string | null> {
	try {
		const list = await apiList(event, `/v1/orgs/${orgId}/projects`, parseProject);
		return list.items.find((project) => project.id === projectId)?.slug ?? null;
	} catch {
		return null;
	}
}

export const actions: Actions = {
	setAccess: setAccessAction('hosts'),
	removeAccess: removeAccessAction('hosts'),
	revokeCredential: revokeCredentialAction('hosts'),
	addDomain,
	verifyDomain,
	setDomainAssignments,
	deleteDomain,
	confirmDomainDelete,

	createProject: async (event) => {
		requireUser(event);
		const id = orgIdOf(event.params);
		const name = cleanName(formText(await event.request.formData(), 'name'));
		if (!name) return invalid(event, 'projects', 'Enter a project name (1–80 characters).', 'createProject');
		try {
			const project = parseProject(await apiFetch(event, `/v1/orgs/${id}/projects`, { method: 'POST', body: JSON.stringify({ name }) }));
			if (!project) return fail(502, present(event, { scope: 'projects' as const, field: 'createProject' as const, message: 'The API returned an unexpected response.' }));
			redirect(303, `/projects/${project.id}`);
		} catch (err) {
			return actionFailure(event, err, 'projects', 'createProject');
		}
	},

	createToken: async (event) => {
		requireUser(event);
		const id = orgIdOf(event.params);
		const form = await event.request.formData();
		const grantProject = formText(form, 'grant_project_id');
		let grant: JoinGrantBody | null = null;
		if (grantProject !== '') {
			const level = cleanAccessLevel(formText(form, 'grant_level'));
			if (!isUuid(grantProject)) return invalid(event, 'hosts', 'Choose a project.', 'createToken');
			if (!level) return invalid(event, 'hosts', 'Choose metrics or deploy.', 'createToken');
			grant = { project_id: grantProject, level };
		}
		const built = buildJoinTokenBody(form, grant);
		if (!built.ok) return invalid(event, 'hosts', built.message, 'createToken');
		const body = built.body;
		try {
			const created = parseCreatedJoin(await apiFetch(event, `/v1/orgs/${id}/join-tokens`, { method: 'POST', body: JSON.stringify(body) }));
			if (!created) return fail(502, present(event, { scope: 'hosts' as const, field: 'createToken' as const, message: 'The API returned an unexpected response.' }));
			const createdSlug = body.grant ? await projectSlug(event, id, body.grant.project_id) : null;
			// Stay on this response so the plaintext token is shown once. A reload is a GET and drops it.
			return present(event, {
				scope: 'hosts' as const,
				field: 'createToken' as const,
				created,
				createdSlug,
				createdGrant: body.grant ?? null,
				message: created.server ? null : 'Server URL not shown: unexpected format'
			});
		} catch (err) {
			return actionFailure(event, err, 'hosts', 'createToken');
		}
	},

	revokeToken: async (event) => {
		requireUser(event);
		const id = orgIdOf(event.params);
		const tokenId = formText(await event.request.formData(), 'token_id');
		if (!isUuid(tokenId)) return invalid(event, 'tokens', 'That join token is not valid.');
		try {
			await apiFetch(event, `/v1/orgs/${id}/join-tokens/${tokenId}`, { method: 'DELETE' });
			redirect(303, backTo(event.url));
		} catch (err) {
			return actionFailure(event, err, 'tokens');
		}
	},

	addMember: async (event) => {
		requireUser(event);
		const id = orgIdOf(event.params);
		const form = await event.request.formData();
		const email = cleanEmail(formText(form, 'email'));
		const role = cleanOrgMemberRole(formText(form, 'role'));
		if (!email) return invalid(event, 'members', 'Enter an email address.', 'addMember');
		if (!role) return invalid(event, 'members', 'Choose admin or member.', 'addMember');
		try {
			await apiFetch(event, `/v1/orgs/${id}/members`, { method: 'POST', body: JSON.stringify({ email, role }) });
			redirect(303, backTo(event.url));
		} catch (err) {
			return actionFailure(event, err, 'members', 'addMember');
		}
	},

	changeRole: async (event) => {
		requireUser(event);
		const id = orgIdOf(event.params);
		const form = await event.request.formData();
		const userId = formText(form, 'user_id');
		const role = cleanOrgMemberRole(formText(form, 'role'));
		if (!isUuid(userId)) return invalid(event, 'members', 'That member is not valid.');
		if (!role) return invalid(event, 'members', 'Choose admin or member.');
		try {
			await apiFetch(event, `/v1/orgs/${id}/members/${userId}`, { method: 'PATCH', body: JSON.stringify({ role }) });
			redirect(303, backTo(event.url));
		} catch (err) {
			return actionFailure(event, err, 'members');
		}
	},

	removeMember: async (event) => {
		requireUser(event);
		const id = orgIdOf(event.params);
		const userId = formText(await event.request.formData(), 'user_id');
		if (!isUuid(userId)) return invalid(event, 'members', 'That member is not valid.');
		try {
			await apiFetch(event, `/v1/orgs/${id}/members/${userId}`, { method: 'DELETE' });
			// Leaving the org yourself ends access to its page.
			if (userId === event.locals.user?.id) redirect(303, '/projects');
			redirect(303, backTo(event.url));
		} catch (err) {
			return actionFailure(event, err, 'members');
		}
	},

	transfer: async (event) => {
		requireUser(event);
		const id = orgIdOf(event.params);
		const userId = formText(await event.request.formData(), 'user_id');
		if (!isUuid(userId)) return invalid(event, 'members', 'Choose a member.');
		try {
			await apiFetch(event, `/v1/orgs/${id}/transfer-ownership`, { method: 'POST', body: JSON.stringify({ user_id: userId }) });
			redirect(303, backTo(event.url));
		} catch (err) {
			return actionFailure(event, err, 'members');
		}
	},

	rename: async (event) => {
		requireUser(event);
		const id = orgIdOf(event.params);
		const name = cleanName(formText(await event.request.formData(), 'name'));
		if (!name) return invalid(event, 'settings', 'Enter an organization name (1–80 characters).');
		try {
			await apiFetch(event, `/v1/orgs/${id}`, { method: 'PATCH', body: JSON.stringify({ name }) });
			redirect(303, backTo(event.url));
		} catch (err) {
			return actionFailure(event, err, 'settings');
		}
	},

	deleteOrg: async (event) => {
		requireUser(event);
		const id = orgIdOf(event.params);
		const typed = formText(await event.request.formData(), 'confirm');
		try {
			// The server check is the authority: the typed name must match the org's current name.
			const current = parseOrg(await apiFetch(event, `/v1/orgs/${id}`));
			if (!current || typed !== current.name) return invalid(event, 'settings', 'Type the organization name exactly to delete it.');
			await apiFetch(event, `/v1/orgs/${id}`, { method: 'DELETE' });
			redirect(303, '/projects');
		} catch (err) {
			return actionFailure(event, err, 'settings');
		}
	}
};
