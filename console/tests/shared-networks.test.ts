import assert from 'node:assert/strict';
import { describe, test } from 'node:test';
import { render } from 'svelte/server';
import type { Host, HostPrograms, ImageDeploy, ImageEntry, ImageSpec } from '../src/lib/api-types.ts';
import {
	SHARED_NETWORKS_UNSUPPORTED_BLOCK,
	imageHostBlockReason,
	imageNetworksBlockReason,
	openSharedNetworks,
	sharedNetworkClosedBlock
} from '../src/lib/catalog.ts';
import { hostSharedNetworks } from '../src/lib/host-view.ts';
import {
	applyDockerPaste,
	blankImageDraft,
	dockerRunPreview,
	imageDraftFromForm,
	imageRequestBody,
	imageSpecFromDraft,
	parseDockerRun
} from '../src/lib/image-spec.ts';
import { parseHost, parseHostPrograms, parseImageSpec } from '../src/lib/server/shape.ts';
import OverviewTab from '../src/routes/hosts/[id]/OverviewTab.svelte';
import CatalogDeployForm from '../src/routes/projects/[id]/CatalogDeployForm.svelte';
import CatalogImageForm from '../src/routes/projects/[id]/CatalogImageForm.svelte';
import { PROJECT, USER } from './harness.ts';
import { setPageUrl } from './stubs/app-state.ts';

const HOST_ID = '22222222-2222-4222-8222-222222222222';
const ENTRY_ID = '33333333-3333-4333-8333-333333333333';
const DD = { name: 'dd', subnet: '10.200.10.0/24' };

function policy(overrides: Partial<HostPrograms> = {}): HostPrograms {
	return {
		enabled: true,
		run_as: ['joe'],
		allow_shell: false,
		policy_hash: 'policy-1',
		images: true,
		docker: true,
		files: false,
		logging: false,
		health_policy: false,
		secrets: false,
		projects: null,
		project_listed: null,
		shared_networks: null,
		env_hash: false,
		...overrides
	};
}

function host(programs: HostPrograms | null = policy(), overrides: Partial<Host> = {}): Host {
	return {
		id: HOST_ID,
		org_id: PROJECT.id,
		name: 'pi3',
		node_type: 'pi',
		os: 'linux',
		arch: 'arm64',
		always_on: true,
		agent_version: '0.2.9',
		status: 'online',
		last_seen_at: '2026-11-14T00:00:00Z',
		enrolled_at: '2026-11-01T00:00:00Z',
		revoked_at: null,
		access_level: 'deploy',
		access: null,
		programs,
		...overrides
	};
}

function spec(overrides: Partial<ImageSpec> = {}): ImageSpec {
	return {
		kind: 'service',
		image: 'nginx:alpine',
		command: null,
		entrypoint: null,
		ports: [],
		env: {},
		env_sets: [],
		secret_env: {},
		volumes: [],
		files: [],
		restart: 'unless-stopped',
		user: '',
		healthy_after_sec: 30,
		memory_mb: null,
		timeout_sec: 600,
		healthcheck: null,
		logging: null,
		health_policy: null,
		...overrides
	};
}

function entry(networks?: string[]): ImageEntry {
	return {
		id: ENTRY_ID,
		project_id: PROJECT.id,
		name: 'serve',
		kind: 'image',
		spec: spec(networks ? { networks } : {}),
		spec_hash: 'a'.repeat(64),
		command_display: 'nginx:alpine',
		created_by: PROJECT.id,
		created_at: '2026-11-14T00:00:00Z',
		updated_at: '2026-11-14T00:00:00Z',
		my_actions: ['deploy']
	} as ImageEntry;
}

const RAW_SPEC = {
	kind: 'service',
	image: 'nginx',
	command: null,
	entrypoint: null,
	ports: [],
	env: {},
	env_sets: [],
	secret_env: {},
	volumes: [],
	restart: 'unless-stopped',
	user: '',
	memory_mb: null,
	healthy_after_sec: 30
};

const RAW_PROGRAMS = { enabled: true, run_as: ['joe'], allow_shell: false, policy_hash: 'policy-1' };

const DEPLOY_PROPS = {
	deployAction: '?/catalogDeploy',
	rollbackAction: '?/catalogRollback',
	removeAction: '?/catalogRemove',
	confirmAction: '?/catalogConfirm',
	historyAction: '?/catalogDeploys'
};

function deployRow(id: string, imageDigest: string): ImageDeploy {
	return {
		id,
		entry_id: ENTRY_ID,
		host_id: HOST_ID,
		digest: imageDigest,
		spec_hash: 'a'.repeat(64),
		outcome: 'succeeded',
		exit_code: 0,
		started_at: '2026-11-14T00:00:00Z',
		finished_at: '2026-11-14T00:01:00Z',
		by: USER.id,
		via: 'console'
	};
}

/** A deploy result for the chosen host with two earlier digests, so Roll back buttons render. */
function withHistory() {
	return {
		name: 'serve',
		resultId: '44444444-4444-4444-8444-444444444444',
		hostId: HOST_ID,
		workload: 'service' as const,
		waitSec: 30,
		outcome: null,
		status: null,
		lines: [],
		history: [deployRow('55555555-5555-4555-8555-555555555551', `sha256:${'a'.repeat(64)}`), deployRow('55555555-5555-4555-8555-555555555552', `sha256:${'b'.repeat(64)}`)],
		confirmation: null,
		confirmKind: null,
		message: null
	};
}

function buttons(html: string): string[] {
	return [...html.matchAll(/<button\b[^>]*>[\s\S]*?<\/button>/g)].map((match) => match[0]);
}

function control(html: string, label: string): string {
	const found = buttons(html).find((tag) => tag.includes(label));
	assert.equal(found != null, true, label);
	return found ?? '';
}

function openTag(tag: string): string {
	return tag.slice(0, tag.indexOf('>') + 1);
}

function markedDisabled(tag: string): boolean {
	return /\sdisabled(?:=|\s|>)/.test(openTag(tag));
}

function imageForm(props: Record<string, unknown>): string {
	setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=catalog`);
	return render(CatalogImageForm, {
		props: { mode: 'edit', entry: entry(['dd']), saveAction: '?/catalogUpdate', sets: [], setsStatus: 'ready', editor: null, ...props }
	}).body;
}

describe('shared networks (api-v1 v1.25)', () => {
	test('parseImageSpec keeps networks only when set and refuses a bad list', () => {
		assert.equal(Object.hasOwn(parseImageSpec(RAW_SPEC) ?? {}, 'networks'), false);
		assert.equal(Object.hasOwn(parseImageSpec({ ...RAW_SPEC, networks: null }) ?? {}, 'networks'), false);
		assert.equal(Object.hasOwn(parseImageSpec({ ...RAW_SPEC, networks: [] }) ?? {}, 'networks'), false);
		assert.deepEqual(parseImageSpec({ ...RAW_SPEC, networks: ['dd', 'web-2'] })?.networks, ['dd', 'web-2']);
		for (const bad of [['DD'], ['-dd'], ['dd', 'dd'], ['a', 'b', 'c', 'd', 'e'], [1], 'dd', [`a${'b'.repeat(32)}`]]) {
			assert.equal(parseImageSpec({ ...RAW_SPEC, networks: bad }), null, JSON.stringify(bad));
		}
	});

	test('parseHostPrograms: missing is null, a valid list is kept, a malformed one disables the policy', () => {
		assert.equal(parseHostPrograms(RAW_PROGRAMS)?.shared_networks, null);
		assert.equal(parseHostPrograms({ ...RAW_PROGRAMS, shared_networks: null })?.shared_networks, null);
		assert.deepEqual(parseHostPrograms({ ...RAW_PROGRAMS, shared_networks: [] })?.shared_networks, []);
		// A member's view: no `projects`.
		assert.deepEqual(parseHostPrograms({ ...RAW_PROGRAMS, shared_networks: [DD] })?.shared_networks, [DD]);
		// An org admin's view: members kept, a repeated slug dropped.
		assert.deepEqual(
			parseHostPrograms({ ...RAW_PROGRAMS, shared_networks: [{ ...DD, projects: ['daikou', 'defect-drainer', 'daikou'] }] })?.shared_networks,
			[{ ...DD, projects: ['daikou', 'defect-drainer'] }]
		);
		const many = Array.from({ length: 9 }, (_, index) => ({ name: `n${index}`, subnet: `10.200.${index}.0/24` }));
		for (const bad of [
			'dd',
			many,
			[{ name: 'DD', subnet: DD.subnet }],
			[{ name: 'dd', subnet: '10.200.10.0' }],
			[{ name: 'dd', subnet: '300.1.1.0/24' }],
			[{ name: 'dd', subnet: '10.0.0.0/33' }],
			[DD, DD],
			[{ ...DD, projects: ['Bad Slug'] }],
			[{ ...DD, projects: Array.from({ length: 33 }, (_, index) => `p${index}`) }]
		]) {
			assert.equal(parseHostPrograms({ ...RAW_PROGRAMS, shared_networks: bad }), null, JSON.stringify(bad).slice(0, 60));
		}
		// The host stays on the page with programs null, as for any other malformed `programs` key.
		const raw = {
			id: HOST_ID,
			name: 'pi3',
			node_type: 'pi',
			os: 'linux',
			arch: 'arm64',
			always_on: true,
			status: 'online',
			agent_version: '0.2.9',
			last_seen_at: null,
			enrolled_at: '2026-11-01T00:00:00Z',
			revoked_at: null,
			org_id: null,
			access_level: 'deploy',
			programs: { ...RAW_PROGRAMS, shared_networks: 'dd' }
		};
		const parsed = parseHost(raw);
		assert.notEqual(parsed, null);
		assert.equal(parsed?.programs, null);
	});

	test('the draft validates like the API, builds networks only when set, and round-trips through the form', () => {
		const draft = blankImageDraft();
		draft.image = 'nginx';
		const none = imageSpecFromDraft(draft);
		assert.equal(none.ok, true);
		if (none.ok) {
			assert.equal(Object.hasOwn(none.spec, 'networks'), false);
			assert.equal(Object.hasOwn(imageRequestBody(none.spec, null).spec as object, 'networks'), false);
		}
		draft.networks = ' dd , web-2 ,';
		const set = imageSpecFromDraft(draft);
		assert.equal(set.ok, true);
		if (set.ok) {
			assert.deepEqual(set.spec.networks, ['dd', 'web-2']);
			assert.deepEqual((imageRequestBody(set.spec, null).spec as { networks: string[] }).networks, ['dd', 'web-2']);
		}
		for (const bad of ['DD', 'dd, dd', 'a, b, c, d, e', '-x', `a${'b'.repeat(32)}`]) {
			const refused = imageSpecFromDraft({ ...draft, networks: bad });
			assert.equal(refused.ok, false, bad);
			if (!refused.ok) assert.equal(refused.field, 'networks', bad);
		}
		const form = new FormData();
		form.set('image', 'nginx');
		form.set('networks', 'dd');
		assert.equal(imageDraftFromForm(form).draft.networks, 'dd');
	});

	test('the preview adds --network per name and the paste maps it back', () => {
		const line = dockerRunPreview({
			name: 'serve',
			image: 'nginx',
			command: [],
			entrypoint: [],
			ports: [],
			envKeys: [],
			volumes: [],
			restart: 'unless-stopped',
			user: '',
			memoryMb: null,
			networks: ['dd', 'web']
		});
		assert.match(line, /--network deploybot-shared-dd --network deploybot-shared-web/);
		const parsed = parseDockerRun(line);
		assert.equal(parsed.ok, true);
		if (!parsed.ok) return;
		assert.equal(parsed.draft.networks, 'dd, web');
		const applied = applyDockerPaste({ ...blankImageDraft(), networks: 'old' }, parsed);
		assert.equal(applied.networks, 'dd, web');
		const short = parseDockerRun('docker run --net=deploybot-shared-dd nginx');
		assert.equal(short.ok && short.draft.networks, 'dd');
		// A paste without a shared network clears the field, like ports and env.
		const plain = parseDockerRun('docker run nginx');
		assert.equal(plain.ok, true);
		if (plain.ok) assert.equal(applyDockerPaste({ ...blankImageDraft(), networks: 'dd' }, plain).networks, '');
		// Other networks stay refused; host networking keeps its own message.
		for (const other of ['docker run --network bridge nginx', 'docker run --network deploybot-shared-BAD nginx', 'docker run --network deploybot-home-lab nginx']) {
			const refused = parseDockerRun(other);
			assert.equal(refused.ok, false, other);
			if (!refused.ok) assert.equal(refused.message, '--network is not supported.', other);
		}
		const hostNet = parseDockerRun('docker run --network host nginx');
		assert.equal(!hostNet.ok && hostNet.message, 'Host networking is not supported.');
	});

	test('deploy block reasons mirror the two API refusals', () => {
		assert.equal(imageNetworksBlockReason(host(policy()), undefined, 'defect-drainer'), null);
		assert.equal(imageNetworksBlockReason(host(policy()), ['dd'], 'defect-drainer'), SHARED_NETWORKS_UNSUPPORTED_BLOCK);
		assert.equal(imageNetworksBlockReason(host(null), ['dd'], 'defect-drainer'), null);
		// Member view: the API listed only the networks open to the project.
		assert.equal(imageNetworksBlockReason(host(policy({ shared_networks: [DD] })), ['dd'], 'defect-drainer'), null);
		assert.equal(imageNetworksBlockReason(host(policy({ shared_networks: [] })), ['dd'], 'defect-drainer'), sharedNetworkClosedBlock('dd'));
		// Org admin view: the member list decides.
		const admin = host(policy({ shared_networks: [{ ...DD, projects: ['daikou'] }] }));
		assert.equal(imageNetworksBlockReason(admin, ['dd'], 'daikou'), null);
		assert.equal(imageNetworksBlockReason(admin, ['dd'], 'defect-drainer'), 'Shared network dd is not open to this project on this host');
		assert.deepEqual(openSharedNetworks([admin, host(policy(), { name: 'pi1' })], 'daikou'), [{ host: 'pi3', networks: [DD] }]);
		assert.deepEqual(openSharedNetworks([admin], 'defect-drainer'), []);

		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=catalog`);
		const props = {
			entry: entry(['dd']),
			deployAction: '?/catalogDeploy',
			rollbackAction: '?/catalogRollback',
			removeAction: '?/catalogRemove',
			confirmAction: '?/catalogConfirm',
			historyAction: '?/catalogDeploys',
			result: null,
			projectSlug: 'defect-drainer'
		};
		const old = render(CatalogDeployForm, { props: { ...props, hosts: [host(policy())] } }).body;
		assert.equal(old.includes(`pi3: ${SHARED_NETWORKS_UNSUPPORTED_BLOCK}`), true);
		const open = render(CatalogDeployForm, { props: { ...props, hosts: [host(policy({ shared_networks: [DD] }))] } }).body;
		assert.equal(open.includes('Shared network'), false);
		assert.equal(open.includes('does not support shared networks'), false);
	});

	test('the container form has the Shared networks field, help with the container name, and the open-network hint', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=catalog`);
		const html = render(CatalogImageForm, {
			props: {
				mode: 'edit',
				entry: entry(['dd']),
				hosts: [host(policy({ shared_networks: [DD] }))],
				projectSlug: 'defect-drainer',
				saveAction: '?/catalogUpdate',
				sets: [],
				setsStatus: 'ready',
				editor: null
			}
		}).body;
		assert.match(html, /name="networks"[^>]*value="dd"|value="dd"[^>]*name="networks"/);
		assert.equal(html.includes('Other entries on the same shared network reach this one by its container name, defect-drainer-serve.'), true);
		// H1: a checkbox per open network, with where it is open.
		assert.equal(html.includes('Open on pi3 (10.200.10.0/24)'), true);
		assert.match(html, /data-network-choice="dd"/);
		assert.equal(html.includes('--network deploybot-shared-dd'), true);
		const blank = render(CatalogImageForm, {
			props: { mode: 'create', entry: null, saveAction: '?/catalogCreate', sets: [], setsStatus: 'ready', editor: null }
		}).body;
		assert.equal(blank.includes('reach this one by its container name, &lt;project>-&lt;entry>.'), true);
		assert.equal(blank.includes('Open on'), false);
	});

	test('Host Overview: one network per line, members for org admins, none and not reported where the policy shows', () => {
		const admin = { access: [] };
		assert.deepEqual(hostSharedNetworks(host(policy({ shared_networks: [DD] }))), { networks: ['dd (10.200.10.0/24)'] });
		assert.deepEqual(
			hostSharedNetworks(
				host(policy({ shared_networks: [{ ...DD, projects: ['daikou', 'defect-drainer'] }, { name: 'web', subnet: '10.200.11.0/24', projects: [] }], projects: ['daikou'] }), admin)
			),
			{ networks: ['dd (10.200.10.0/24): daikou, defect-drainer', 'web (10.200.11.0/24): no projects'] }
		);
		assert.equal(hostSharedNetworks(host(policy({ shared_networks: [] }))), null);
		assert.equal(hostSharedNetworks(host(policy())), null);
		assert.equal(hostSharedNetworks(host(null)), null);
		assert.deepEqual(hostSharedNetworks(host(policy({ shared_networks: [] }), admin)), { status: 'none' });
		assert.deepEqual(hostSharedNetworks(host(policy(), admin)), { status: 'not reported' });
		// No policy at all from the agent: the Host policy line shows for an admin, and the networks are not reported.
		assert.deepEqual(hostSharedNetworks(host(null, admin)), { status: 'not reported' });

		setPageUrl(`http://console.example/hosts/${HOST_ID}?tab=overview`);
		const props = { range: '1h' as const, metrics: [], snapshot: null };
		const member = render(OverviewTab, { props: { ...props, host: host(policy({ shared_networks: [DD] })) } }).body;
		assert.equal(member.includes('Shared networks'), true);
		assert.match(member, /<li[^>]*>dd \(10\.200\.10\.0\/24\)<\/li>/);
		const several = render(OverviewTab, {
			props: {
				...props,
				host: host(policy({ shared_networks: [{ ...DD, projects: [] }, { name: 'web', subnet: '10.200.11.0/24', projects: ['daikou'] }], projects: [] }), admin)
			}
		}).body;
		assert.match(several, /<li[^>]*>dd \(10\.200\.10\.0\/24\): no projects<\/li>/);
		assert.match(several, /<li[^>]*>web \(10\.200\.11\.0\/24\): daikou<\/li>/);
		const none = render(OverviewTab, { props: { ...props, host: host(policy({ shared_networks: [], projects: [] }), admin) } }).body;
		assert.match(none.replaceAll(/<!--[^>]*-->/g, ''), /Shared networks<\/dt>\s*<dd[^>]*>\s*none\s*<\/dd>/);
		const plain = render(OverviewTab, { props: { ...props, host: host(policy()) } }).body;
		assert.equal(plain.includes('Shared networks'), false);
	});

	test('an agent without env_hash or shared_networks: block reason and env note both show, Deploy described by the reason only', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=catalog`);
		const old = host(policy({ shared_networks: null, env_hash: false }));
		const html = render(CatalogDeployForm, { props: { ...DEPLOY_PROPS, entry: entry(['dd']), projectSlug: 'defect-drainer', hosts: [old], result: null } }).body;
		const note = "pi3: This host's agent does not apply env value changes without Force.";
		assert.equal(html.includes(`pi3: ${SHARED_NETWORKS_UNSUPPORTED_BLOCK}`), true);
		assert.equal(html.includes(note), true);
		const deploy = control(html, 'Deploy');
		assert.equal(markedDisabled(deploy), true);
		assert.match(openTag(deploy), /aria-describedby="deploy-serve-reasons"/);
		// The reasons element holds the block reason and not the note.
		const start = html.indexOf('id="deploy-serve-reasons"');
		const reasons = html.slice(start, html.indexOf('</div>', start));
		assert.equal(reasons.includes(SHARED_NETWORKS_UNSUPPORTED_BLOCK), true);
		assert.equal(reasons.includes(note), false);
	});

	test('Deploy and Roll back are disabled and described by their reasons on a closed host', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=catalog`);
		const closed = host(policy({ shared_networks: [] }));
		const props = { ...DEPLOY_PROPS, entry: entry(['dd']), projectSlug: 'defect-drainer', hosts: [closed] };
		const fresh = render(CatalogDeployForm, { props: { ...props, result: null } }).body;
		const deploy = control(fresh, 'Deploy');
		assert.equal(markedDisabled(deploy), true);
		assert.match(openTag(deploy), /aria-describedby="deploy-serve-reasons"/);
		assert.match(fresh, /id="deploy-serve-reasons"[^>]*>[\s\S]*pi3: Shared network dd is not open to this project on this host/);

		const history = render(CatalogDeployForm, { props: { ...props, result: withHistory() } }).body;
		const rollback = control(history, 'Roll back');
		assert.equal(markedDisabled(rollback), true);
		assert.match(openTag(rollback), /aria-describedby="rollback-serve-networks"/);
		assert.equal(history.includes('id="rollback-serve-networks"'), true);

		// Any other reason describes Deploy too (here: an agent without log settings).
		const logged = { ...entry(), spec: spec({ logging: { max_size_mb: 10, max_files: 3 } }) } as ImageEntry;
		const other = render(CatalogDeployForm, { props: { ...props, entry: logged, hosts: [host(policy())], result: null } }).body;
		assert.match(openTag(control(other, 'Deploy')), /aria-describedby="deploy-serve-reasons"/);
		// An open host: Deploy is enabled and not described by reasons.
		const open = render(CatalogDeployForm, { props: { ...props, hosts: [host(policy({ shared_networks: [DD] }))], result: null } }).body;
		const enabled = control(open, 'Deploy');
		assert.equal(markedDisabled(enabled), false);
		assert.equal(openTag(enabled).includes('aria-describedby'), false);
	});

	test('the block reason: no networks, the first closed name, and the container reason first', () => {
		const admin = host(policy({ shared_networks: [{ ...DD, projects: ['daikou'] }, { name: 'web', subnet: '10.200.11.0/24', projects: ['other'] }] }));
		// No slug in view: an admin's member list cannot refuse.
		assert.equal(imageNetworksBlockReason(admin, ['dd', 'web'], null), null);
		// Only the second name is closed: its reason.
		assert.equal(imageNetworksBlockReason(admin, ['dd', 'web'], 'daikou'), sharedNetworkClosedBlock('web'));
		// Images or Docker off: the container reason wins and networks add none.
		for (const off of [policy({ images: false }), policy({ docker: false })]) {
			assert.notEqual(imageHostBlockReason(host(off)), null);
			assert.equal(imageNetworksBlockReason(host(off), ['dd'], 'daikou'), null);
		}
	});

	test('an org admin host leaving the project out is refused through the component', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=catalog`);
		const admin = host(policy({ shared_networks: [{ ...DD, projects: ['daikou'] }], projects: ['daikou'] }), { access: [] });
		const html = render(CatalogDeployForm, { props: { ...DEPLOY_PROPS, entry: entry(['dd']), projectSlug: 'defect-drainer', hosts: [admin], result: null } }).body;
		assert.equal(html.includes('pi3: Shared network dd is not open to this project on this host'), true);
		assert.equal(markedDisabled(control(html, 'Deploy')), true);
		const member = render(CatalogDeployForm, { props: { ...DEPLOY_PROPS, entry: entry(['dd']), projectSlug: 'daikou', hosts: [admin], result: null } }).body;
		assert.equal(member.includes('Shared network dd'), false);
	});

	test('openSharedNetworks lists only hosts the project deploys to that run containers', () => {
		const open = policy({ shared_networks: [DD] });
		const hosts = [
			host(open, { name: 'pi1', access_level: 'metrics' }),
			host(policy({ shared_networks: [DD], images: false }), { name: 'pi2' }),
			host(policy({ shared_networks: [DD], docker: false }), { name: 'pi4' }),
			host(open, { name: 'pi3' })
		];
		assert.deepEqual(openSharedNetworks(hosts, 'daikou'), [{ host: 'pi3', networks: [DD] }]);
	});

	test('parseHostPrograms takes exactly 8 networks; the subnet check is shape only', () => {
		const eight = Array.from({ length: 8 }, (_, index) => ({ name: `n${index}`, subnet: `10.200.${index}.0/24` }));
		assert.equal(parseHostPrograms({ ...RAW_PROGRAMS, shared_networks: eight })?.shared_networks?.length, 8);
		// Display only and deliberately looser than the API: host bits set and a public range are not refused here,
		// because the console never decides with the subnet; the API and the agent validate it.
		assert.deepEqual(parseHostPrograms({ ...RAW_PROGRAMS, shared_networks: [{ name: 'dd', subnet: '8.8.8.8/0' }] })?.shared_networks, [
			{ name: 'dd', subnet: '8.8.8.8/0' }
		]);
	});

	test('paste: an empty name is refused, a repeated name kept once, bridge still refused (amendment A1)', () => {
		const empty = parseDockerRun('docker run --network deploybot-shared- nginx');
		assert.equal(!empty.ok && empty.message, '--network is not supported.');
		const twice = parseDockerRun('docker run --network deploybot-shared-dd --network deploybot-shared-dd nginx');
		assert.equal(twice.ok && twice.draft.networks, 'dd');
		const bridge = parseDockerRun('docker run --network bridge nginx');
		assert.equal(!bridge.ok && bridge.message, '--network is not supported.');
	});

	test('the form: help and format, a neutral placeholder, the describedby chain, and the empty and truncated cases', () => {
		const html = imageForm({ hosts: [host(policy({ shared_networks: [] }))], projectSlug: 'defect-drainer' });
		assert.equal(html.includes('Choose up to 4 shared networks.'), true);
		assert.equal(html.replaceAll('&#39;', "'").includes("A host's administrator opens a network to a project in runner.toml."), true);
		assert.equal(html.includes('No host opens a shared network to this project.'), true);
		assert.equal(html.includes('Not every host could be read'), false);
		// H1: the field is a group of checkboxes, described by the help; the names post in a hidden field.
		const group = /<div[^>]*role="group"[^>]*aria-labelledby="[^"]*-networks-label"[^>]*>/.exec(html)?.[0] ?? '';
		assert.match(group, /aria-describedby="[^"]*-networks-hint"/);
		assert.match(/<input[^>]*name="networks"[^>]*>/.exec(html)?.[0] ?? '', /type="hidden"/);

		const errored = imageForm({ hosts: [], editor: { mode: 'edit', name: 'serve', draft: { ...blankImageDraft(), image: 'nginx', networks: 'DD' }, field: 'networks', message: 'bad' } });
		const errorGroup = /<div[^>]*role="group"[^>]*aria-labelledby="[^"]*-networks-label"[^>]*>/.exec(errored)?.[0] ?? '';
		assert.match(errorGroup, /aria-describedby="[^"]*-networks-hint [^"]*-error"/);

		const truncated = imageForm({ hosts: [host(policy({ shared_networks: [] }))], hostsIncomplete: true });
		assert.equal(truncated.includes('No host opens a shared network to this project.'), false);
		assert.equal(truncated.includes('None of the hosts read opens a shared network to this project.'), true);
		assert.equal(truncated.includes('The choices come only from the hosts that were read. Not every host could be read, so another host may open more networks; add one by name.'), true);
	});

	test('the form loads an empty field for an entry whose networks are null or absent', () => {
		const absent = imageForm({ entry: entry() });
		assert.match(/<input[^>]*name="networks"[^>]*>/.exec(absent)?.[0] ?? '', /value=""/);
		const nulled = { ...entry(), spec: { ...spec(), networks: null } } as unknown as ImageEntry;
		const fromNull = imageForm({ entry: nulled });
		assert.match(/<input[^>]*name="networks"[^>]*>/.exec(fromNull)?.[0] ?? '', /value=""/);
	});
});
