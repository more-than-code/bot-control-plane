<script lang="ts">
	import AgentVersion from '$lib/components/AgentVersion.svelte';
	import { ORG_ERROR_ID } from '$lib/org-view';
	import type { Snippet } from 'svelte';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { page } from '$app/state';
	import ConfirmAction from '$lib/components/ConfirmAction.svelte';
	import DataTable from '$lib/components/DataTable.svelte';
	import HostAccessEditor from '$lib/components/HostAccessEditor.svelte';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import RowAction from '$lib/components/RowAction.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import type { Host } from '$lib/api-types';
	import { accessSummary, type AccessActionData, type AccessProject } from '$lib/host-access';
	import { afterHostRemoved, canRenameHost, hostLabel, nextRenameToast, ORG_HOSTS_HEADING_ID, revokedLast, type HostRename } from '$lib/host-name';
	import { toastHostRemoved, toastHostRenamed } from '$lib/success-toast';
	import RenameHost from './RenameHost.svelte';
	import type { Column } from '$lib/table';
	import { actionUrlFrom } from '$lib/url';
	import * as Dialog from '$lib/components/ui/dialog';

	let {
		hosts,
		omitted,
		truncated,
		projects,
		message,
		accessFailure = null,
		renameFailure = null,
		children
	}: {
		hosts: Host[];
		omitted: number;
		truncated: boolean;
		/** The org's projects, for the access editor. */
		projects: AccessProject[];
		message: string | null;
		/** The last failed access write; its host's dialog opens again on it. */
		accessFailure?: AccessActionData | null;
		/** The last failed rename: that host's Rename dialog opens again with the message. */
		renameFailure?: { hostId: string; message: string | null } | null;
		/** The Add a host card, above the list. */
		children?: Snippet;
	} = $props();

	// Revoked hosts after live ones (api-v1 v1.38: a revoked host keeps its name, which a live one may share).
	const rows = $derived(revokedLast(hosts));

	const columns: Column<Host>[] = [
		{ key: 'name', label: 'Name', sortable: true, searchable: true, value: (host) => host.name },
		{ key: 'status', label: 'Status', sortable: true, value: (host) => host.status },
		{ key: 'access', label: 'Project access', searchable: true, value: (host) => accessSummary(host.access) },
		{ key: 'seen', label: 'Last seen', sortable: true, value: (host) => host.last_seen_at },
		{ key: 'version', label: 'Version', value: (host) => host.agent_version },
		{ key: 'actions', label: 'Actions', srLabel: true }
	];

	// The access dialog of the host whose write just failed opens again, with the message inside it.
	let openHostId = $state<string | null>(null);
	$effect(() => {
		if (accessFailure?.accessHostId) openHostId = accessFailure.accessHostId;
	});

	// The rename toast lives here, not in the dialog: the reload after a rename can unmount the row.
	let pendingRename: HostRename | null = null;
	function renameToast(event: Parameters<typeof nextRenameToast>[1]) {
		const next = nextRenameToast(pendingRename, event);
		pendingRename = next.pending;
		if (next.fire) toastHostRenamed(next.fire.from, next.fire.to);
	}

	// Remove (api-v1 v1.40): the reload drops the row, so the dialog is gone by the time this runs.
	function removed(label: string) {
		afterHostRemoved(label, toastHostRemoved, (id) => document.getElementById(id)?.focus());
	}
</script>

{#if message}
	<div id={ORG_ERROR_ID} tabindex="-1" class="mb-4 max-w-full min-w-0 break-words rounded-md focus-visible:ring-3 focus-visible:ring-ring">
		<Notice kind="error">{message}</Notice>
	</div>
{/if}
{@render children?.()}
{#if omitted > 0}
	<p class="text-muted-foreground mb-2 text-sm">{omitted} more {omitted === 1 ? 'row' : 'rows'} left out.</p>
{/if}
{#if truncated}
	<p class="text-muted-foreground mb-2 text-sm">More items not shown</p>
{/if}
<h2 id={ORG_HOSTS_HEADING_ID} tabindex="-1" class="sr-only">Hosts</h2>
<DataTable
	{rows}
	{columns}
	id="hosts"
	labelledBy={ORG_HOSTS_HEADING_ID}
	empty="No hosts in this organization."
	rowKey={(host) => host.id}
	searchLabel="Search host or project"
>
	{#snippet emptyExtra()}
		{#if !message}
			{' '}<a class="text-primary underline underline-offset-4" href="#new-host">Add a host</a>
		{/if}
	{/snippet}
	{#snippet row(host)}
		<tr>
			<td class="p-2 align-middle"><a class="text-primary underline-offset-4 hover:underline" href="/hosts/{host.id}" aria-label={host.status === 'revoked' ? hostLabel(host) : undefined}>{host.name}</a></td>
			<td class="p-2 align-middle"><StatusBadge status={host.status} /></td>
			<td class="p-2 align-middle whitespace-normal">{accessSummary(host.access)}</td>
			<td class="p-2 align-middle"><LocalTime iso={host.last_seen_at} /></td>
			<td class="p-2 align-middle"><AgentVersion {host} /></td>
			<td class="p-2 align-middle">
				<div class="flex items-center gap-1">
					<Dialog.Root
						bind:open={() => openHostId === host.id, (next) => (openHostId = next ? host.id : openHostId === host.id ? null : openHostId)}
					>
						<RowAction label="Manage access {hostLabel(host)}" icon={ShieldCheck}>
							{#snippet trigger(props)}
								<Dialog.Trigger {...props}>
									<ShieldCheck class="size-4" />
								</Dialog.Trigger>
							{/snippet}
						</RowAction>
						<Dialog.Content class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl">
							<Dialog.Header>
								<Dialog.Title>Access to {hostLabel(host)}</Dialog.Title>
								<Dialog.Description>Which projects of this organization may use the host, and at what level.</Dialog.Description>
							</Dialog.Header>
							<HostAccessEditor
								hostId={host.id}
								hostName={host.name}
								rows={host.access ?? []}
								{projects}
								programs={host.programs}
								setAction={actionUrlFrom(page.url, 'setAccess')}
								removeAction={actionUrlFrom(page.url, 'removeAccess')}
								tableId="hostaccess"
								message={accessFailure?.accessHostId === host.id ? accessFailure.message : null}
								conflictProjectId={accessFailure?.accessConflict?.hostId === host.id ? accessFailure.accessConflict.projectId : null}
							/>
						</Dialog.Content>
					</Dialog.Root>
					{#if canRenameHost(host)}
						<RenameHost
							action={actionUrlFrom(page.url, 'renameHost')}
							hostId={host.id}
							name={host.name}
							failed={renameFailure?.hostId === host.id}
							message={renameFailure?.hostId === host.id ? renameFailure.message : null}
							onRenamed={(rename, dialogOpen) => renameToast({ kind: 'succeeded', rename, dialogOpen })}
							onClosed={() => renameToast({ kind: 'closed', hostId: host.id })}
						/>
					{/if}
					{#if host.status !== 'revoked'}
						<ConfirmAction
							closeOnFailure
							failureFocusId={ORG_ERROR_ID}
							id="revoke-host-{host.id}"
							action={actionUrlFrom(page.url, 'revokeCredential')}
							label="Revoke credential"
							confirmLabel="Revoke credential"
							message="Revoke the credential for {host.name}? The agent connection closes for every project that uses it."
							hidden={[{ name: 'host_id', value: host.id }]}
							rowLabel="Revoke credential {host.name}"
						/>
					{:else}
						{@const removedLabel = hostLabel(host)}
						<ConfirmAction
							closeOnFailure
							failureFocusId={ORG_ERROR_ID}
							id="remove-host-{host.id}"
							action={actionUrlFrom(page.url, 'removeHost')}
							label="Remove"
							confirmLabel="Remove"
							message="Remove {removedLabel} from all host lists? Its history and audit records stay. The console cannot bring it back."
							hidden={[{ name: 'host_id', value: host.id }]}
							rowLabel="Remove {hostLabel(host)}"
							icon={Trash2}
							destructive
							onSuccess={() => removed(removedLabel)}
						/>
					{/if}
				</div>
			</td>
		</tr>
	{/snippet}
</DataTable>
