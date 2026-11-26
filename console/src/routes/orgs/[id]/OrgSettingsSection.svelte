<script lang="ts">
	import { createDraftFollower } from '$lib/draft-field';
	import { untrack } from 'svelte';
	import { ORG_ERROR_ID } from '$lib/org-view';
	import { enhance } from '$app/forms';
	import { page } from '$app/state';
	import ConfirmAction from '$lib/components/ConfirmAction.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import type { Org } from '$lib/api-types';
	import { actionUrlFrom } from '$lib/url';
	import { Button } from '$lib/components/ui/button';
	import * as Card from '$lib/components/ui/card';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';

	let { org, canOwner, message }: { org: Org; canOwner: boolean; message: string | null } = $props();

	// The rename field keeps what was typed across Refresh; it follows the name only when the name changes.
	let renameDraft = $state(untrack(() => org.name));
	const followName = createDraftFollower(untrack(() => org.name));
	$effect(() => {
		const next = followName(org.name, untrack(() => renameDraft));
		if (next !== untrack(() => renameDraft)) renameDraft = next;
	});
</script>

{#if message}
	<div id={ORG_ERROR_ID} tabindex="-1" class="mb-4 max-w-full min-w-0 break-words rounded-md focus-visible:ring-3 focus-visible:ring-ring">
		<Notice kind="error">{message}</Notice>
	</div>
{/if}
<Card.Root class="mb-4">
	<Card.Header>
		<Card.Title>Rename</Card.Title>
	</Card.Header>
	<Card.Content>
		<form method="POST" action={actionUrlFrom(page.url, 'rename')} class="grid gap-3" use:enhance>
			<Label class="grid gap-1.5" for="org-rename">
				Organization name
				<Input id="org-rename" name="name" bind:value={renameDraft} required maxlength={80} autocomplete="off" />
			</Label>
			<div>
				<Button type="submit">Rename organization</Button>
			</div>
		</form>
	</Card.Content>
</Card.Root>
<!-- api-v1 v1.58: the metrics_retention problem links here (#metrics-retention). -->
<Card.Root id="metrics-retention" class="mb-4 scroll-mt-4">
	<Card.Header>
		<Card.Title>Metrics retention</Card.Title>
		<Card.Description>
			Only relevant if this organization owns the host that runs this Deploybot server. Deploybot then checks every hour that its database deletes
			metrics older than 7 days, and tells this organization's owners and admins in the bell if it doesn't.
		</Card.Description>
	</Card.Header>
	<Card.Content class="text-sm">
		<p>
			The problem's detail names the affected tables. Old metrics that are not deleted can fill the database host's disk. The fix is on that host:
			check TimescaleDB's retention jobs (<code class="bg-muted rounded px-1 py-0.5 font-mono break-all">timescaledb_information.job_stats</code>),
			its worker limit (<code class="bg-muted rounded px-1 py-0.5 font-mono break-all">timescaledb.max_background_workers</code>), and that the job
			owner can connect. The problem clears itself within an hour of the jobs succeeding again.
		</p>
	</Card.Content>
</Card.Root>
{#if canOwner}
	<Card.Root>
		<Card.Header>
			<Card.Title>Delete</Card.Title>
			<Card.Description>Deleting the organization revokes every host's credential and deletes its projects.</Card.Description>
		</Card.Header>
		<Card.Content>
			<ConfirmAction
				closeOnFailure
				failureFocusId={ORG_ERROR_ID}
				id="delete-org"
				action={actionUrlFrom(page.url, 'deleteOrg')}
				label="Delete organization"
				confirmLabel="Delete organization"
				message="Delete {org.name}? This revokes every host's credential and deletes every project in it."
				typeToConfirm={{ name: 'confirm', expected: org.name, label: `Type ${org.name} to confirm` }}
			/>
		</Card.Content>
	</Card.Root>
{:else}
	<p class="text-muted-foreground text-sm">Only the owner can delete this organization.</p>
{/if}
