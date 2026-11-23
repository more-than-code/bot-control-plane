import assert from 'node:assert/strict';
import { describe, test } from 'node:test';
import { render } from 'svelte/server';
import type { ImageEntry, ImageSpec } from '../src/lib/api-types.ts';
import { blankImageDraft, imageDraftFromForm, imageRequestBody, imageSpecFromDraft } from '../src/lib/image-spec.ts';
import { parseImageSpec } from '../src/lib/server/shape.ts';
import CatalogImageForm from '../src/routes/projects/[id]/CatalogImageForm.svelte';
import { PROJECT } from './harness.ts';
import { setPageUrl } from './stubs/app-state.ts';

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

function entry(spec: ImageSpec): ImageEntry {
	return {
		id: '33333333-3333-4333-8333-333333333333',
		project_id: PROJECT.id,
		name: 'api',
		kind: 'image',
		spec,
		spec_hash: 'a'.repeat(64),
		command_display: 'nginx',
		created_by: PROJECT.id,
		created_at: '2026-11-23T00:00:00Z',
		updated_at: '2026-11-23T00:00:00Z',
		my_actions: ['deploy']
	} as ImageEntry;
}

describe('ImageSpec network (api-v1 v1.50 item 1)', () => {
	test('parseImageSpec keeps network host and refuses anything else', () => {
		assert.equal(Object.hasOwn(parseImageSpec(RAW_SPEC) ?? {}, 'network'), false);
		assert.equal(Object.hasOwn(parseImageSpec({ ...RAW_SPEC, network: null }) ?? {}, 'network'), false);
		assert.equal(parseImageSpec({ ...RAW_SPEC, network: 'host' })?.network, 'host');
		for (const bad of ['bridge', '', 1]) assert.equal(parseImageSpec({ ...RAW_SPEC, network: bad }), null, String(bad));
		assert.equal(parseImageSpec({ ...RAW_SPEC, network: 'host', networks: ['dd'] }), null);
	});

	test('an edit keeps network host: form field, draft, spec and request body', () => {
		const form = new FormData();
		form.set('image', 'nginx');
		form.set('workload', 'service');
		form.set('network', 'host');
		const { draft } = imageDraftFromForm(form);
		assert.equal(draft.network, 'host');
		const checked = imageSpecFromDraft(draft);
		assert.equal(checked.ok, true);
		if (!checked.ok) return;
		assert.equal(checked.spec.network, 'host');
		assert.equal((imageRequestBody(checked.spec, null).spec as Record<string, unknown>).network, 'host');
		// Without it nothing is sent.
		const plain = imageSpecFromDraft({ ...blankImageDraft(), image: 'nginx' });
		assert.equal(plain.ok && Object.hasOwn(imageRequestBody(plain.spec, null).spec as object, 'network'), false);
		// Host network with shared networks is refused like the API.
		const both = imageSpecFromDraft({ ...draft, networks: 'dd' });
		assert.equal(both.ok, false);
		assert.equal(!both.ok && both.field, 'networks');
	});

	test('the container form shows network host read-only and posts it back', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=catalog`);
		const spec = parseImageSpec({ ...RAW_SPEC, network: 'host' });
		assert.ok(spec);
		const props = { mode: 'edit' as const, saveAction: '?/catalogUpdate', sets: [], setsStatus: 'ready' as const, editor: null };
		const html = render(CatalogImageForm, { props: { ...props, entry: entry(spec) } }).body;
		assert.match(html, /data-image-network="host"/);
		assert.match(html, /name="network"[^>]*value="host"|value="host"[^>]*name="network"/);
		const plain = parseImageSpec(RAW_SPEC);
		assert.ok(plain);
		const without = render(CatalogImageForm, { props: { ...props, entry: entry(plain) } }).body;
		assert.equal(without.includes('data-image-network'), false);
		assert.equal(/name="network"/.test(without), false);
	});
});
