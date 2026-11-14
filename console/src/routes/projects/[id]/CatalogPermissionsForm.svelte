<script lang="ts">
	import { enhance } from '$app/forms';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { saveRedirectOutcome } from '$lib/permissions-read';
	import { untrack, type Snippet } from 'svelte';
	import { Button } from '$lib/components/ui/button';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import * as Select from '$lib/components/ui/select';
	import * as Sheet from '$lib/components/ui/sheet';
	import Notice from '$lib/components/Notice.svelte';
	import type { CatalogGrant, CatalogGrantAction, Member } from '$lib/api-types';
	import type { CatalogPermissionsState } from '$lib/project-action';
	import { GRANT_ACTIONS, IMAGE_GRANT_ACTIONS } from '$lib/catalog';

	type Boxes = Record<CatalogGrantAction, boolean>;

	type MemberRow = { user: string } & Boxes;

	let {
		action,
		name,
		grants,
		members,
		message = null,
		loaded,
		includeDeploy = false,
		onSaved,
		onFailed,
		cancel
	}: {
		action: string;
		name: string;
		grants: CatalogGrant[];
		members: Member[];
		message?: string | null;
		loaded: boolean;
		includeDeploy?: boolean;
		/** The save went through: the drawer closes (and returns focus to the row's ⋯ button). Nothing reloads. */
		onSaved?: () => void;
		/** The save was refused: the drawer shows the message and keeps the boxes. */
		onFailed?: (state: CatalogPermissionsState) => void;
		cancel?: Snippet;
	} = $props();

	const actions = $derived(includeDeploy ? IMAGE_GRANT_ACTIONS : GRANT_ACTIONS);

	function boxesFor(subject: string): Boxes {
		const grant = grants.find((item) => item.subject === subject);
		return {
			run: grant?.actions.includes('run') ?? false,
			stop: grant?.actions.includes('stop') ?? false,
			restart: grant?.actions.includes('restart') ?? false,
			logs: grant?.actions.includes('logs') ?? false,
			deploy: grant?.actions.includes('deploy') ?? false
		};
	}

	function memberRows(): MemberRow[] {
		return grants
			.filter((grant) => grant.subject.startsWith('user:'))
			.map((grant) => ({ user: grant.subject.slice('user:'.length), ...boxesFor(grant.subject) }));
	}

	let admin = $state(untrack(() => boxesFor('role:admin')));
	let viewer = $state(untrack(() => boxesFor('role:viewer')));
	let rows = $state<MemberRow[]>(untrack(memberRows));
	const choosable = $derived(members.filter((member) => member.role !== 'owner'));

	function memberLabel(user: string): string {
		if (user === '') return 'Choose a member';
		const name = choosable.find((member) => member.user_id === user)?.name.trim();
		return name ? name : user;
	}

	function addMember() {
		if (rows.length >= 100) return;
		rows = [...rows, { user: '', run: false, stop: false, restart: false, logs: false, deploy: false }];
	}

	function removeMember(index: number) {
		rows = rows.filter((_, item) => item !== index);
	}
</script>

<!--
	The save action redirects back to the page on success. The grants are shown only in this drawer, so nothing on the
	page changes: the drawer closes instead of following the redirect, and the page load does not re-run. Any other
	redirect is not a save (`saveRedirectOutcome`).
-->
<form
	method="POST"
	{action}
	class="flex min-h-0 flex-1 flex-col"
	use:enhance={() => {
		return async ({ result, update }) => {
			if (result.type === 'redirect') {
				const next = saveRedirectOutcome(result.location, page.url);
				if (next.kind === 'saved') {
					onSaved?.();
					return;
				}
				// An expired session (sign-in) is followed; nothing is reported as saved.
				if (next.kind === 'navigate') {
					await goto(next.path);
					return;
				}
			}
			const failed = result.type === 'failure' ? (result.data as { permissions?: CatalogPermissionsState } | undefined)?.permissions : undefined;
			if (failed && onFailed) {
				onFailed(failed);
				return;
			}
			await update({ reset: false });
		};
	}}
>
	<div class="grid min-h-0 flex-1 content-start gap-3 overflow-y-auto scroll-py-4 px-4 pb-4" data-slot="form-body">
		{#if message}
			<Notice kind="error">{message}</Notice>
		{/if}
		<Input type="hidden" name="name" value={name} />
		{#if includeDeploy}
			<Input type="hidden" name="entry_kind" value="image" />
		{/if}
		<!-- A failed first read has no grants to edit: only its Notice and Cancel. -->
		{#if loaded}
			<div class="grid gap-2">
				<p class="text-sm font-medium">Admins</p>
				<div class="flex flex-wrap gap-3">
					{#each actions as actionName (actionName)}
						<Label class="flex items-center gap-2">
							<Checkbox name="admin_{actionName}" value="1" checked={admin[actionName]} onCheckedChange={(value) => (admin[actionName] = value)} />
							{actionName}
						</Label>
					{/each}
				</div>
			</div>
			<div class="grid gap-2">
				<p class="text-sm font-medium">Viewers</p>
				<div class="flex flex-wrap gap-3">
					{#each actions as actionName (actionName)}
						<Label class="flex items-center gap-2">
							<Checkbox name="viewer_{actionName}" value="1" checked={viewer[actionName]} onCheckedChange={(value) => (viewer[actionName] = value)} />
							{actionName}
						</Label>
					{/each}
				</div>
			</div>
			<div class="grid gap-2">
				<p class="text-sm font-medium">Members</p>
				{#each rows as row, index (index)}
					<div class="grid min-w-0 gap-2">
						<Label class="sr-only" for="catalog-member-{index}">Member {index + 1}</Label>
						<Select.Root type="single" name="member_{index}_user" bind:value={rows[index].user}>
							<Select.Trigger id="catalog-member-{index}" class="w-full min-w-0">
								<Select.Value placeholder="Choose a member" class="min-w-0 truncate">{memberLabel(row.user)}</Select.Value>
							</Select.Trigger>
							<Select.Content>
								{#each choosable as member (member.user_id)}
									<Select.Item value={member.user_id} label={member.name}>{member.name}</Select.Item>
								{/each}
								{#if row.user !== '' && !choosable.some((member) => member.user_id === row.user)}
									<Select.Item value={row.user} label={row.user}>{row.user}</Select.Item>
								{/if}
							</Select.Content>
						</Select.Root>
						<div class="flex flex-wrap items-center gap-3">
							{#each actions as actionName (actionName)}
								<Label class="flex items-center gap-2">
									<Checkbox name="member_{index}_{actionName}" value="1" checked={row[actionName]} onCheckedChange={(value) => (row[actionName] = value)} />
									{actionName}
								</Label>
							{/each}
							<Button type="button" variant="outline" onclick={() => removeMember(index)}>Remove</Button>
						</div>
					</div>
				{/each}
				<Button type="button" variant="outline" disabled={rows.length >= 100} onclick={addMember}>Add member</Button>
			</div>
		{/if}
	</div>
	<Sheet.Footer class="bg-popover flex-row flex-wrap items-center border-t">
		{#if loaded}
			<Button type="submit">Save</Button>
		{/if}
		{#if cancel}
			{@render cancel()}
		{/if}
	</Sheet.Footer>
</form>
