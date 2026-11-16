/**
 * The signed-in user's open requests (api-v1 v1.33 item 8, spec-agent-usability-1 F2): value, file and connect
 * requests an agent asked for and that still wait. The API sends a handle (the stored SHA-256 of the id), never
 * the id, and no reason, value or URL. Each row links to its fill page by `h-<handle>`.
 */
import { boundedText, isRequestHandle, UUID } from '$lib/request-shape';

export const OPEN_REQUEST_TYPES = ['value', 'file', 'connect'] as const;
export type OpenRequestType = (typeof OPEN_REQUEST_TYPES)[number];

export type OpenRequest = {
	type: OpenRequestType;
	handle: string;
	project_id: string;
	project_name: string;
	/** `set.KEY`, the file name, or `ghcr.io`. Plain text. */
	subject: string;
	expires_at: string;
	created_at: string;
};

/** The API sends at most 60. */
export const OPEN_REQUESTS_MAX = 60;

export const OPEN_REQUEST_TEXT = {
	empty: 'No open requests. When an AI agent asks you for a value, a file or a GitHub connection, it shows here until you answer it or it expires.',
	unexpected: 'The API returned an unexpected response.'
} as const;

const TYPE_LABELS: Record<OpenRequestType, string> = { value: 'Env value', file: 'File', connect: 'Connect GitHub' };

export function openRequestTypeLabel(type: OpenRequestType): string {
	return TYPE_LABELS[type];
}

/** The fill page of a request, by its handle. */
export function openRequestHref(request: Pick<OpenRequest, 'type' | 'handle'>): string {
	const base = request.type === 'value' ? '/fill' : request.type === 'file' ? '/fill-file' : '/connect';
	return `${base}/h-${request.handle}`;
}

function parseItem(value: unknown): OpenRequest | null {
	if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
	const record = value as Record<string, unknown>;
	const type = record.type;
	if (typeof type !== 'string' || !(OPEN_REQUEST_TYPES as readonly string[]).includes(type)) return null;
	const handle = record.handle;
	if (!isRequestHandle(handle)) return null;
	const project_id = boundedText(record, 'project_id', 36);
	const project_name = boundedText(record, 'project_name', 200);
	const subject = boundedText(record, 'subject', 200);
	const expires_at = boundedText(record, 'expires_at', 64);
	const created_at = boundedText(record, 'created_at', 64);
	if (!project_id || !UUID.test(project_id) || project_name == null || !subject || !expires_at || !created_at) return null;
	if (Number.isNaN(Date.parse(expires_at)) || Number.isNaN(Date.parse(created_at))) return null;
	return { type: type as OpenRequestType, handle, project_id, project_name, subject, expires_at, created_at };
}

/** `GET /v1/me/requests`. A malformed answer is null; a malformed row is dropped. Newest first, at most 60. */
export function parseOpenRequests(payload: unknown): OpenRequest[] | null {
	if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return null;
	const items = (payload as Record<string, unknown>).items;
	if (!Array.isArray(items)) return null;
	const out: OpenRequest[] = [];
	const seen = new Set<string>();
	for (const item of items.slice(0, OPEN_REQUESTS_MAX)) {
		const row = parseItem(item);
		if (!row || seen.has(`${row.type}:${row.handle}`)) continue;
		seen.add(`${row.type}:${row.handle}`);
		out.push(row);
	}
	return out.sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
}
