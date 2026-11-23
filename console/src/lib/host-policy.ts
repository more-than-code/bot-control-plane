/**
 * Host policy from the console (api-v1 v1.51): the part of a host's runner.toml that org owners and admins may
 * change once the host's root set `console_policy = true`: the edge, the image patterns and the projects. This
 * module is the page's pure side: the shapes, the rules copied from the API (which stays the authority), the
 * staged change a deep link carries (`policy.edge=on`, `policy.add_image=<pattern>`, `policy.add_project=<slug>`),
 * the diff and the apply state words.
 */

export const POLICY_MAX_IMAGES = 64;
export const POLICY_MAX_PROJECTS = 32;
/** The tab re-reads the policy this often while an apply runs. */
export const POLICY_POLL_MS = 5_000;

/** The runner.toml line a host's root adds once. */
export const CONSOLE_POLICY_LINE = 'console_policy = true';

export type PolicyValues = { edge: boolean; images: string[]; projects: string[] };

export type PolicyChange = {
	edge_from: boolean;
	edge_to: boolean;
	images_added: string[];
	images_removed: string[];
	projects_added: string[];
	projects_removed: string[];
};

export type PolicyApplyState = 'running' | 'applied' | 'rolled_back' | 'unknown';

export type PolicyApply = {
	state: PolicyApplyState;
	started_at: string;
	ends_by: string;
	started_by: { id: string; name: string };
	change: PolicyChange;
	finished_at: string | null;
};

export type HostPolicy = PolicyValues & {
	managed: boolean;
	policy_hash: string | null;
	apply: PolicyApply | null;
};

/** The Policy tab's data: the policy, or the read's message. */
export type HostPolicyTab = { policy: HostPolicy | null; message: string | null };

const PROJECT_RE = /^[a-z0-9][a-z0-9-]{0,63}$/;
const NAME_COMP_RE = /^[a-z0-9]+(?:(?:(?:[._]|__|[-]*)[a-z0-9]+)+)?$/;
const DOMAIN_COMP_RE = /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/;
const HASH_RE = /^[0-9a-f]{64}$/;

export function isPolicyHash(value: unknown): value is string {
	return typeof value === 'string' && HASH_RE.test(value);
}

export function isPolicyProject(value: string): boolean {
	return PROJECT_RE.test(value);
}

function validRegistryHost(host: string): boolean {
	let name = host;
	const colon = host.indexOf(':');
	if (colon >= 0) {
		const port = host.slice(colon + 1);
		const n = Number(port);
		if (!/^[1-9][0-9]{0,4}$/.test(port) || n > 65535) return false;
		name = host.slice(0, colon);
	}
	return name !== '' && name.split('.').every((c) => DOMAIN_COMP_RE.test(c));
}

/** The runner.toml image pattern rule: a registry host, then path components where `*` is one whole component. */
export function isImagePattern(value: string): boolean {
	if (value === '' || value.length > 255 || /[\s\0?#\\%@]/.test(value)) return false;
	const slash = value.indexOf('/');
	if (slash <= 0) return false;
	const host = value.slice(0, slash);
	const rest = value.slice(slash + 1);
	if (rest === '' || host.includes('*') || !validRegistryHost(host)) return false;
	return rest.split('/').every((c) => c === '*' || (!c.includes('*') && NAME_COMP_RE.test(c)));
}

/** Why a typed image pattern cannot be added, or null. */
export function imagePatternProblem(value: string, current: readonly string[]): string | null {
	if (!isImagePattern(value)) return 'Enter a pattern such as ghcr.io/acme/* : a registry host, then path parts in lower case, where * stands for one whole part.';
	if (current.includes(value)) return 'That pattern is already in the list.';
	if (current.length >= POLICY_MAX_IMAGES) return `A host allows at most ${POLICY_MAX_IMAGES} image patterns.`;
	return null;
}

/** Why a typed project slug cannot be added, or null. */
export function projectProblem(value: string, current: readonly string[]): string | null {
	if (!isPolicyProject(value)) return 'Enter a project slug: lower-case letters, digits and hyphens, starting with a letter or digit.';
	if (current.includes(value)) return 'That project is already in the list.';
	if (current.length >= POLICY_MAX_PROJECTS) return `A host allows at most ${POLICY_MAX_PROJECTS} projects.`;
	return null;
}

/**
 * The change a deep link stages on top of the host's values: `policy.edge=on|off`, then each valid
 * `policy.add_image` and `policy.add_project` not already present. Invalid or repeated values are dropped and
 * counted, so the tab can say so. The link never applies anything.
 */
export function stagedFromUrl(params: URLSearchParams, base: PolicyValues): { values: PolicyValues; dropped: number } {
	const values: PolicyValues = { edge: base.edge, images: [...base.images], projects: [...base.projects] };
	let dropped = 0;
	const edge = params.get('policy.edge');
	if (edge === 'on') values.edge = true;
	else if (edge === 'off') values.edge = false;
	else if (edge != null) dropped++;
	for (const raw of params.getAll('policy.add_image').slice(0, POLICY_MAX_IMAGES)) {
		const value = raw.trim();
		if (imagePatternProblem(value, values.images)) {
			if (!base.images.includes(value)) dropped++;
			continue;
		}
		values.images.push(value);
	}
	for (const raw of params.getAll('policy.add_project').slice(0, POLICY_MAX_PROJECTS)) {
		const value = raw.trim();
		if (projectProblem(value, values.projects)) {
			if (!base.projects.includes(value)) dropped++;
			continue;
		}
		values.projects.push(value);
	}
	return { values, dropped };
}

/** The change from `a` to `b`, in the API's shape. */
export function policyDiff(a: PolicyValues, b: PolicyValues): PolicyChange {
	return {
		edge_from: a.edge,
		edge_to: b.edge,
		images_added: b.images.filter((x) => !a.images.includes(x)),
		images_removed: a.images.filter((x) => !b.images.includes(x)),
		projects_added: b.projects.filter((x) => !a.projects.includes(x)),
		projects_removed: a.projects.filter((x) => !b.projects.includes(x))
	};
}

export function changeIsEmpty(c: PolicyChange): boolean {
	return c.edge_from === c.edge_to && c.images_added.length + c.images_removed.length + c.projects_added.length + c.projects_removed.length === 0;
}

/** One line per change, for the diff and the confirm. */
export function changeLines(c: PolicyChange): string[] {
	const lines: string[] = [];
	if (c.edge_from !== c.edge_to) lines.push(c.edge_to ? 'Turn on the edge' : 'Turn off the edge');
	for (const x of c.images_added) lines.push(`Allow images ${x}`);
	for (const x of c.images_removed) lines.push(`Stop allowing images ${x}`);
	for (const x of c.projects_added) lines.push(`Allow project ${x}`);
	for (const x of c.projects_removed) lines.push(`Stop allowing project ${x}`);
	return lines;
}

export function applyStateText(state: PolicyApplyState): string {
	switch (state) {
		case 'running':
			return 'Applying: the agent restarts with the new policy, and the old one comes back if it does not reconnect.';
		case 'applied':
			return 'Applied: the agent reconnected with the new policy.';
		case 'rolled_back':
			return 'Rolled back: the agent did not reconnect with the new policy, so the previous runner.toml was put back.';
		default:
			return 'Result unknown: the host has not reported the new policy or the old one. Check the host, then reload.';
	}
}

/** The StatusBadge label for an apply state. */
export function applyBadge(state: PolicyApplyState): { status: string; text: string } {
	switch (state) {
		case 'running':
			return { status: 'in_flight', text: 'applying' };
		case 'applied':
			return { status: 'succeeded', text: 'applied' };
		case 'rolled_back':
			return { status: 'rolled_back', text: 'rolled back' };
		default:
			return { status: 'unknown', text: 'unknown' };
	}
}

// ---- parsing (shared by the server load and tests) --------------------------------------------------------

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function stringList(value: unknown, max: number): string[] | null {
	if (!Array.isArray(value) || value.length > max) return null;
	if (!value.every((x) => typeof x === 'string' && x.length <= 255)) return null;
	return value as string[];
}

function parseChange(value: unknown): PolicyChange | null {
	if (!isRecord(value) || typeof value.edge_from !== 'boolean' || typeof value.edge_to !== 'boolean') return null;
	const ia = stringList(value.images_added, 256);
	const ir = stringList(value.images_removed, 256);
	const pa = stringList(value.projects_added, 256);
	const pr = stringList(value.projects_removed, 256);
	if (!ia || !ir || !pa || !pr) return null;
	return { edge_from: value.edge_from, edge_to: value.edge_to, images_added: ia, images_removed: ir, projects_added: pa, projects_removed: pr };
}

const STATES: readonly PolicyApplyState[] = ['running', 'applied', 'rolled_back', 'unknown'];

export function parsePolicyApply(value: unknown): PolicyApply | null | undefined {
	if (value == null) return null;
	if (!isRecord(value)) return undefined;
	const by = value.started_by;
	const change = parseChange(value.change);
	if (typeof value.started_at !== 'string' || typeof value.ends_by !== 'string' || !isRecord(by) || !change) return undefined;
	if (typeof by.id !== 'string' || typeof by.name !== 'string') return undefined;
	if (value.finished_at != null && typeof value.finished_at !== 'string') return undefined;
	const state = STATES.includes(value.state as PolicyApplyState) ? (value.state as PolicyApplyState) : 'unknown';
	return {
		state,
		started_at: value.started_at,
		ends_by: value.ends_by,
		started_by: { id: by.id, name: by.name.slice(0, 200) },
		change,
		finished_at: (value.finished_at as string | null | undefined) ?? null
	};
}

export function parseHostPolicy(value: unknown): HostPolicy | null {
	if (!isRecord(value) || typeof value.managed !== 'boolean' || typeof value.edge !== 'boolean') return null;
	const images = stringList(value.images, POLICY_MAX_IMAGES * 4);
	const projects = stringList(value.projects, POLICY_MAX_PROJECTS * 4);
	const apply = parsePolicyApply(value.apply);
	if (!images || !projects || apply === undefined) return null;
	const hash = value.policy_hash == null ? null : isPolicyHash(value.policy_hash) ? value.policy_hash : undefined;
	if (hash === undefined) return null;
	return { managed: value.managed, policy_hash: hash, edge: value.edge, images, projects, apply };
}
