import assert from 'node:assert/strict';
import { test } from 'node:test';
import { render } from 'svelte/server';
import Tabs from '../src/lib/components/Tabs.svelte';

// The project strip (no Join tokens since api-v1 v1.24) and the org strip's Projects and Join tokens tabs.
const items = [
	{ key: 'projects', label: 'Projects', href: '/orgs/home?tab=projects' },
	{ key: 'hosts', label: 'Hosts', href: '/projects/lab?tab=hosts' },
	{ key: 'catalog', label: 'Catalog & services', href: '/projects/lab?tab=catalog' },
	{ key: 'members', label: 'Members', href: '/projects/lab?tab=members' },
	{ key: 'tokens', label: 'Join tokens', href: '/orgs/home?tab=tokens' },
	{ key: 'settings', label: 'Settings', href: '/projects/lab?tab=settings' }
];

test('each project section tab shows an icon after its label', () => {
	const html = render(Tabs, { props: { items, current: 'catalog', label: 'Project sections' } }).body;
	const icons = [
		['Projects', 'lucide-folder-kanban'],
		['Hosts', 'lucide-server'],
		['Catalog &amp; services', 'lucide-library'],
		['Members', 'lucide-users'],
		['Join tokens', 'lucide-key-round'],
		['Settings', 'lucide-settings']
	] as const;
	for (const [label, icon] of icons) {
		const at = html.indexOf(`>${label}`);
		assert.ok(at >= 0, label);
		const link = html.slice(html.lastIndexOf('<a', at), html.indexOf('</a>', at));
		assert.equal(link.includes(icon), true, `${label} icon`);
		assert.equal(link.indexOf(label) < link.indexOf(icon), true, `${label} icon follows the label`);
	}
});
