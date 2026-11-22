<script lang="ts">
	import { untrack } from 'svelte';
	import { deserialize } from '$app/forms';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import ConfirmAction from '$lib/components/ConfirmAction.svelte';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import { Badge } from '$lib/components/ui/badge';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import * as Dialog from '$lib/components/ui/dialog';
	import { restoreFocusTo } from '$lib/dialog-focus';
	import {
		confirmCode,
		deleteBlocked,
		deleteMessage,
		readImageVersions,
		shortDigest,
		splitStale,
		versionStatus,
		type ImageVersion,
		type VersionsState
	} from '$lib/image-versions';
	import { toastVersionDeleted } from '$lib/success-toast';

	/**
	 * Image versions of a ghcr.io entry (api-v1 v1.45): every package version with its tags, age and holders. Stale
	 * versions (unused and not updated for 14 days) are collapsed. An org owner or admin with a GitHub login that may
	 * delete can delete one unheld version, after typing the first 12 characters of its digest.
	 */
	let {
		image,
		entry,
		projectId,
		readAction,
		deleteAction,
		returnFocusId = null,
		onClosed
	}: {
		/** The ghcr.io repository, without a tag or digest. */
		image: string;
		entry: string;
		projectId: string;
		/** `?/catalogImageVersions`. */
		readAction: string;
		/** `?/catalogImageVersionDelete`. */
		deleteAction: string;
		returnFocusId?: string | null;
		/** Runs once the dialog has finished closing; the row then unmounts it. */
		onClosed: () => void;
	} = $props();

	let open = $state(true);
	let view = $state<VersionsState>({ state: 'loading' });
	let showStale = $state(false);
	let controller: AbortController | null = null;

	function load() {
		controller?.abort();
		const current = new AbortController();
		controller = current;
		view = { state: 'loading' };
		const deps = { fetch, deserialize, action: readAction, origin: location.origin };
		void readImageVersions(deps, image, current.signal).then((read) => {
			if (current.signal.aborted) return;
			if (read.kind === 'redirect') void goto(read.path);
			else view = read.state;
		});
	}

	$effect(() => {
		untrack(load);
		return () => controller?.abort();
	});

	const groups = $derived(view.state === 'ready' ? splitStale(view.list.items) : { current: [], stale: [] });
	const failure = $derived.by(() => {
		const data = page.form as { imageVersionDelete?: { message?: string | null } } | null;
		return data?.imageVersionDelete?.message ?? null;
	});
	const noticeId = 'image-versions-error';

	function onOpenChangeComplete(isOpen: boolean) {
		if (!isOpen) onClosed();
	}

	function onCloseAutoFocus(event: Event) {
		if (returnFocusId) restoreFocusTo(returnFocusId)(event);
	}

	function deleted(version: ImageVersion) {
		toastVersionDeleted(shortDigest(version.digest), version.tags.length);
		load();
	}
</script>

{#snippet versionRow(version: ImageVersion)}
	{@const blocked = view.state === 'ready' ? deleteBlocked(view.list, version, projectId) : null}
	<li class="flex flex-wrap items-start justify-between gap-2 border-b py-2 last:border-b-0" data-version={version.version_id}>
		<div class="grid min-w-0 flex-1 gap-1">
			<p class="flex min-w-0 flex-wrap items-center gap-1 text-sm">
				{#each version.tags as tag (tag)}
					<Badge variant="secondary" class="font-mono">{tag}</Badge>
				{:else}
					<span class="text-muted-foreground">No tags</span>
				{/each}
				{#if version.release}
					<Badge variant="outline">release</Badge>
				{/if}
			</p>
			<p class="text-muted-foreground min-w-0 text-xs break-all">
				<span class="font-mono" title={version.digest}>{shortDigest(version.digest)}</span>
				· updated <LocalTime iso={version.updated_at} />
			</p>
			<p class="min-w-0 text-xs break-words" data-status>{versionStatus(version, projectId)}</p>
		</div>
		{#if blocked == null}
			<ConfirmAction
				id="delete-version-{version.version_id}"
				action={deleteAction}
				label="Delete"
				confirmLabel="Delete version"
				rowLabel="Delete version {shortDigest(version.digest)}"
				message={deleteMessage(version)}
				messageClass="wrap-anywhere"
				hidden={[
					{ name: 'image', value: image },
					{ name: 'version_id', value: String(version.version_id) },
					{ name: 'digest', value: version.digest }
				]}
				typeToConfirm={{ name: 'confirm', expected: confirmCode(version.digest), label: `Type ${confirmCode(version.digest)} to confirm` }}
				closeOnFailure
				failureFocusId={noticeId}
				onSuccess={() => deleted(version)}
			/>
		{/if}
	</li>
{/snippet}

<Dialog.Root bind:open {onOpenChangeComplete}>
	<Dialog.Content class="flex max-h-[calc(100dvh-2rem)] flex-col overflow-y-auto sm:max-w-2xl" {onCloseAutoFocus}>
		<Dialog.Header class="pr-8">
			<Dialog.Title>Image versions of {entry}</Dialog.Title>
			<Dialog.Description class="max-w-full min-w-0 break-all">{image}</Dialog.Description>
		</Dialog.Header>
		<div class="grid min-w-0 gap-3">
			{#if failure}
				<div id={noticeId} tabindex="-1" class="max-w-full min-w-0 break-words">
					<Notice kind="error">{failure}</Notice>
				</div>
			{/if}
			{#if view.state === 'loading'}
				<p class="text-muted-foreground text-sm">Loading…</p>
			{:else if view.state === 'failed'}
				<div class="max-w-full min-w-0 break-words">
					<Notice kind="error">{view.message}</Notice>
				</div>
			{:else}
				{#if view.list.can_delete_reason}
					<p class="text-muted-foreground text-sm break-words" data-delete-reason>{view.list.can_delete_reason}</p>
				{/if}
				{#if view.list.items.length === 0}
					<p class="text-muted-foreground text-sm">No versions.</p>
				{/if}
				<ul class="min-w-0" aria-label="Current versions">
					{#each groups.current as version (version.version_id)}
						{@render versionRow(version)}
					{/each}
				</ul>
				{#if groups.stale.length > 0}
					<Button type="button" variant="outline" size="sm" aria-expanded={showStale} onclick={() => (showStale = !showStale)}>
						{showStale ? 'Hide' : 'Show'}
						{groups.stale.length} stale {groups.stale.length === 1 ? 'version' : 'versions'}
					</Button>
					{#if showStale}
						<ul class="min-w-0" aria-label="Stale versions">
							{#each groups.stale as version (version.version_id)}
								{@render versionRow(version)}
							{/each}
						</ul>
					{/if}
				{/if}
				{#if view.list.truncated}
					<p class="text-muted-foreground text-xs">This package has more versions than this list shows.</p>
				{/if}
			{/if}
		</div>
		<div>
			<Dialog.Close type="button" class={buttonVariants({ variant: 'outline' })}>Close</Dialog.Close>
		</div>
	</Dialog.Content>
</Dialog.Root>
