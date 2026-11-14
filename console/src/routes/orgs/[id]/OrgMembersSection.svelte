<script lang="ts">
	import { enhance } from '$app/forms';
	import { page } from '$app/state';
	import UserCog from '@lucide/svelte/icons/user-cog';
	import ConfirmAction from '$lib/components/ConfirmAction.svelte';
	import DataTable from '$lib/components/DataTable.svelte';
	import FormDialog from '$lib/components/FormDialog.svelte';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import RowAction from '$lib/components/RowAction.svelte';
	import type { OrgMember } from '$lib/api-types';
	import { toastMemberAdded } from '$lib/success-toast';
	import type { Column } from '$lib/table';
	import { actionUrlFrom } from '$lib/url';
	import { Button } from '$lib/components/ui/button';
	import * as Card from '$lib/components/ui/card';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import * as Select from '$lib/components/ui/select';

	let {
		members,
		omitted,
		truncated,
		canAdmin,
		canOwner,
		currentUserId,
		message,
		addMemberMessage = null
	}: {
		members: OrgMember[];
		omitted: number;
		truncated: boolean;
		canAdmin: boolean;
		canOwner: boolean;
		currentUserId: string | null;
		message: string | null;
		addMemberMessage?: string | null;
	} = $props();

	const columns = $derived<Column<OrgMember>[]>([
		{ key: 'name', label: 'Name', sortable: true, searchable: true, value: (member) => member.name },
		{ key: 'email', label: 'Email', sortable: true, searchable: true, value: (member) => member.email },
		{ key: 'role', label: 'Role', sortable: true, value: (member) => member.role },
		{ key: 'added', label: 'Added', sortable: true, value: (member) => member.added_at },
		{ key: 'actions', label: 'Actions', srLabel: true }
	]);

	let addedEmail = '';
	const successors = $derived(members.filter((member) => member.role !== 'owner'));
	let ownerId = $state('');
	const ownerLabel = $derived(successors.find((member) => member.user_id === ownerId)?.email ?? '');
</script>

{#if message}
	<Notice kind="error">{message}</Notice>
{/if}
<p class="text-muted-foreground mb-4 text-sm">
	Owners and admins manage the organization's hosts, join tokens and host access, and act as admin on every project in it.
	Members see only the projects they belong to.
</p>
{#if omitted > 0}
	<p class="text-muted-foreground mb-2 text-sm">{omitted} more {omitted === 1 ? 'row' : 'rows'} left out.</p>
{/if}
{#if truncated}
	<p class="text-muted-foreground mb-2 text-sm">More items not shown</p>
{/if}
<h2 id="org-members-heading" class="sr-only">Members</h2>
<DataTable rows={members} {columns} id="members" labelledBy="org-members-heading" empty="No members returned." rowKey={(member) => member.user_id}>
	{#snippet row(member)}
		{@const self = member.user_id === currentUserId}
		<tr>
			<td class="p-2 align-middle">
				{member.name}
				{#if self}
					<span class="text-muted-foreground text-sm">(you)</span>
				{/if}
			</td>
			<td class="p-2 align-middle">{member.email}</td>
			<td class="p-2 align-middle">{member.role}</td>
			<td class="p-2 align-middle"><LocalTime iso={member.added_at} /></td>
			<td class="p-2 align-middle whitespace-normal">
				{#if member.role !== 'owner'}
					<div class="flex flex-wrap items-end gap-2">
						{#if canAdmin}
							<form method="POST" action={actionUrlFrom(page.url, 'changeRole')} class="flex flex-wrap items-end gap-2" use:enhance>
								<Input type="hidden" name="user_id" value={member.user_id} />
								<Label class="sr-only" for="org-member-role-{member.user_id}">Role for {member.email}</Label>
								<Select.Root type="single" name="role" value={member.role === 'admin' ? 'admin' : 'member'}>
									<Select.Trigger id="org-member-role-{member.user_id}" class="w-28">
										<Select.Value />
									</Select.Trigger>
									<Select.Content>
										<Select.Item value="admin">admin</Select.Item>
										<Select.Item value="member">member</Select.Item>
									</Select.Content>
								</Select.Root>
								<RowAction type="submit" label="Change role {member.email}" icon={UserCog} />
							</form>
						{/if}
						{#if canAdmin || self}
							<ConfirmAction
								id="remove-org-member-{member.user_id}"
								action={actionUrlFrom(page.url, 'removeMember')}
								label="Remove"
								confirmLabel={self ? 'Leave organization' : 'Remove'}
								message={self ? 'Leave this organization? Projects you are a member of keep your project role.' : `Remove ${member.email} from this organization? Their project roles stay.`}
								hidden={[{ name: 'user_id', value: member.user_id }]}
								rowLabel={self ? 'Leave organization' : `Remove ${member.email}`}
							/>
						{/if}
					</div>
				{/if}
			</td>
		</tr>
	{/snippet}
</DataTable>

{#if canAdmin}
	<div class="mt-6">
		<FormDialog id="add-org-member" label="Add member" title="Add member" description="The person must have signed in to Deploybot before." failed={addMemberMessage != null} onCloseComplete={() => addedEmail && toastMemberAdded(addedEmail)}>
			{#snippet children(settle, cancel)}
				<form
					method="POST"
					action={actionUrlFrom(page.url, 'addMember')}
					class="grid gap-3"
					use:enhance={({ formData }) => {
						const email = String(formData.get('email') ?? '').trim();
						return async ({ result, update }) => {
							await update();
							addedEmail = result.type === 'redirect' ? email : '';
							settle(result.type);
						};
					}}
				>
					{#if addMemberMessage}
						<Notice kind="error">{addMemberMessage}</Notice>
					{/if}
					<Label class="grid gap-1.5" for="org-member-email">
						Email
						<Input id="org-member-email" name="email" type="email" required maxlength={254} autocomplete="off" />
					</Label>
					<div class="grid gap-1.5">
						<Label for="org-member-new-role">Role</Label>
						<Select.Root type="single" name="role" value="member">
							<Select.Trigger id="org-member-new-role" class="w-full sm:w-40">
								<Select.Value />
							</Select.Trigger>
							<Select.Content>
								<Select.Item value="member">member</Select.Item>
								<Select.Item value="admin">admin</Select.Item>
							</Select.Content>
						</Select.Root>
					</div>
					<div class="flex flex-wrap items-center gap-2">
						<Button type="submit">Add member</Button>
						{@render cancel()}
					</div>
				</form>
			{/snippet}
		</FormDialog>
	</div>
{/if}

{#if canOwner}
	<Card.Root class="mt-4">
		<Card.Header>
			<Card.Title>Transfer ownership</Card.Title>
			<Card.Description>The new owner must already be a member of the organization.</Card.Description>
		</Card.Header>
		<Card.Content>
			<form method="POST" action={actionUrlFrom(page.url, 'transfer')} class="grid gap-3" use:enhance>
				{#if successors.length === 0}
					<p class="text-muted-foreground text-sm">Add another member before transferring ownership.</p>
				{:else}
					<div class="grid gap-1.5">
						<Label for="org-new-owner">New owner</Label>
						<Select.Root type="single" name="user_id" bind:value={ownerId} required>
							<Select.Trigger id="org-new-owner" class="w-full">
								<Select.Value placeholder="Choose a member">{ownerLabel}</Select.Value>
							</Select.Trigger>
							<Select.Content>
								{#each successors as member (member.user_id)}
									<Select.Item value={member.user_id}>{member.name} ({member.email})</Select.Item>
								{/each}
							</Select.Content>
						</Select.Root>
					</div>
					<div>
						<Button type="submit">Transfer ownership</Button>
					</div>
				{/if}
			</form>
		</Card.Content>
	</Card.Root>
{/if}
