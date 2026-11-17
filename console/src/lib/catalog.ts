import type { CatalogEntry, CatalogPreview, Host } from '$lib/api-types';
import type { SortDir } from '$lib/table';

export const GRANT_ACTIONS = ['run', 'stop', 'restart', 'logs'] as const;

/** Image entries add deploy. Program grants stay on GRANT_ACTIONS. */
export const IMAGE_GRANT_ACTIONS = [...GRANT_ACTIONS, 'deploy'] as const;

/** Hover sentence for Run on a container. The aria-label stays "Run <name>". */
export const IMAGE_RUN_TIP = 'Starts the container already on this host.';

/** Hover sentence for Deploy. The aria-label stays "Deploy <name>". */
export const IMAGE_DEPLOY_TIP = 'Pulls the image and creates or replaces the container from this catalog entry.';

/** What a catalog entry is: `program`, or an image's `spec.kind` (`service` or `job`). */
export type CatalogKind = 'program' | 'service' | 'job';

/** In sort order: Program, Service, Job. */
export const CATALOG_KINDS: readonly { kind: CatalogKind; label: string; plural: string; tip: string }[] = [
	{
		kind: 'program',
		label: 'Program',
		plural: 'Programs',
		tip: 'A program on the host, run as a systemd unit: Run, Stop, Restart, Logs.'
	},
	{
		kind: 'service',
		label: 'Service',
		plural: 'Services',
		tip: 'A long-running container: Deploy creates it from the image; it can have a health check.'
	},
	{ kind: 'job', label: 'Job', plural: 'Jobs', tip: 'A container that runs once to completion: Deploy runs the job.' }
];

/** `program`, `service` or `job`. A value the console does not know is returned as sent. */
export function catalogKindOf(entry: { kind: string; spec?: unknown }): string {
	if (entry.kind !== 'image') return entry.kind;
	const spec = entry.spec as { kind?: unknown } | undefined;
	return typeof spec?.kind === 'string' ? spec.kind : entry.kind;
}

/** Label, tooltip and sort rank. An unknown kind shows as returned, has no tip, and sorts last. */
export function catalogKindInfo(entry: { kind: string; spec?: unknown }): { kind: string; label: string; tip: string; order: number } {
	const kind = catalogKindOf(entry);
	const order = CATALOG_KINDS.findIndex((item) => item.kind === kind);
	if (order < 0) return { kind, label: kind, tip: '', order: CATALOG_KINDS.length };
	const known = CATALOG_KINDS[order];
	return { kind, label: known.label, tip: known.tip, order };
}

/** Catalog column key (and `catalog.sort` value) for Last deployed. */
export const LAST_DEPLOYED_SORT = 'last_deployed';

/**
 * Last deployed sort value: the time as UTC ISO text, which sorts in time order whatever offset the
 * API used. Never (null) and unknown (absent) are null, which sorts last in both directions.
 */
export function lastDeployedValue(entry: { last_deployed_at?: string | null }): string | null {
	if (!entry.last_deployed_at) return null;
	const time = Date.parse(entry.last_deployed_at);
	return Number.isFinite(time) ? new Date(time).toISOString() : null;
}

/**
 * DataTable defaults for the Catalog & services tab (spec-catalog-last-deployed §3.2).
 * When any entry carries `last_deployed_at`: Last deployed, newest first. Never-deployed entries
 * sort last and keep the API's name order (the sort is stable). An older API without the field
 * gets no default sort, as before this column: rows stay in the API's name order (Name A–Z).
 * A `catalog.sort` naming another column without `catalog.dir` stays ascending, as before,
 * so `catalog.sort=name` is A–Z.
 */
export function catalogDefaultSort(
	entries: readonly { last_deployed_at?: string | null }[],
	rawSort: string | null,
	sortableKeys: readonly string[]
): { sort: string | null; dir: SortDir } {
	const known = entries.some((entry) => entry.last_deployed_at !== undefined);
	if (!known) return { sort: null, dir: 'asc' };
	const otherColumn = rawSort != null && rawSort !== LAST_DEPLOYED_SORT && sortableKeys.includes(rawSort);
	return { sort: LAST_DEPLOYED_SORT, dir: otherColumn ? 'asc' : 'desc' };
}

/** Header filter options for the catalog Kind column. */
export const CATALOG_KIND_OPTIONS = CATALOG_KINDS.map((item) => ({ value: item.kind, label: item.plural }));

export const CATALOG_NAME = /^[a-z0-9][a-z0-9-]{0,47}$/;

export const RUN_AS = /^[a-z_][a-z0-9_-]{0,31}$/;

export const ENV_KEY = /^[A-Z_][A-Z0-9_]{0,63}$/;

export const SPEC_HASH = /^[0-9a-f]{64}$/;

export const IDEMPOTENCY_KEY = /^[A-Za-z0-9._:-]{1,128}$/;

/** NUL or a newline. A tab is allowed in a command, directory, argument, and environment value. */
const LINE_BREAK = /[\u0000\n\r]/;;

const utf8 = new TextEncoder();

export function byteLength(value: string): number {
	return utf8.encode(value).byteLength;
}

export function isCatalogName(value: string): boolean {
	return CATALOG_NAME.test(value);
}

/**
 * A create refused with `conflict` (api-v1: the name exists in the project). Names are unique across
 * program and image entries, so the message does not name a kind. Edit keeps its name, so it cannot hit this.
 */
export function duplicateEntryMessage(name: string): string {
	return `An entry named ${name} already exists in this project.`;
}

export function isIdempotencyKey(value: string): boolean {
	return IDEMPOTENCY_KEY.test(value);
}


/** A host this project has `deploy` access to (api-v1 v1.24 item 4). Metrics access is not a catalog host. */
export function isCatalogHost(host: Pick<Host, 'access_level'>): boolean {
	return host.access_level === 'deploy';
}

/** Why this host cannot run the entry. Null means the host can be chosen. */
export function hostBlockReason(host: Pick<Host, 'programs'>, runAs: string, shell: boolean): string | null {
	if (host.programs == null) return 'This host does not allow programs.';
	if (!host.programs.enabled) return 'Programs are disabled on this host.';
	if (runAs !== '' && !host.programs.run_as.includes(runAs)) return `This host does not allow run as ${runAs}.`;
	if (shell && !host.programs.allow_shell) return 'This host does not allow a shell.';
	return null;
}

/** Block reason when a program entry uses env sets and the host's agent lacks `programs.secrets` (api-v1 v1.22). */
export const PROGRAM_SECRETS_BLOCK = "This host's agent does not support program secrets.";

/** Block reason when the host's `projects` allowlist leaves this project out (api-v1 v1.23). */
export const PROJECT_NOT_LISTED_BLOCK =
	"This host's allowlist does not include this project. The host owner adds it to `projects` in `/etc/bot-agent/runner.toml`.";

/**
 * A program entry that reads env sets: Run needs `programs.secrets`, and Restart keeps the host's stored values.
 * Null (not reported) counts as reading them, so an unknown entry is never shown as safe to start without support.
 */
export function programUsesSecrets(spec: { env_sets: readonly string[] | null; secret_env: Record<string, string> | null }): boolean {
	if (spec.env_sets == null || spec.secret_env == null) return true;
	return spec.env_sets.length > 0 || Object.keys(spec.secret_env).length > 0;
}

/** Hover note on Start and Restart of a program that uses env sets (spec-program-secrets §3.4). */
export const PROGRAM_SECRET_START_NOTE = 'Start sends the current env set values.';
export const PROGRAM_SECRET_RESTART_NOTE = 'Restart reuses the values stored on the host. A changed secret takes effect at the next Start.';

/** Under a program's Secret env (spec-program-secrets §3.4): where the values live and who can read them. */
export const PROGRAM_SECRET_HELP =
	'Env set values are written to a root-only file on the host. A process running as the same run-as account, or as root, can read them, so give a sensitive program its own account. A changed value takes effect at the next Start, not at Restart.';

/** Under a program's Secret env (api-v1 v1.22 amendment 2): keys a secret value may not set. The API refuses them. */
export const PROGRAM_SECRET_KEYS_HELP =
	'Proxy, SSL certificate, Java and Git hook variables (for example HTTPS_PROXY, SSL_CERT_FILE, JAVA_TOOL_OPTIONS, GIT_SSH_COMMAND) cannot come from a secret, because they change what the program runs. Set them as plain values.';

/** Allowlist only: false means the host's agent reported a list without this project. Unknown (null) is not blocked. */
export function projectListedBlockReason(host: Pick<Host, 'programs'>): string | null {
	return host.programs?.project_listed === false ? PROJECT_NOT_LISTED_BLOCK : null;
}

/**
 * Why Start (Run) of a program is refused on a host its policy already allows (`hostBlockReason` is null):
 * the allowlist leaves the project out, or the entry uses env sets and the agent lacks `programs.secrets`.
 */
export function programRunBlockReason(host: Pick<Host, 'programs'>, usesSecrets: boolean): string | null {
	const listed = projectListedBlockReason(host);
	if (listed) return listed;
	if (usesSecrets && host.programs?.secrets !== true) return PROGRAM_SECRETS_BLOCK;
	return null;
}

/** Why this host cannot run a container. Null means the host can be chosen. */
export function imageHostBlockReason(host: Pick<Host, 'programs'>): string | null {
	if (host.programs == null || !host.programs.images || !host.programs.docker) return 'This host does not run containers.';
	return null;
}

/** Why Deploy is refused for an entry that mounts files. An entry with no files has no reason. */
export function imageFilesBlockReason(host: Pick<Host, 'programs'>, hasFiles: boolean): string | null {
	if (!hasFiles) return null;
	if (host.programs?.files === true) return null;
	return "This host's agent does not support files";
}

/** Why Deploy is refused for an entry that sets logging. An entry without it has no reason. */
export function imageLoggingBlockReason(host: Pick<Host, 'programs'>, hasLogging: boolean): string | null {
	if (!hasLogging) return null;
	if (host.programs?.logging === true) return null;
	return "This host's agent does not support log settings";
}

/**
 * Why Deploy, Run, or Roll back is refused for an entry with a non-default health policy (api-v1 v1.15).
 * The wording is the API's gate message with a capital T, like the other block reasons.
 */
export function imageHealthPolicyBlockReason(host: Pick<Host, 'programs'>, hasPolicy: boolean): string | null {
	if (!hasPolicy) return null;
	if (host.programs?.health_policy === true) return null;
	return "This host's agent does not support health policies";
}

/** The API's first shared-network refusal (api-v1 v1.25 item 3), with a capital T like the other block reasons. */
export const SHARED_NETWORKS_UNSUPPORTED_BLOCK = "This host's agent does not support shared networks; upgrade it to 0.2.9";

/** The API's second shared-network refusal (api-v1 v1.25 item 3). */
export function sharedNetworkClosedBlock(name: string): string {
	return `Shared network ${name} is not open to this project on this host`;
}

type SharedNetworkView = NonNullable<NonNullable<Host['programs']>['shared_networks']>[number];

/**
 * Whether a host's network admits this project. A list without `projects` was already filtered by the API to the
 * project in view; with `projects` (an org admin's view) the slug must be in it. An unknown slug is not refused.
 */
function networkOpen(network: SharedNetworkView, slug: string | null): boolean {
	if (network.projects == null || slug == null) return true;
	return network.projects.includes(slug);
}

/**
 * Why Deploy or Roll back is refused for an entry that joins shared networks, mirroring the API gates.
 * No networks, or a host that does not run containers (imageHostBlockReason already says so), has no reason.
 */
export function imageNetworksBlockReason(host: Pick<Host, 'programs'>, networks: readonly string[] | undefined, slug: string | null): string | null {
	if (networks == null || networks.length === 0 || host.programs == null || imageHostBlockReason(host) != null) return null;
	const shared = host.programs.shared_networks;
	if (shared == null) return SHARED_NETWORKS_UNSUPPORTED_BLOCK;
	for (const name of networks) {
		const network = shared.find((item) => item.name === name);
		if (network == null || !networkOpen(network, slug)) return sharedNetworkClosedBlock(name);
	}
	return null;
}

/**
 * Shared networks open to this project, per host, for the container form's hint (spec amendment A5): only hosts the
 * project can deploy to that run containers. Hosts with none open are left out.
 */
export function openSharedNetworks(
	hosts: readonly Pick<Host, 'name' | 'access_level' | 'programs'>[],
	slug: string | null
): Array<{ host: string; networks: Array<{ name: string; subnet: string }> }> {
	const out: Array<{ host: string; networks: Array<{ name: string; subnet: string }> }> = [];
	for (const host of hosts) {
		if (!isCatalogHost(host) || imageHostBlockReason(host) != null) continue;
		const open = (host.programs?.shared_networks ?? [])
			.filter((network) => networkOpen(network, slug))
			.map((network) => ({ name: network.name, subnet: network.subnet }));
		if (open.length > 0) out.push({ host: host.name, networks: open });
	}
	return out;
}

/** One row of the project Settings Networks card (spec-agent-usability-1 H2). Read-only. */
export type ProjectNetworkRow = { host: string; name: string; subnet: string; entries: string[] };

/**
 * Each network open to this project on one of its hosts, with this project's image entries whose `spec.networks`
 * include it (names, A–Z). Ordered by host, then network name. From the loaded hosts and catalog only.
 */
export function projectNetworkRows(
	hosts: readonly Pick<Host, 'name' | 'access_level' | 'programs'>[],
	slug: string | null,
	catalog: readonly CatalogEntry[]
): ProjectNetworkRow[] {
	const rows: ProjectNetworkRow[] = [];
	for (const item of openSharedNetworks(hosts, slug)) {
		for (const network of [...item.networks].sort((a, b) => a.name.localeCompare(b.name))) {
			const entries = catalog
				.filter((entry) => entry.kind === 'image' && (entry.spec.networks ?? []).includes(network.name))
				.map((entry) => entry.name)
				.sort();
			rows.push({ host: item.host, name: network.name, subnet: network.subnet, entries });
		}
	}
	return rows.sort((a, b) => a.host.localeCompare(b.host) || a.name.localeCompare(b.name));
}

/** One choice of the container form's Shared networks multi-select (spec-agent-usability-1 H1). */
export type NetworkChoice = {
	name: string;
	/** Where it is open to this project: `host (subnet)`. Empty for a saved name no read host opens. */
	where: string[];
	/** False for a name already in the entry that no read host opens to this project: it still shows, marked. */
	open: boolean;
};

/**
 * The Shared networks choices: every network open to this project on its hosts (by name, A–Z), then any `selected`
 * name none of them opens, in its saved order, so a saved value is never dropped from view.
 */
export function networkChoices(
	open: ReadonlyArray<{ host: string; networks: ReadonlyArray<{ name: string; subnet: string }> }>,
	selected: readonly string[]
): NetworkChoice[] {
	const byName = new Map<string, string[]>();
	for (const item of open) {
		for (const network of item.networks) {
			const where = byName.get(network.name) ?? [];
			where.push(`${item.host} (${network.subnet})`);
			byName.set(network.name, where);
		}
	}
	const choices: NetworkChoice[] = [...byName.keys()].sort().map((name) => ({ name, where: byName.get(name) ?? [], open: true }));
	for (const name of selected) {
		if (!byName.has(name) && !choices.some((choice) => choice.name === name)) choices.push({ name, where: [], open: false });
	}
	return choices;
}

/** The networks field text with `name` turned on or off; order kept, a new name last. */
export function toggleNetwork(text: string, name: string, on: boolean): string {
	const names = text
		.split(',')
		.map((item) => item.trim())
		.filter((item) => item !== '');
	const next = on ? (names.includes(name) ? names : [...names, name]) : names.filter((item) => item !== name);
	return next.join(', ');
}

/** The networks limit as a visible, announced line once it is reached. */
export function networksLimitText(max: number): string {
	return `Limit of ${max} reached. Clear one to choose another.`;
}

/**
 * Add by name (when not every host could be read, a network may be open on one that was not): the v1.25 name rule
 * and the limit of `max`. A name already chosen is kept once.
 */
export function addNetworkByName(text: string, raw: string, max: number): { ok: true; text: string } | { ok: false; message: string } {
	const name = raw.trim();
	if (!/^[a-z0-9][a-z0-9-]{0,31}$/.test(name)) {
		return { ok: false, message: 'A shared network name starts with a lowercase letter or digit and uses up to 32 lowercase letters, digits, and hyphens.' };
	}
	const names = text.split(',').map((item) => item.trim()).filter((item) => item !== '');
	if (names.includes(name)) return { ok: true, text: names.join(', ') };
	if (names.length >= max) return { ok: false, message: networksLimitText(max) };
	return { ok: true, text: toggleNetwork(text, name, true) };
}

/**
 * A note, not a block (agent-protocol v1.14): an agent without `env_hash` reports a value-only env change to
 * a service as unchanged, so the container keeps its old env unless the deploy is forced. Null for a job
 * (it always runs fresh), for a capable agent, and for a host that cannot run containers (it has its own reason).
 */
export function imageEnvHashNote(host: Pick<Host, 'programs'>, workload: 'service' | 'job'): string | null {
	if (workload !== 'service') return null;
	if (imageHostBlockReason(host) != null) return null;
	if (host.programs?.env_hash === true) return null;
	return "This host's agent does not apply env value changes without Force.";
}

/** Union of run-as accounts on eligible hosts. Shell entries only use hosts that allow a shell. */
export function runAsChoices(hosts: readonly Pick<Host, 'access_level' | 'programs'>[], shell: boolean): string[] {
	const names = new Set<string>();
	for (const host of hosts) {
		if (!isCatalogHost(host) || host.programs == null || !host.programs.enabled) continue;
		if (shell && !host.programs.allow_shell) continue;
		for (const name of host.programs.run_as) names.add(name);
	}
	return [...names].sort();
}

export function isAllowedRunAs(
	hosts: readonly Pick<Host, 'access_level' | 'programs'>[],
	runAs: string,
	shell: boolean
): boolean {
	return runAsChoices(hosts, shell).includes(runAs);
}

/** The spec sentence, once an account is chosen. Empty until then. */
export function shellRisk(runAs: string): string | null {
	if (runAs === '') return null;
	return `anything in this line runs as ${runAs}`;
}

/** Absolute path, at most 255 bytes, no `..` segment, no NUL and no newline. */
export function cleanDir(value: string): string | null {
	const dir = value.trim();
	if (!dir.startsWith('/') || byteLength(dir) > 255 || LINE_BREAK.test(dir)) return null;
	if (dir.split('/').includes('..')) return null;
	return dir;
}

/** One command line. The text is kept as typed, including surrounding spaces. */
export function cleanCommand(value: string): string | null {
	if (value.length === 0 || byteLength(value) > 4096 || LINE_BREAK.test(value)) return null;
	return value;
}

export function cleanRunAs(value: string): string | null {
	if (value === 'root' || !RUN_AS.test(value)) return null;
	return value;
}

/** Blank becomes null. Anything else that is not a port is undefined. */
export function cleanPort(value: string): number | null | undefined {
	const trimmed = value.trim();
	if (trimmed === '') return null;
	if (!/^[0-9]+$/.test(trimmed)) return undefined;
	const port = Number(trimmed);
	if (!Number.isInteger(port) || port < 1 || port > 65535) return undefined;
	return port;
}

export function cleanRestart(value: string): 'on-failure' | 'no' | null {
	if (value === '' || value === 'on-failure') return 'on-failure';
	if (value === 'no') return 'no';
	return null;
}

export type EnvClean = { ok: true; env: Record<string, string> } | { ok: false; message: string };

/**
 * Skip blank pairs. Shape only: charset, duplicates, the 64-key cap, and no NUL or newline.
 * Reserved keys and the v1.6.1 denylist are the API's decision. The form shows `err.msg`.
 */
export function cleanEnv(rows: readonly { key: string; value: string }[]): EnvClean {
	const env: Record<string, string> = {};
	for (const row of rows) {
		const key = row.key.trim();
		if (key === '' && row.value === '') continue;
		if (key === '') return { ok: false, message: 'An environment value needs a key.' };
		if (!ENV_KEY.test(key)) return { ok: false, message: 'Environment keys use A-Z, digits, and underscore.' };
		if (LINE_BREAK.test(row.value) || byteLength(row.value) > 4096) {
			return { ok: false, message: 'An environment value is too long or contains a newline.' };
		}
		if (Object.hasOwn(env, key)) return { ok: false, message: 'Each environment key can appear once.' };
		if (Object.keys(env).length >= 64) return { ok: false, message: 'At most 64 environment variables.' };
		env[key] = row.value;
	}
	return { ok: true, env };
}

export type EnvRow = { key: string; value: string };

type PreviewSubmitter = {
	addEventListener(type: 'submit', listener: () => void, options: { once: true }): void;
	removeEventListener(type: 'submit', listener: () => void): void;
	requestSubmit(submitter: unknown): void;
};

/**
 * Submit with the Preview button. The callback runs only when the submit event fires.
 * Browser constraint validation does not fire it, so a blocked preview is not remembered.
 */
export function submitCatalogPreview(form: PreviewSubmitter, submitter: unknown, remember: () => void): void {
	form.addEventListener('submit', remember, { once: true });
	try {
		form.requestSubmit(submitter);
	} finally {
		form.removeEventListener('submit', remember);
	}
}

/** A preview result applies only when it is still the latest preview. A save always applies. */
export function shouldApplyPreviewUpdate(previewing: boolean, requestId: number, latestPreviewId: number): boolean {
	if (!previewing) return true;
	return requestId === latestPreviewId;
}

/** Keep the idempotency key until the post succeeds, so a retry replays instead of dispatching again. */
export function reuseIdempotencyKey(resultType: string): boolean {
	return resultType !== 'success';
}

/** Owners edit the spec. Everyone else gets the catalog row without the values that will run. */
export function catalogForCaller(entries: readonly CatalogEntry[], owner: boolean): CatalogEntry[] {
	if (owner) return [...entries];
	return entries.map((item) => {
		if (item.kind === 'image') return { ...item, spec: { ...item.spec, env: {} } };
		return { ...item, spec: { ...item.spec, exec: '', args: [], env: {} } };
	});
}

/** Identifies a parsed line, so the form does not ask the server to parse the same line twice. */
export function previewToken(shell: boolean, line: string): string {
	return `${shell ? '1' : '0'}\n${line}`;
}

/**
 * Apply a parse preview to the form. Hoisted `KEY=value` words leave the command (`command_display`
 * is the argv without them) and become env rows; a typed value replaces a row with the same key.
 * Leaving them in both places would send each key twice. Returns null when there is nothing to do:
 * shell mode (nothing is split), nothing hoisted, or the field no longer holds the previewed line.
 */
export function hoistPreviewEnv(
	command: string,
	shell: boolean,
	rows: readonly EnvRow[],
	previewedLine: string,
	preview: CatalogPreview
): { command: string; env: EnvRow[] } | null {
	const hoisted = Object.entries(preview.env);
	if (shell || hoisted.length === 0 || command !== previewedLine) return null;
	const env = rows.map((row) => ({
		key: row.key,
		value: Object.hasOwn(preview.env, row.key) ? preview.env[row.key] : row.value
	}));
	for (const [key, value] of hoisted) {
		if (!env.some((row) => row.key === key)) env.push({ key, value });
	}
	return { command: preview.command_display, env };
}
