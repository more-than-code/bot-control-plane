<script lang="ts">
	import Bell from '@lucide/svelte/icons/bell';
	import BellOff from '@lucide/svelte/icons/bell-off';
	import * as DropdownMenu from '$lib/components/ui/dropdown-menu';
	import {
		BLOCKED_TEXT,
		browserNotifications,
		browserPrefs,
		DESKTOP_LABEL,
		readPref,
		switchView,
		toggleDesktop,
		type SwitchView
	} from '$lib/desktop-notify';

	/**
	 * The account menu's "Desktop notifications" switch (spec-console-live-updates §0 item 2, §2.6). Off by default;
	 * turning it on asks the browser's permission from this click only. Its state is read at once (the menu content
	 * mounts only when open, so there is no pop-in) and bound through `toggleDesktop`, so a dismissed prompt or a
	 * failed save shows off. Hidden without browser support; "Blocked in this browser's settings" once the browser
	 * denied. The rules are `$lib/desktop-notify.ts`.
	 */
	let view = $state<SwitchView>(switchView(browserNotifications(), readPref(browserPrefs())));

	async function changed(next: boolean): Promise<void> {
		view = await toggleDesktop(browserNotifications(), browserPrefs(), next);
	}
</script>

{#if view.shown}
	<DropdownMenu.Separator />
	{#if view.blocked}
		<!-- An item, not a disabled one: arrow keys reach it, it reads at full opacity, and choosing it keeps the menu open. -->
		<DropdownMenu.Item
			data-desktop-notify="blocked"
			aria-label="{DESKTOP_LABEL}: {BLOCKED_TEXT}"
			onSelect={(event) => event.preventDefault()}
		>
			<BellOff class="size-4" />
			<span class="grid min-w-0">
				<span>{DESKTOP_LABEL}</span>
				<span class="text-muted-foreground text-xs">{BLOCKED_TEXT}</span>
			</span>
		</DropdownMenu.Item>
	{:else}
		<DropdownMenu.CheckboxItem bind:checked={() => view.checked, (next) => void changed(next)} data-desktop-notify="switch">
			<Bell class="size-4" />{DESKTOP_LABEL}
			<!-- The vendored indicator shows a check only when on; this word shows the state either way. -->
			<span data-desktop-notify-state class="text-muted-foreground ml-auto pl-2 text-xs">{view.checked ? 'On' : 'Off'}</span>
		</DropdownMenu.CheckboxItem>
	{/if}
{/if}
