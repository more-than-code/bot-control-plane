<script lang="ts">
	import Plug from '@lucide/svelte/icons/plug';
	import * as AlertDialog from '$lib/components/ui/alert-dialog';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';

	/**
	 * Connect GitHub or Reconnect (api-v1 v1.29 item 2). A plain form post: the action answers with a redirect to
	 * github.com, which an enhanced form cannot follow. Over a manual ghcr.io login it asks first, because the
	 * connection replaces that login.
	 */
	let {
		action,
		label = 'Connect GitHub',
		replaces = null,
		size = 'sm',
		deletePackages = false
	}: {
		/** `?/connectGitHub` on the project page. */
		action: string;
		label?: 'Connect GitHub' | 'Reconnect' | 'Allow deleting image versions';
		/** The username of a manual ghcr.io login this connection would replace. */
		replaces?: string | null;
		size?: 'sm' | 'default';
		/** api-v1 v1.45: the start also asks GitHub for delete:packages. */
		deletePackages?: boolean;
	} = $props();

	let open = $state(false);
</script>

{#if replaces == null}
	<form method="POST" {action} class="inline">
		{#if deletePackages}
			<Input type="hidden" name="delete_packages" value="1" />
		{/if}
		<Button type="submit" variant="outline" {size}>
			<Plug class="size-4" />
			{label}
		</Button>
	</form>
{:else}
	<AlertDialog.Root bind:open>
		<AlertDialog.Trigger class={buttonVariants({ variant: 'outline', size })}>
			<Plug class="size-4" />
			{label}
		</AlertDialog.Trigger>
		<AlertDialog.Content>
			<AlertDialog.Header>
				<AlertDialog.Title>Replace the ghcr.io login?</AlertDialog.Title>
				<AlertDialog.Description class="max-w-full min-w-0 break-words">
					ghcr.io has a login typed by hand ({replaces}). Connecting GitHub replaces it with your GitHub login. The old token is not revoked anywhere.
				</AlertDialog.Description>
			</AlertDialog.Header>
			<form method="POST" {action} class="flex flex-wrap justify-end gap-2">
				<AlertDialog.Cancel type="button">Cancel</AlertDialog.Cancel>
				<Button type="submit">
					<Plug class="size-4" />
					Connect GitHub
				</Button>
			</form>
		</AlertDialog.Content>
	</AlertDialog.Root>
{/if}
