import { fail, redirect } from '@sveltejs/kit';
import { actionFailure, apiFetch, apiList, loadFailure } from '$lib/server/api';
import { formText } from '$lib/server/form';
import { requireUser } from '$lib/server/guard';
import { present } from '$lib/server/redact';
import { isOrgAdmin } from '$lib/server/roles';
import { parseOrg, parseProject } from '$lib/server/shape';
import { cleanName } from '$lib/server/validate';
import type { Actions, PageServerLoad } from './$types';

/** Orgs where the caller may create a project (owner or admin), for the New project dialog. */
function creatableOrgs(locals: App.Locals) {
	return locals.orgs.filter((org) => isOrgAdmin(locals.orgs, org.id)).map((org) => ({ id: org.id, name: org.name }));
}

export const load: PageServerLoad = async (event) => {
	requireUser(event);
	const orgs = creatableOrgs(event.locals);
	const orgNames = Object.fromEntries(event.locals.orgs.map((org) => [org.id, org.name]));
	// In an org but admin of none: no New project, only who to ask.
	const askOrgAdmin = orgs.length === 0 && event.locals.orgs.length > 0;
	try {
		const list = await apiList(event, '/v1/projects', parseProject);
		return present(event, { projects: list.items, omitted: list.omitted, truncated: list.truncated, message: null, orgs, orgNames, askOrgAdmin });
	} catch (err) {
		const failure = loadFailure(err);
		return present(event, { projects: [], omitted: 0, truncated: false, message: failure.message, orgs, orgNames, askOrgAdmin });
	}
};

export const actions: Actions = {
	create: async (event) => {
		requireUser(event);
		const form = await event.request.formData();
		const name = cleanName(formText(form, 'name'));
		const orgId = formText(form, 'org_id');
		if (!name) return fail(400, present(event, { message: 'Enter a project name (1–80 characters).' }));
		const creatable = creatableOrgs(event.locals);
		if (orgId !== '' && !creatable.some((org) => org.id === orgId)) return fail(400, present(event, { message: 'Choose an organization.' }));
		if (orgId === '' && creatable.length > 1) return fail(400, present(event, { message: 'Choose an organization.' }));
		// api-v1 v1.24: a project is created in an org. Without one, POST /v1/projects uses the caller's only org.
		const path = orgId ? `/v1/orgs/${orgId}/projects` : '/v1/projects';
		try {
			const project = parseProject(await apiFetch(event, path, { method: 'POST', body: JSON.stringify({ name }) }));
			if (!project) return fail(502, present(event, { message: 'The API returned an unexpected response.' }));
			redirect(303, `/projects/${project.id}`);
		} catch (err) {
			return actionFailure(event, err);
		}
	},
	// spec-new-org: any signed-in user may create an org (POST /v1/orgs) and becomes its owner.
	createOrg: async (event) => {
		requireUser(event);
		const form = await event.request.formData();
		const name = cleanName(formText(form, 'name'));
		if (!name) return fail(400, present(event, { message: 'Enter an organization name (1–80 characters).' }));
		try {
			const org = parseOrg(await apiFetch(event, '/v1/orgs', { method: 'POST', body: JSON.stringify({ name }) }));
			if (!org) return fail(502, present(event, { message: 'The API returned an unexpected response.' }));
			redirect(303, `/orgs/${org.id}?tab=projects`);
		} catch (err) {
			return actionFailure(event, err);
		}
	}
};
