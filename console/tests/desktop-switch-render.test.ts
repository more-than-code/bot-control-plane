import assert from 'node:assert/strict';
import { test } from 'node:test';
import { render } from 'svelte/server';

// spec-console-live-updates §0 item 2: the account-menu switch, rendered by the real component inside an open
// menu. The browser's Notification and localStorage are stand-ins set before the component reads them (this file
// runs in its own process).

const g = globalThis as Record<string, unknown>;
const prefs = new Map<string, string>();
g.window = globalThis;
g.localStorage = { getItem: (key: string) => prefs.get(key) ?? null, setItem: (key: string, value: string) => void prefs.set(key, value) };

const { default: SwitchInMenu } = await import('./fixtures/SwitchInMenu.svelte');

function renderWith(permission: string | null, pref: 'on' | null): string {
	if (permission == null) delete g.Notification;
	else g.Notification = { permission, requestPermission: async () => permission };
	prefs.clear();
	if (pref) prefs.set('deploybot:desktop-notifications', pref);
	return render(SwitchInMenu).body;
}

function tagOf(body: string, marker: string): string {
	const at = body.indexOf(marker);
	assert.notEqual(at, -1, marker);
	return body.slice(body.lastIndexOf('<', at), body.indexOf('>', at) + 1);
}

test('off by default: an unchecked checkbox item, read at render (no pop-in)', () => {
	const body = renderWith('default', null);
	const item = tagOf(body, 'data-desktop-notify="switch"');
	assert.match(item, /role="menuitemcheckbox"/);
	assert.match(item, /aria-checked="false"/);
	assert.ok(body.includes('Desktop notifications'));
	assert.match(body, /data-desktop-notify-state[^>]*>Off</);
	assert.equal(indicatorHasCheck(body), false);
});

/** The vendored item's indicator span holds the Check icon (path "M20 6 9 17l-5-5") only when checked. */
function indicatorHasCheck(body: string): boolean {
	const at = body.indexOf('data-slot="dropdown-menu-checkbox-item-indicator"');
	assert.notEqual(at, -1, 'the indicator renders');
	return body.slice(at, body.indexOf('</span>', at)).includes('M20 6 9 17l-5-5');
}

test('granted and on: checked, with the check and "On" visible', () => {
	const body = renderWith('granted', 'on');
	const item = tagOf(body, 'data-desktop-notify="switch"');
	assert.match(item, /aria-checked="true"/);
	assert.match(item, /data-state="checked"/);
	assert.equal(indicatorHasCheck(body), true);
	assert.match(body, /data-desktop-notify-state[^>]*>On</);
});

test('granted but the choice is off: unchecked', () => {
	const item = tagOf(renderWith('granted', null), 'data-desktop-notify="switch"');
	assert.match(item, /aria-checked="false"/);
});

test('denied: a menu item (arrow keys reach it) that reads as information, not disabled', () => {
	const body = renderWith('denied', 'on');
	const item = tagOf(body, 'data-desktop-notify="blocked"');
	assert.match(item, /role="menuitem"/);
	assert.match(item, /aria-label="Desktop notifications: Blocked in this browser&#39;s settings"|aria-label="Desktop notifications: Blocked in this browser's settings"/);
	// Not disabled: no data-disabled attribute (the class list names it only as a variant), full opacity.
	assert.doesNotMatch(item, /\sdata-disabled(=|\s|>)|aria-disabled="true"/);
	assert.equal(body.includes('data-desktop-notify="switch"'), false);
});

test('no Notification in this browser: nothing', () => {
	const body = renderWith(null, 'on');
	assert.equal(body.includes('data-desktop-notify'), false);
	assert.equal(body.includes('Desktop notifications'), false);
});
