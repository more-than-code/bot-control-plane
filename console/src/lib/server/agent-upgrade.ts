import { isHttpError, isRedirect, type RequestEvent } from '@sveltejs/kit';
import type { AgentUpgrade } from '../api-types';
import { ApiError, apiFetch, loadFailure } from './api';
import { isRecord, parseAgentUpgrade } from './shape';

/**
 * Agent upgrades (api-v1 v1.48): start one and read the history. The API waits up to 130 s for the host to
 * accept the task, so the start passes a longer timeout than the default 35 s.
 */
export const AGENT_UPGRADE_WAIT_MS = 140_000;

export type UpgradeStart = { ok: true; upgradeId: string } | { ok: false; status: number; message: string };

/** POST /v1/hosts/{id}/agent-upgrade with the version the API named as latest. */
export async function startAgentUpgrade(event: RequestEvent, hostId: string, version: string): Promise<UpgradeStart> {
	try {
		const payload = await apiFetch(
			event,
			`/v1/hosts/${hostId}/agent-upgrade`,
			{ method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ version }) },
			AGENT_UPGRADE_WAIT_MS
		);
		const id = isRecord(payload) && typeof payload.upgrade_id === 'string' ? payload.upgrade_id : null;
		if (!id) return { ok: false, status: 502, message: 'The API returned an unexpected response.' };
		return { ok: true, upgradeId: id };
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		if (err instanceof ApiError) return { ok: false, status: err.status, message: err.msg };
		return { ok: false, status: 503, message: loadFailure(err).message };
	}
}

/** GET /v1/hosts/{id}/agent-upgrades: newest first; rows that do not parse are left out. */
export async function loadAgentUpgrades(event: RequestEvent, hostId: string): Promise<{ items: AgentUpgrade[]; message: string | null }> {
	try {
		const payload = await apiFetch(event, `/v1/hosts/${hostId}/agent-upgrades`);
		if (!isRecord(payload) || !Array.isArray(payload.items)) return { items: [], message: 'The API returned an unexpected response.' };
		const items = payload.items.slice(0, 20).map(parseAgentUpgrade).filter((row): row is AgentUpgrade => row != null);
		return { items, message: null };
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return { items: [], message: loadFailure(err).message };
	}
}
