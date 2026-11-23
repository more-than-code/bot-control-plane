<script lang="ts">
	import AgentVersion from '$lib/components/AgentVersion.svelte';
	import type { Snippet } from 'svelte';
	import LogOut from '@lucide/svelte/icons/log-out';
	import { page } from '$app/state';
	import ConfirmAction from '$lib/components/ConfirmAction.svelte';
	import DataTable from '$lib/components/DataTable.svelte';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import type { Host } from '$lib/api-types';
	import type { AccessActionData } from '$lib/host-access';
	import { hostLabel, revokedLast } from '$lib/host-name';
	import type { Column } from '$lib/table';
	import { actionUrlFrom } from '$lib/url';
	import { Badge } from '$lib/components/ui/badge';

	let {
		hosts,
		omitted,
		truncated,
		canAdmin,
		canOrgAdmin,
		projectId,
		orgId,
		message,
		hostsMessage,
		accessFailure = null,
		children
	}: {
		hosts: Host[];
		omitted: number;
		truncated: boolean;
		/** Admin+ of this project: may leave a host. */
		canAdmin: boolean;
		/** Owner or admin of the project's org: adds hosts and revokes credentials. */
		canOrgAdmin: boolean;
		projectId: string;
		orgId: string | null;
		message: string | null;
		hostsMessage: string | null;
		/** A failed Leave. A 409 offers to move this project's services to the organization. */
		accessFailure?: AccessActionData | null;
		/** Org-admin-only Add a host card, shown above the list. */
		children?: Snippet;
	} = $props();

	const columns = $derived<Column<Host>[]>([
		{ key: 'name', label: 'Name' },
		{ key: 'access', label: 'Access' },
		{ key: 'status', label: 'Status' },
		{ key: 'seen', label: 'Last seen' },
		{ key: 'version', label: 'Version' },
		...(canAdmin || canOrgAdmin ? [{ key: 'actions', label: 'Actions', srLabel: true }] : [])
	]);
	const conflictHost = $derived(
		accessFailure?.accessConflict ? (hosts.find((host) => host.id === accessFailure.accessConflict?.hostId) ?? null) : null
	);
	const leaveFailureId = 'leave-host-error';
	// Revoked hosts after live ones (api-v1 v1.38: a revoked host keeps its name, which a live one may share).
	const rows = $derived(revokedLast(hosts));
</script>

{#if message}
	<Notice kind="error">{message}</Notice>
{/if}
{#if accessFailure}
	<div id={leaveFailureId} tabindex="-1" class="mb-4 grid gap-2 rounded-md focus-visible:ring-3 focus-visible:ring-ring">
		{#if conflictHost}
			<Notice kind="error">This project still has services on {conflictHost.name}. Move them to the organization and leave?</Notice>
			<div>
				<ConfirmAction
					id="leave-reassign-{conflictHost.id}"
					action={actionUrlFrom(page.url, 'leaveAccess')}
					label="Move services and leave"
					confirmLabel="Move services and leave"
					message="Move this project's services on {conflictHost.name} to the organization and leave the host? They keep running."
					hidden={[
						{ name: 'host_id', value: conflictHost.id },
						{ name: 'project_id', value: projectId },
						{ name: 'reassign', value: '1' }
					]}
				/>
			</div>
		{:else}
			<Notice kind="error">{accessFailure.message}</Notice>
		{/if}
	</div>
{/if}
{@render children?.()}
{#if canAdmin && !canOrgAdmin && !hostsMessage}
	<p class="text-muted-foreground mb-4 text-sm">Ask an org admin to add a host.</p>
{/if}
{#if omitted > 0}
	<p class="text-muted-foreground mb-2 text-sm">{omitted} more {omitted === 1 ? 'row' : 'rows'} left out.</p>
{/if}
{#if truncated}
	<p class="text-muted-foreground mb-2 text-sm">More items not shown</p>
{/if}
<h2 id="hosts-heading" class="sr-only">Hosts</h2>
<DataTable {rows} {columns} id="hosts" labelledBy="hosts-heading" empty="No hosts yet." rowKey={(host) => host.id}>
	{#snippet emptyExtra()}
		{#if !hostsMessage}
			{#if canOrgAdmin}
				{' '}<a class="text-primary underline underline-offset-4" href="#new-host">Add a host</a>
			{:else if !canAdmin}
				{' '}Ask an org admin to add a host.
			{/if}
		{/if}
	{/snippet}
	{#snippet row(host)}
		<tr>
			<td class="p-2 align-middle"><a class="text-primary underline-offset-4 hover:underline" href="/hosts/{host.id}" aria-label={host.status === 'revoked' ? hostLabel(host) : undefined}>{host.name}</a></td>
			<td class="p-2 align-middle">
				{#if host.access_level}
					<Badge variant="outline">{host.access_level}</Badge>
				{:else}
					n/a
				{/if}
			</td>
			<td class="p-2 align-middle"><StatusBadge status={host.status} /></td>
			<td class="p-2 align-middle"><LocalTime iso={host.last_seen_at} /></td>
			<td class="p-2 align-middle"><AgentVersion {host} canUpgrade={canOrgAdmin} compact /></td>
			{#if canAdmin || canOrgAdmin}
				<td class="p-2 align-middle">
					<div class="flex items-center gap-1">
						{#if canAdmin}
							<ConfirmAction
								id="leave-host-{host.id}"
								action={actionUrlFrom(page.url, 'leaveAccess')}
								label="Leave"
								confirmLabel="Leave host"
								message="Remove this project's access to {host.name}? The host stays in the organization."
								hidden={[
									{ name: 'host_id', value: host.id },
									{ name: 'project_id', value: projectId }
								]}
								rowLabel="Leave {hostLabel(host)}"
								icon={LogOut}
								closeOnFailure
								failureFocusId={leaveFailureId}
							/>
						{/if}
						{#if canOrgAdmin && host.org_id === orgId && host.status !== 'revoked'}
							<ConfirmAction
								id="revoke-host-{host.id}"
								action={actionUrlFrom(page.url, 'revokeCredential')}
								label="Revoke credential"
								confirmLabel="Revoke credential"
								message="Revoke the credential for {host.name}? The agent connection closes for every project that uses it."
								hidden={[{ name: 'host_id', value: host.id }]}
								rowLabel="Revoke credential {host.name}"
							/>
						{/if}
					</div>
				</td>
			{/if}
		</tr>
	{/snippet}
</DataTable>
