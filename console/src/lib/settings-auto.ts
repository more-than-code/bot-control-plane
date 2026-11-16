/**
 * Automatic project settings (api-v1 v1.29): Connect GitHub, image access and the readiness check.
 * Pure helpers shared by the server actions, the callback route and the components. No value or token
 * ever passes through here: the API answers carry names, statuses and fixed texts only.
 */

export const IMAGE_ACCESSES = ['public', 'private_ok', 'private_no_login', 'login_failed', 'not_found', 'unknown'] as const;
export type ImageAccess = (typeof IMAGE_ACCESSES)[number];
export const IMAGE_LOGINS = ['none', 'manual', 'github'] as const;
export type ImageLogin = (typeof IMAGE_LOGINS)[number];

export type ImageAccessResult = { registry: string; access: ImageAccess; login: ImageLogin };

/** The GitHub registry a Connect GitHub stores. */
export const GITHUB_REGISTRY = 'ghcr.io';

const REGISTRY_HOST = /^[a-z0-9]([a-z0-9.-]{0,252})(:[0-9]{1,5})?$/;
const SET_NAME = /^[a-z0-9][a-z0-9-]{0,47}$/;
/** An entry-private set (api-v1 v1.33 item 6): `@` and the entry's name. */
const PRIVATE_SET_NAME = /^@[a-z0-9][a-z0-9-]{0,47}$/;
const ENV_KEY = /^[A-Z_][A-Z0-9_]{0,63}$/;
const FILE_NAME = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const IMAGE_PATTERN = /^[a-z0-9.-]+(:[0-9]{1,5})?(\/[a-z0-9._-]+)+$/;
/** The project slug rule (the API's store.Slug: up to 80 characters). */
const PROJECT_SLUG = /^[a-z0-9][a-z0-9-]{0,79}$/;

function record(value: unknown): Record<string, unknown> | null {
	return value != null && typeof value === 'object' && !Array.isArray(value) ? (value as Record<string, unknown>) : null;
}

function oneOf<T extends string>(list: readonly T[], value: unknown): value is T {
	return typeof value === 'string' && (list as readonly string[]).includes(value);
}

/** The image-access answer, or null when it does not have the v1.29 shape. Extra fields are dropped. */
export function parseImageAccess(value: unknown): ImageAccessResult | null {
	const v = record(value);
	if (!v || typeof v.registry !== 'string' || !REGISTRY_HOST.test(v.registry) || !oneOf(IMAGE_ACCESSES, v.access) || !oneOf(IMAGE_LOGINS, v.login)) return null;
	return { registry: v.registry, access: v.access, login: v.login };
}

/**
 * The one line under the Image field. `connect` asks for the Connect GitHub button, `settings` for a link to
 * Settings → Registries.
 */
export function accessLine(result: ImageAccessResult): { text: string; connect: boolean; settings: boolean } {
	const github = result.registry === GITHUB_REGISTRY;
	switch (result.access) {
		case 'public':
			return { text: 'Public image, no login needed.', connect: false, settings: false };
		case 'private_ok':
			return {
				text: result.login === 'github' ? 'Private, uses the GitHub connection.' : `Private, uses the stored login for ${result.registry}.`,
				connect: false,
				settings: false
			};
		case 'private_no_login':
			return github
				? { text: 'Private or missing: Connect GitHub.', connect: true, settings: false }
				: { text: `Private or missing: add a login for ${result.registry}.`, connect: false, settings: true };
		case 'login_failed':
			return github
				? { text: 'The stored login for ghcr.io was refused: reconnect GitHub.', connect: true, settings: false }
				: { text: `The stored login for ${result.registry} was refused: update it.`, connect: false, settings: true };
		case 'not_found':
			return { text: 'The registry has no image with this tag. Without a tag, latest is used.', connect: false, settings: false };
		default:
			return { text: 'Image access could not be checked.', connect: false, settings: false };
	}
}

export const READY_CHECKS = ['env_sets', 'env_values', 'env_refs', 'files', 'registry', 'host_access', 'host_online', 'host_images', 'host_policy', 'networks', 'env_pairs'] as const;
export type ReadyCheckName = (typeof READY_CHECKS)[number];
export const READY_STATUSES = ['ok', 'missing', 'warn', 'unknown'] as const;
export type ReadyStatus = (typeof READY_STATUSES)[number];
export const FIX_ACTIONS = ['create_env_set', 'fill_value', 'add_file', 'connect_github', 'add_registry_login', 'grant_deploy_access', 'host_policy_snippet', 'none'] as const;
export type FixAction = (typeof FIX_ACTIONS)[number];

export type ReadinessFix = { action: FixAction; set?: string; key?: string; name?: string; registry?: string; host_id?: string };
export type ReadinessCheck = { check: ReadyCheckName; status: ReadyStatus; message: string; fix: ReadinessFix };
export type Readiness = { ready: boolean; checks: ReadinessCheck[] };

function parseFix(value: unknown): ReadinessFix {
	const v = record(value);
	if (!v || !oneOf(FIX_ACTIONS, v.action)) return { action: 'none' };
	const fix: ReadinessFix = { action: v.action };
	if (typeof v.set === 'string' && (SET_NAME.test(v.set) || PRIVATE_SET_NAME.test(v.set))) fix.set = v.set;
	if (typeof v.key === 'string' && ENV_KEY.test(v.key)) fix.key = v.key;
	if (typeof v.name === 'string' && FILE_NAME.test(v.name)) fix.name = v.name;
	if (typeof v.registry === 'string' && REGISTRY_HOST.test(v.registry)) fix.registry = v.registry;
	if (typeof v.host_id === 'string' && UUID.test(v.host_id)) fix.host_id = v.host_id;
	return fix;
}

/** The readiness answer, or null when it is not the v1.29 shape. Only the listed fields are kept. */
export function parseReadiness(value: unknown): Readiness | null {
	const v = record(value);
	if (!v || typeof v.ready !== 'boolean' || !Array.isArray(v.checks) || v.checks.length === 0 || v.checks.length > 200) return null;
	const checks: ReadinessCheck[] = [];
	for (const item of v.checks) {
		const c = record(item);
		if (!c || !oneOf(READY_CHECKS, c.check) || !oneOf(READY_STATUSES, c.status) || typeof c.message !== 'string') return null;
		checks.push({ check: c.check, status: c.status, message: c.message.slice(0, 500), fix: parseFix(c.fix) });
	}
	return { ready: v.ready, checks };
}

const CHECK_LABELS: Record<ReadyCheckName, string> = {
	env_sets: 'Env sets',
	env_values: 'Env values',
	env_refs: 'Env references',
	files: 'Files',
	registry: 'Registry',
	host_access: 'Host access',
	host_online: 'Host online',
	host_images: 'Containers',
	host_policy: 'Host policy',
	networks: 'Shared networks',
	/** api-v1 v1.33 item 5: a client ID and its secret come from different places. Advisory (`warn`). */
	env_pairs: 'Env pairs'
};

export function checkLabel(name: ReadyCheckName): string {
	return CHECK_LABELS[name];
}

/** The badge word for a status. */
export function statusWord(status: ReadyStatus): string {
	switch (status) {
		case 'ok':
			return 'OK';
		case 'missing':
			return 'Missing';
		case 'warn':
			return 'Check';
		default:
			return 'Unknown';
	}
}

/** The readiness summary line. */
export function readinessSummary(r: Readiness): string {
	const missing = r.checks.filter((c) => c.status === 'missing').length;
	if (r.ready) return 'Ready as far as Deploybot can check. Deploy still runs its own checks.';
	return `${missing} item${missing === 1 ? '' : 's'} missing. You can still deploy; the deploy may be refused.`;
}

/**
 * The entry whose private set (`@<entry>`, api-v1 v1.33 item 6) a value fix is about, or null. Its keys are listed in
 * that entry's Edit drawer ("Secrets for this entry"), not in Settings → Env sets, which hides private sets.
 */
export function privateFixEntry(fix: ReadinessFix): string | null {
	if (fix.action !== 'fill_value' && fix.action !== 'create_env_set') return null;
	return fix.set != null && PRIVATE_SET_NAME.test(fix.set) ? fix.set.slice(1) : null;
}

/** The label of a fix's button or link, or null for none. */
export function fixLabel(fix: ReadinessFix): string | null {
	if (privateFixEntry(fix) != null) return 'Open entry secrets';
	switch (fix.action) {
		case 'connect_github':
			return 'Connect GitHub';
		case 'add_registry_login':
			return 'Add registry login';
		case 'fill_value':
			return 'Fill value';
		case 'create_env_set':
			return 'Add env set';
		case 'add_file':
			return 'Add file';
		case 'grant_deploy_access':
			return 'Grant access';
		case 'host_policy_snippet':
			return 'Allow on host';
		default:
			return null;
	}
}

/**
 * Where a link fix goes: the Settings cards of `projectPath` (`/projects/<id>`), or the host page's Access section.
 * Connect GitHub and Allow on host are not links (a form post and a dialog), so they return null.
 */
export function fixHref(fix: ReadinessFix, projectPath: string): string | null {
	// A private set's keys live with its entry: the catalog filtered to it (the drawer itself opens by a button).
	const owner = privateFixEntry(fix);
	if (owner != null) return `${projectPath}?tab=catalog&catalog.q=${encodeURIComponent(owner)}`;
	switch (fix.action) {
		case 'fill_value':
		case 'create_env_set':
			return `${projectPath}?tab=settings#env-sets`;
		case 'add_file':
			return `${projectPath}?tab=settings#files`;
		case 'add_registry_login':
			return `${projectPath}?tab=settings#registries`;
		case 'grant_deploy_access':
			return fix.host_id ? `/hosts/${fix.host_id}` : null;
		default:
			return null;
	}
}

export type HostPolicySnippet = { image_pattern: string; project: string; toml: string; command: string; note: string };

/** The host-policy snippet. The runner.toml lines and the command are rebuilt from the two checked names. */
export function parseHostPolicySnippet(value: unknown): HostPolicySnippet | null {
	const v = record(value);
	if (!v || typeof v.image_pattern !== 'string' || !IMAGE_PATTERN.test(v.image_pattern) || typeof v.project !== 'string' || !PROJECT_SLUG.test(v.project)) return null;
	const pattern = v.image_pattern;
	const slug = v.project;
	return {
		image_pattern: pattern,
		project: slug,
		toml: `images = ["${pattern}"]\nprojects = ["${slug}"]\n`,
		command: `sudo ./install.sh install --runner --project ${slug} --image '${pattern}'`,
		note: typeof v.note === 'string' ? v.note.slice(0, 1000) : ''
	};
}

/** Shared network name rule (api-v1 v1.25 item 1). */
const NETWORK_NAME = /^[a-z0-9][a-z0-9-]{0,31}$/;
const IPV4_CIDR = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(\d{1,2})$/;

/**
 * The shared-network snippet request (api-v1 v1.33 item 7): a network name and an IPv4 subnet in CIDR form. A shape
 * check only; the API decides what a host accepts. Null when valid, else the message to show.
 */
export function networkSnippetProblem(network: string, subnet: string): string | null {
	if (!NETWORK_NAME.test(network)) return 'A shared network name starts with a lowercase letter or digit and uses up to 32 lowercase letters, digits, and hyphens.';
	const match = IPV4_CIDR.exec(subnet);
	if (!match || !match.slice(1, 5).every((octet) => Number(octet) <= 255) || Number(match[5]) > 32) return 'Enter the subnet as an IPv4 range, such as 10.200.10.0/24.';
	return null;
}

/** A shown snippet with what was asked, so its heading never names a host chosen afterwards. */
export type NetworkSnippetResult = { hostName: string; network: string; subnet: string; snippet: string };

/** The fields a snippet or a message answers; a change to any of them makes the shown answer stale. */
export type NetworkSnippetAsk = { hostId: string; network: string; subnet: string };

export function sameSnippetAsk(a: NetworkSnippetAsk | null, b: NetworkSnippetAsk): boolean {
	return a != null && a.hostId === b.hostId && a.network.trim() === b.network.trim() && a.subnet.trim() === b.subnet.trim();
}

export function networkSnippetHeading(result: NetworkSnippetResult): string {
	return `Add to /etc/bot-agent/runner.toml on ${result.hostName} (network ${result.network}, ${result.subnet}), keeping what is there, then restart bot-agent:`;
}

/** The snippet route's `not_found`: the project has no deploy access to the host (api-v1 v1.33 item 7). */
export const NETWORK_SNIPPET_NO_ACCESS = "This project can't deploy to that host.";

/** Hosts the snippet may be asked for: this project deploys containers there (`access_level` `deploy`). */
export function networkSnippetHosts<H extends { id: string; name: string; access_level?: string | null }>(
	hosts: readonly H[],
	runsContainers: (host: H) => boolean
): Array<{ id: string; name: string }> {
	return hosts.filter((host) => host.access_level === 'deploy' && runsContainers(host)).map((host) => ({ id: host.id, name: host.name }));
}

/** The Host select's trigger text: the chosen host's name (never its id), or the prompt. */
export function snippetHostLabel(hosts: ReadonlyArray<{ id: string; name: string }>, hostId: string): string {
	return hosts.find((host) => host.id === hostId)?.name ?? 'Choose a host';
}

/** The snippet answer: its runner.toml lines as text, or null when it is not `{"snippet": string}`. */
export function parseNetworkSnippet(value: unknown): string | null {
	const v = record(value);
	if (!v || typeof v.snippet !== 'string' || v.snippet.trim() === '' || v.snippet.length > 4000) return null;
	return v.snippet;
}

/** GitHub's authorize endpoint is the only place Connect GitHub may send the browser. */
export function isGitHubAuthorizeUrl(value: unknown): value is string {
	if (typeof value !== 'string' || value.length > 4096) return false;
	try {
		const url = new URL(value);
		return url.protocol === 'https:' && url.host === 'github.com' && url.pathname === '/login/oauth/authorize' && url.username === '' && url.password === '';
	} catch {
		return false;
	}
}

/** Notes after Connect GitHub or Disconnect, carried as a short code (never API text) in `?github=`. */
export const GITHUB_NOTES = {
	connected: { kind: 'success', text: 'Connected to GitHub. ghcr.io now uses that login.' },
	expired: { kind: 'error', text: 'This GitHub sign-in expired or was already used. Start again.' },
	scope: { kind: 'error', text: 'GitHub did not grant read:packages, so nothing was saved.' },
	failed: { kind: 'error', text: 'GitHub sign-in failed. Try again.' },
	forbidden: { kind: 'error', text: 'Only an owner or admin of this project can connect GitHub.' },
	denied: { kind: 'info', text: 'GitHub sign-in was cancelled. Nothing was changed.' },
	off: { kind: 'error', text: 'GitHub is not configured on this server.' },
	revoke_failed: { kind: 'info', text: 'Disconnected here; also revoke Deploybot in your GitHub settings.' }
} as const;
export type GitHubNoteCode = keyof typeof GITHUB_NOTES;

export function githubNoteCode(value: string | null | undefined): GitHubNoteCode | null {
	return value != null && Object.hasOwn(GITHUB_NOTES, value) ? (value as GitHubNoteCode) : null;
}

/** The callback's failure code for an API error code and message. */
export function githubFailureCode(code: string, msg: string): GitHubNoteCode {
	if (code === 'bad_request') return msg.includes('read:packages') ? 'scope' : 'expired';
	if (code === 'forbidden' || code === 'not_found') return 'forbidden';
	if (code === 'unavailable') return 'off';
	return 'failed';
}

/**
 * The registry host of an image reference, as the API normalises it: the first path part when it has a dot or a
 * port or is `localhost`, else `docker.io`.
 */
export function imageRegistryHost(ref: string): string {
	const name = ref.split('@')[0];
	const slash = name.indexOf('/');
	if (slash === -1) return 'docker.io';
	const first = name.slice(0, slash);
	return first.includes('.') || first.includes(':') || first === 'localhost' ? first.toLowerCase() : 'docker.io';
}

/** Entries as the "Used by" notes read them: names and the references that count, never a value. */
export type UsedByEntry = {
	name: string;
	kind: 'program' | 'image';
	spec: { env_sets: string[]; secret_env: Record<string, string>; files?: Array<{ file: string }>; image?: string };
};

function names(catalog: readonly UsedByEntry[], uses: (entry: UsedByEntry) => boolean): string[] {
	return catalog.filter(uses).map((entry) => entry.name).sort();
}

/** Entries that use env set `set`: through env_sets or a secret_env reference. */
export function envSetUsers(catalog: readonly UsedByEntry[], set: string): string[] {
	return names(catalog, (entry) => entry.spec.env_sets.includes(set) || Object.values(entry.spec.secret_env).some((ref) => ref.split('.')[0] === set));
}

/** Image entries that mount managed file `file`. */
export function fileUsers(catalog: readonly UsedByEntry[], file: string): string[] {
	return names(catalog, (entry) => entry.kind === 'image' && (entry.spec.files ?? []).some((mount) => mount.file === file));
}

/** Image entries whose image comes from registry `host`. */
export function registryUsers(catalog: readonly UsedByEntry[], host: string): string[] {
	return names(catalog, (entry) => entry.kind === 'image' && entry.spec.image != null && imageRegistryHost(entry.spec.image) === host);
}

/**
 * The "Used by" line: names only, or that nothing uses it. `known` is false when the catalog failed to load, was
 * truncated or left entries out: then an empty list is "Usage unknown", and a list may be incomplete.
 */
export function usedByText(users: readonly string[], known = true): string {
	if (users.length === 0) return known ? 'Not used by any entry' : 'Usage unknown';
	return `Used by ${users.join(', ')}${known ? '' : ' (the list may be incomplete)'}`;
}

/** Who may act on a fix: project admins for the Settings fixes, org admins for host access. */
export type FixRights = { canAdmin: boolean; canOrgAdmin: boolean; githubConnect: boolean };

/**
 * What a fix shows for someone who cannot act on it: a short "ask" line instead of a button or a link that leads
 * nowhere. Null when they can act (the caller then shows the button or link).
 */
export function fixAsk(fix: ReadinessFix, rights: FixRights): string | null {
	switch (fix.action) {
		case 'connect_github':
			return rights.canAdmin ? null : 'Ask an admin to connect GitHub.';
		case 'add_registry_login':
			return rights.canAdmin ? null : 'Ask an admin to add the registry login.';
		case 'fill_value':
			return rights.canAdmin ? null : 'Ask an admin to fill the value.';
		case 'create_env_set':
			return rights.canAdmin ? null : 'Ask an admin to add the env set.';
		case 'add_file':
			return rights.canAdmin ? null : 'Ask an admin to add the file.';
		case 'host_policy_snippet':
			return rights.canAdmin ? null : 'Ask an admin for the host policy lines.';
		case 'grant_deploy_access':
			return rights.canOrgAdmin ? null : 'Ask an org admin to grant deploy access.';
		default:
			return null;
	}
}

/** The image-access line's state: a result, a lookup that failed (including 429), or nothing yet. */
export type AccessLineState = { result: ImageAccessResult } | { failed: true };

export function accessLineText(state: AccessLineState): { text: string; connect: boolean; settings: boolean } {
	if ('failed' in state) return { text: 'Image access could not be checked.', connect: false, settings: false };
	return accessLine(state.result);
}
