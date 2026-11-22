import { error, fail, isHttpError, isRedirect, type RequestEvent } from '@sveltejs/kit';
import { ApiError, apiFetch } from '$lib/server/api';
import { formText } from '$lib/server/form';
import { requireUser } from '$lib/server/guard';
import { present } from '$lib/server/redact';
import { isUuid } from '$lib/server/validate';
import { confirmCode, ghcrRepository, parseImageVersions, versionsReasonText, type ImageVersionsList } from '$lib/image-versions';

/** Image versions (api-v1 v1.45): the dialog's list and the guarded delete. The browser never calls the API. */

const UNEXPECTED = 'The API returned an unexpected response.';
const DIGEST = /^sha256:[0-9a-f]{64}$/;

function projectIdOf(params: { id?: string }): string {
	if (!params.id || !isUuid(params.id)) error(404, 'Not found');
	return params.id;
}

/** The API's message, or the reason of a registry_unavailable in the person's words. */
function versionsError(err: unknown): { status: number; message: string } {
	if (!(err instanceof ApiError)) {
		console.error('bot api request failed');
		return { status: 503, message: 'The API is unavailable.' };
	}
	const status = err.status >= 400 && err.status <= 599 ? err.status : 503;
	if (err.code === 'registry_unavailable') {
		const reason = err.payload != null && typeof err.payload === 'object' ? (err.payload as Record<string, unknown>).reason : undefined;
		return { status, message: versionsReasonText(reason) };
	}
	if (err.status === 404 && err.code !== 'not_found') return { status, message: 'This API does not list image versions yet.' };
	return { status, message: err.msg };
}

/** The versions of one ghcr.io repository, for the dialog. */
export async function catalogImageVersions(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const image = ghcrRepository(formText(await event.request.formData(), 'image'));
	const answer = (list: ImageVersionsList | null, message: string | null) => present(event, { scope: 'catalog' as const, imageVersions: { list, message } });
	if (image == null) return fail(400, answer(null, 'Image versions are only listed for ghcr.io images.'));
	try {
		const list = parseImageVersions(await apiFetch(event, `/v1/projects/${id}/image-versions?image=${encodeURIComponent(image)}`));
		if (!list) return fail(502, answer(null, UNEXPECTED));
		return answer(list, null);
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		const { status, message } = versionsError(err);
		return fail(status, answer(null, message));
	}
}

/** Delete one version by id, with the typed confirmation (the API checks it again, and the guards). */
export async function catalogImageVersionDelete(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const form = await event.request.formData();
	const image = ghcrRepository(formText(form, 'image'));
	const versionId = formText(form, 'version_id');
	const digest = formText(form, 'digest');
	const confirm = formText(form, 'confirm').trim();
	const answer = (message: string | null, deleted: string | null = null) =>
		present(event, { scope: 'catalog' as const, imageVersionDelete: { message, deleted } });
	if (image == null || !/^[1-9][0-9]{0,15}$/.test(versionId) || !DIGEST.test(digest)) return fail(400, answer('Choose a version to delete.'));
	if (confirm.toLowerCase() !== confirmCode(digest)) return fail(400, answer('Type the first 12 characters of the digest to confirm.'));
	try {
		await apiFetch(event, `/v1/projects/${id}/image-versions/${versionId}?image=${encodeURIComponent(image)}`, {
			method: 'DELETE',
			body: JSON.stringify({ digest, confirm })
		});
		return answer(null, digest);
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		const { status, message } = versionsError(err);
		return fail(status, answer(message));
	}
}
