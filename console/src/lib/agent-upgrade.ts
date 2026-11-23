import type { AgentUpgrade, AgentUpgradeState, Host } from './api-types';

/**
 * Agent upgrades from the console (api-v1 v1.48): what the "Update available" badge offers, the words for a
 * row's state and reason, and the poll that follows a started upgrade.
 */

/** How often the badge re-reads the host's upgrades while one runs, and for how long at most. */
export const UPGRADE_POLL_MS = 5_000;
export const UPGRADE_POLL_LIMIT_MS = 16 * 60_000;

export type UpgradeOffer =
	/** No newer release, or a revoked host: nothing to offer. */
	| 'none'
	/** The caller is not an org owner or admin: the tooltip only. */
	| 'view'
	/** The host's runner.toml does not allow it: the tooltip says how to opt in. */
	| 'opt-in'
	/** An upgrade is running: no button, a running note. */
	| 'running'
	/** The Upgrade button. */
	| 'upgrade';

export function upgradeOpen(state: AgentUpgradeState | null | undefined): boolean {
	return state === 'started' || state === 'running';
}

/** What the badge offers for this host and caller. `canUpgrade` is org owner or admin of the host's org. */
export function upgradeOffer(
	host: Partial<Pick<Host, 'update_available' | 'latest_agent_version' | 'agent_upgrade_allowed' | 'agent_upgrade' | 'revoked_at'>>,
	canUpgrade: boolean
): UpgradeOffer {
	if (upgradeOpen(host.agent_upgrade?.state)) return 'running';
	if (!host.update_available || !host.latest_agent_version || host.revoked_at) return 'none';
	if (!canUpgrade) return 'view';
	return host.agent_upgrade_allowed ? 'upgrade' : 'opt-in';
}

/** The badge tooltip (api-v1 v1.34, v1.48). */
export function agentUpdateTooltip(latest: string, offer: UpgradeOffer): string {
	const head = `bot-agent ${latest} is available.`;
	if (offer === 'upgrade') return `${head} Upgrade installs it; the host puts the old agent back by itself if the new one does not connect.`;
	if (offer === 'opt-in') return `${head} To upgrade from here, add agent_upgrade = true to the host's runner.toml (bot-agent 0.2.15 or later) and restart the agent.`;
	return `${head} An owner or admin of the organization can upgrade it.`;
}

const STATE_WORDS: Record<AgentUpgradeState, string> = {
	started: 'starting',
	running: 'running',
	succeeded: 'upgraded',
	rolled_back: 'rolled back',
	failed: 'failed',
	unknown: 'no result'
};

export function upgradeStateWord(state: AgentUpgradeState): string {
	return STATE_WORDS[state];
}

const REASON_WORDS: Record<string, string> = {
	download_failed: 'the release could not be downloaded',
	signature_invalid: 'the release signature did not check out',
	bundle_invalid: 'the release bundle was not valid',
	version_mismatch: 'the bundle held a different version',
	install_failed: 'the install did not finish',
	not_reconnected: 'the new agent did not connect',
	policy_denied: "the host's runner.toml does not allow it",
	busy: 'the host was busy with another change',
	not_newer: 'the host already runs this version or newer',
	bad_spec: 'the host refused the request',
	helper_unavailable: "the host's helper is not running",
	forbidden: "the host's agent does not allow it",
	unavailable: 'the host did not answer'
};

/** A reason token in plain words; an unknown token is shown as it is (it is a plain token). */
export function upgradeReasonText(reason: string | null): string | null {
	if (!reason) return null;
	return REASON_WORDS[reason] ?? reason.replaceAll('_', ' ');
}

/** One line for a row: "0.2.14 → 0.2.15: rolled back (the new agent did not connect)". */
export function upgradeLine(row: Pick<AgentUpgrade, 'from' | 'to' | 'state' | 'reason'>): string {
	const reason = upgradeReasonText(row.reason);
	const from = row.from || 'unknown';
	return `${from} → ${row.to}: ${upgradeStateWord(row.state)}${reason ? ` (${reason})` : ''}`;
}

/** The notice after an upgrade settles. */
export function upgradeOutcomeText(row: Pick<AgentUpgrade, 'to' | 'state' | 'reason'>): string {
	const reason = upgradeReasonText(row.reason);
	switch (row.state) {
		case 'succeeded':
			return `bot-agent ${row.to} is running.`;
		case 'rolled_back':
			return `The upgrade to ${row.to} was rolled back${reason ? `: ${reason}` : ''}. The previous agent is running.`;
		case 'failed':
			return `The upgrade to ${row.to} failed${reason ? `: ${reason}` : ''}. Nothing changed on the host.`;
		case 'unknown':
			return `The upgrade to ${row.to} did not report back. Check the host.`;
		default:
			return `Upgrading to ${row.to}…`;
	}
}

/**
 * One poll step: keep polling while the row is open and the limit has not passed. `row` is the newest row the
 * read returned (null when it failed).
 */
export function upgradePollNext(row: Pick<AgentUpgrade, 'state'> | null, startedMs: number, nowMs: number): boolean {
	if (nowMs - startedMs >= UPGRADE_POLL_LIMIT_MS) return false;
	return row == null || upgradeOpen(row.state);
}
