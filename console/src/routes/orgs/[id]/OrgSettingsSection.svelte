<script lang="ts">
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
</script>

{#if message}
	<Notice kind="error">{message}</Notice>
{/if}
<Card.Root class="mb-4">
	<Card.Header>
		<Card.Title>Rename</Card.Title>
	</Card.Header>
	<Card.Content>
		<form method="POST" action={actionUrlFrom(page.url, 'rename')} class="grid gap-3" use:enhance>
			<Label class="grid gap-1.5" for="org-rename">
				Organization name
				<Input id="org-rename" name="name" value={org.name} required maxlength={80} autocomplete="off" />
			</Label>
			<div>
				<Button type="submit">Rename organization</Button>
			</div>
		</form>
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
