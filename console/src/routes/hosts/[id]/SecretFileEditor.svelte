<script lang="ts">
	import { enhance } from '$app/forms';
	import { beforeNavigate, invalidateAll } from '$app/navigation';
	import { page } from '$app/state';
	import Save from '@lucide/svelte/icons/save';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import Undo2 from '@lucide/svelte/icons/undo-2';
	import DataTable from '$lib/components/DataTable.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import RowAction from '$lib/components/RowAction.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { untrack } from 'svelte';
	import { keysAfter, secretSaveBody, unsavedLeave, type HostFileKey, type StagedChange } from '$lib/host-files';
	import type { Column } from '$lib/table';
	import { actionUrlFrom } from '$lib/url';
	import SecretKeyDialog from './SecretKeyDialog.svelte';

	/**
	 * A secret host file (spec §2.8): key names with set or unset, never a value. Set value, Remove and Add key
	 * stage changes in this component's memory; Save sends them in one PUT, Discard drops them. A failed Save
	 * keeps them and reads the file again (a 409 means someone else saved first).
	 */
	let {
		orgId,
		name,
		keys,
		latest,
		message = null,
		onStaged
	}: {
		orgId: string;
		name: string;
		keys: HostFileKey[];
		latest: number | null;
		/** The last Save's failure message. */
		message?: string | null;
		/** Whether changes are staged and not saved. */
		onStaged?: (staged: boolean) => void;
	} = $props();

	/** The file this editor was opened for; the page remounts the editor for another file. */
	const owner = untrack(() => name);
	let staged = $state<Record<string, StagedChange>>({});
	/** A navigation away was stopped once because of staged changes. */
	let warned = $state(false);
	let saving = $state(false);
	let conflict = $state(false);
	const count = $derived(Object.keys(staged).length);

	$effect(() => {
		onStaged?.(count > 0);
		if (count === 0) warned = false;
	});

	// Leaving with staged changes: a page unload gets the browser's prompt; a link is stopped once with a note.
	beforeNavigate((navigation) => {
		const decision = unsavedLeave({
			unsaved: count > 0,
			saving,
			type: navigation.type,
			to: navigation.to?.url ?? null,
			here: page.url,
			name: owner,
			warned
		});
		if (decision === 'go') return;
		navigation.cancel();
		if (decision === 'warn') warned = true;
	});

	function stage(key: string, value: string) {
		staged = { ...staged, [key]: { op: 'set', value } };
	}

	function stageRemove(key: string) {
		staged = { ...staged, [key]: { op: 'remove' } };
	}

	function unstage(key: string) {
		const next = { ...staged };
		delete next[key];
		staged = next;
	}

	function discard() {
		staged = {};
		conflict = false;
	}

	/** Existing keys, then keys staged to be added. */
	const rows = $derived<HostFileKey[]>([
		...keys,
		...Object.keys(staged)
			.filter((key) => !keys.some((item) => item.name === key))
			.sort()
			.map((key) => ({ name: key, set: false }))
	]);

	const columns: Column<HostFileKey>[] = [
		{ key: 'name', label: 'Key', sortable: true, searchable: true, value: (row) => row.name },
		{ key: 'set', label: 'Value', sortable: true, value: (row) => (row.set ? 'set' : 'unset') },
		{ key: 'staged', label: 'Change', value: (row) => staged[row.name]?.op ?? '' },
		{ key: 'actions', label: 'Actions', srLabel: true }
	];
</script>

<p class="text-muted-foreground text-sm">
	Values are stored encrypted and never shown. Set value, Remove and Add key are staged here; Save makes one new version, and
	Apply puts the latest version on the host.
</p>
<DataTable
	{rows}
	{columns}
	id="filekeys"
	labelledBy="host-file-heading"
	empty={rows.length === 0 ? 'No keys yet. Add one.' : 'No matching keys.'}
	rowKey={(row) => row.name}
	defaultSort="name"
	searchLabel="Search keys"
>
	{#snippet row(row)}
		{@const change = staged[row.name]}
		<tr data-key={row.name}>
			<td class="p-2 align-middle font-mono">{row.name}</td>
			<td class="p-2 align-middle">
				{#if keys.some((item) => item.name === row.name)}
					<StatusBadge status={row.set ? 'up' : 'unknown'} text={row.set ? 'set' : 'unset'} />
				{:else}
					<span class="text-muted-foreground">new</span>
				{/if}
			</td>
			<td class="p-2 align-middle">
				{#if change}<Badge variant="outline" data-staged={change.op}>{change.op === 'set' ? 'will be set' : 'will be removed'}</Badge>{/if}
			</td>
			<td class="p-2 text-right align-middle">
				<span class="inline-flex items-center gap-1">
					{#if change}
						<RowAction label="Undo {row.name}" icon={Undo2} onclick={() => unstage(row.name)} />
					{:else}
						<SecretKeyDialog key={row.name} onStage={stage} />
						<RowAction
							label="Remove {row.name}"
							icon={Trash2}
							destructive
							blockedReason={keysAfter(keys, { ...staged, [row.name]: { op: 'remove' } }) === 0 ? 'A secret file keeps at least one key.' : null}
							onclick={() => stageRemove(row.name)}
						/>
					{/if}
				</span>
			</td>
		</tr>
	{/snippet}
</DataTable>

<div class="flex flex-wrap items-center gap-2">
	<SecretKeyDialog onStage={stage} />
</div>

{#if count > 0}
	<section class="grid gap-2" aria-labelledby="host-file-staged-heading">
		<h4 id="host-file-staged-heading" class="text-sm font-medium">Staged changes</h4>
		<ul class="text-sm" data-staged-list>
			{#each Object.entries(staged).sort(([a], [b]) => (a < b ? -1 : 1)) as [key, change] (key)}
				<li><span class="font-mono">{key}</span> {change.op === 'set' ? 'will be set' : 'will be removed'}</li>
			{/each}
		</ul>
		{#if warned}
			<Notice kind="info">You have staged changes. Save them, or follow the link again to leave without saving.</Notice>
		{/if}
		{#if conflict}
			<Notice kind="info">The file changed since you opened it (now {latest == null ? 'no version' : `v${latest}`}). Your staged changes are kept; Save puts them on top of it.</Notice>
		{/if}
		{#if message}
			<Notice kind="error">{message}</Notice>
		{/if}
		<form
			method="POST"
			action={actionUrlFrom(page.url, 'saveSecretFile')}
			class="flex flex-wrap items-center gap-2"
			use:enhance={({ formData, cancel }) => {
				const body = saving ? null : secretSaveBody(owner, name, keys, staged);
				if (!body) {
					cancel();
					return;
				}
				// The values join the post here and exist nowhere else in the page's markup.
				formData.set('changes', JSON.stringify(body));
				saving = true;
				return async ({ result, update }) => {
					try {
						await update({ reset: false });
						if (result.type === 'success') discard();
						else if (result.type === 'failure') {
							conflict = result.status === 409;
							await invalidateAll();
						}
					} finally {
						saving = false;
					}
				};
			}}
		>
			<Input type="hidden" name="org" value={orgId} />
			<Input type="hidden" name="name" value={name} />
			<Input type="hidden" name="base_version" value={String(latest ?? 0)} />
			<Button type="submit" aria-disabled={saving ? 'true' : undefined} class={saving ? 'opacity-50' : undefined}
				><Save class="size-4" />Save {count} {count === 1 ? 'change' : 'changes'}</Button
			>
			<Button type="button" variant="outline" onclick={discard}>Discard</Button>
		</form>
	</section>
{/if}
