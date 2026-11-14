<script lang="ts">
	import { untrack } from 'svelte';
	import { deserialize } from '$app/forms';
	import { goto } from '$app/navigation';
	import { Button } from '$lib/components/ui/button';
	import * as Sheet from '$lib/components/ui/sheet';
	import DrawerResize from '$lib/components/DrawerResize.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import type { Member } from '$lib/api-types';
	import { restoreFocusTo } from '$lib/dialog-focus';
	import { readPermissions } from '$lib/permissions-read';
	import type { CatalogPermissionsState } from '$lib/project-action';
	import CatalogPermissionsForm from './CatalogPermissionsForm.svelte';

	let {
		action,
		readAction,
		name,
		members,
		returnFocusId = null,
		includeDeploy = false,
		onClosed
	}: {
		/** The existing `catalogPermissions` save action. */
		action: string;
		/** The `catalogPermissionsRead` action: the drawer reads the entry's grants through it when it opens. */
		readAction: string;
		name: string;
		members: Member[];
		/** The row's ⋯ button: focus goes back there on close and after a save. */
		returnFocusId?: string | null;
		includeDeploy?: boolean;
		/** Runs once the drawer has finished closing; the section then unmounts it. */
		onClosed: () => void;
	} = $props();

	// Mounted open. Closing never navigates: the page and its URL stay as they are.
	let open = $state(true);
	/** Null while the read runs. */
	let view = $state<CatalogPermissionsState | null>(null);

	// One read per mount: the section remounts the drawer for each opening, and a later URL change does not re-read.
	$effect(() => {
		const controller = new AbortController();
		const deps = untrack(() => ({ fetch, deserialize, action: readAction, origin: location.origin }));
		void readPermissions(deps, untrack(() => name), controller.signal).then((read) => {
			if (controller.signal.aborted) return;
			if (read.kind === 'redirect') void goto(read.path);
			else view = read.state;
		});
		return () => controller.abort();
	});

	function onOpenChangeComplete(isOpen: boolean) {
		if (!isOpen) onClosed();
	}

	function onCloseAutoFocus(event: Event) {
		if (returnFocusId) restoreFocusTo(returnFocusId)(event);
	}

	/** A refused save keeps the drawer open with the boxes as ticked, the message, and Save. */
	function onFailed(state: CatalogPermissionsState) {
		view = { ...state, name, loaded: true };
	}
</script>

<!-- A drawer from the right, full width on a phone. The grants scroll between the header and the pinned Save footer. -->
<Sheet.Root bind:open {onOpenChangeComplete}>
	<Sheet.Content side="right" class="data-[side=right]:w-full data-[side=right]:sm:max-w-2xl" {onCloseAutoFocus}>
		{#snippet after()}<DrawerResize type="permissions" />{/snippet}
		<Sheet.Header class="pr-12 pb-0">
			<Sheet.Title class="break-all">Permissions for {name}</Sheet.Title>
			<Sheet.Description>The owner always has every action. Admins, viewers, and members get only what is checked.</Sheet.Description>
		</Sheet.Header>
		{#if view}
			<CatalogPermissionsForm
				{action}
				{name}
				grants={view.grants}
				{members}
				message={view.message}
				loaded={view.loaded}
				{includeDeploy}
				onSaved={() => (open = false)}
				{onFailed}
			>
				{#snippet cancel()}
					<Button type="button" variant="outline" onclick={() => (open = false)}>Cancel</Button>
				{/snippet}
			</CatalogPermissionsForm>
		{:else}
			<div class="px-4 pb-4">
				<Notice kind="info">Loading permissions…</Notice>
			</div>
		{/if}
	</Sheet.Content>
</Sheet.Root>
