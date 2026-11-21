<script lang="ts">
	// Test fixture: the account menu, open and rendered in place (no portal), holding only the desktop
	// notifications switch, so a server render shows the real item.
	import * as DropdownMenu from '../../src/lib/components/ui/dropdown-menu';
	import DesktopNotifySwitch from '../../src/lib/components/DesktopNotifySwitch.svelte';
</script>

<DropdownMenu.Root open={true}>
	<DropdownMenu.Trigger>Account</DropdownMenu.Trigger>
	<DropdownMenu.Content portalProps={{ disabled: true }} forceMount>
		<DesktopNotifySwitch />
	</DropdownMenu.Content>
</DropdownMenu.Root>
