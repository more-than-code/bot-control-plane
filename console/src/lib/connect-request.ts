/**
 * The Connect GitHub page started from chat (api-v1 v1.31, spec-settings-automatic §3.6). An agent asked; the person
 * who asked continues to GitHub from here or declines. No token ever reaches this page. The id rule, UUID and text
 * bounds are the shared request shapes ($lib/request-shape); the states and views are this page's own, because a
 * connect request is done as `connected` and each end state reads differently here.
 */
import { boundedText, isRequestId, UUID } from '$lib/request-shape';


const STATES = ['pending', 'connected', 'declined', 'expired'] as const;
const SOURCES = ['manual', 'github'] as const;

export type ConnectState = (typeof STATES)[number];

/** The project's ghcr.io login when the page loaded: who it is, never a token. */
export type ConnectLogin = { source: (typeof SOURCES)[number]; username: string };

export type ConnectRequest = {
	request_id: string;
	project_id: string;
	project_name: string;
	state: ConnectState;
	expires_at: string;
	/** The agent's note. Plain text only: render it as text, never as HTML or a link. */
	reason: string;
	current_login: ConnectLogin | null;
};

/** What the page shows: the buttons, or one outcome with none. `error` is a load failure: its message only. */
export type ConnectView = 'form' | 'connected' | 'declined' | 'expired' | 'missing' | 'invalid' | 'error';

export const CONNECT_TEXT = {
	declined: 'This request was declined. You can close this tab.',
	declinedNow: 'Declined. The agent will see that you declined. You can close this tab.',
	expired: 'This request has expired. Ask the agent for a new one.',
	missing:
		'This request was not found. It belongs to the account that asked for it, so sign in as that person. You must also still be an owner or admin of the project.',
	invalid: 'This link is not valid. Check that you copied all of it.',
	off: 'GitHub is not configured on this server.',
	connectedEnded: 'GitHub was connected, but this request had already ended. The login is saved.',
	unexpected: 'The API returned an unexpected response.',
	safety:
		'Continue opens github.com, where you approve read access to packages (read:packages). The token goes straight to Deploybot and is stored encrypted for this project. The agent never sees it.'
} as const;

/** Connect requests use the shared request id rule. */
export const isConnectRequestId = isRequestId;

function loginOf(value: unknown): ConnectLogin | null | undefined {
	if (value === null) return null;
	if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined;
	const record = value as Record<string, unknown>;
	const source = boundedText(record, 'source', 16);
	const username = boundedText(record, 'username', 256);
	if (!source || !(SOURCES as readonly string[]).includes(source) || username == null) return undefined;
	return { source: source as ConnectLogin['source'], username };
}

/** `GET /v1/registry-connect-requests/{id}/page`. Anything unexpected is null (fail closed). */
export function parseConnectRequest(payload: unknown, id: string): ConnectRequest | null {
	if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return null;
	const record = payload as Record<string, unknown>;
	const project_id = boundedText(record, 'project_id', 36);
	const project_name = boundedText(record, 'project_name', 200);
	const state = boundedText(record, 'state', 16);
	const expires_at = boundedText(record, 'expires_at', 64);
	const reason = boundedText(record, 'reason', 1000);
	const current_login = loginOf(record.current_login);
	if (record.request_id !== id || record.registry !== 'ghcr.io' || !project_id || !UUID.test(project_id) || project_name == null) return null;
	if (!expires_at || reason == null || current_login === undefined) return null;
	if (!state || !(STATES as readonly string[]).includes(state)) return null;
	return { request_id: id, project_id, project_name, state: state as ConnectState, expires_at, reason, current_login };
}

/** The API's message when Connect GitHub is not set up (api-v1 v1.29 item 1); any other `unavailable` is transient. */
export const GITHUB_OFF_MESSAGE = 'GitHub is not configured';

/** The view for a loaded request. */
export function connectView(state: ConnectState): ConnectView {
	return state === 'pending' ? 'form' : state;
}

/** The line under the facts when the project already has a ghcr.io login, else null. */
export function replaceNote(login: ConnectLogin | null): string | null {
	if (!login) return null;
	if (login.source === 'github') return `This replaces the current ghcr.io login (connected as ${login.username}).`;
	return 'This replaces the current ghcr.io login (a hand-typed login).';
}

/** The connected outcome, naming the GitHub login when the project's login is the GitHub one. */
export function connectedText(login: ConnectLogin | null): string {
	if (login?.source === 'github' && login.username !== '') return `Connected to GitHub as ${login.username}. You can close this tab.`;
	return 'Connected to GitHub. You can close this tab.';
}
