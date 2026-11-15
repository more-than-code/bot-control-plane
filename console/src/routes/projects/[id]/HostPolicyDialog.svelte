<script lang="ts">
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import CommandBlock from '$lib/components/CommandBlock.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import * as Dialog from '$lib/components/ui/dialog';
	import { buttonVariants } from '$lib/components/ui/button';
	import type { HostPolicySnippet } from '$lib/settings-auto';
	import FieldsPost from './FieldsPost.svelte';

	/**
	 * Allow on host (api-v1 v1.29 item 8): the runner.toml lines and the install.sh command for the host's root,
	 * with Copy. Deploybot never edits runner.toml; the person passes this to whoever has root on the host.
	 */
	let {
		action,
		entry,
		hostId,
		hostName = null
	}: {
		/** `?/hostPolicySnippet`. */
		action: string;
		entry: string;
		hostId: string;
		hostName?: string | null;
	} = $props();

	let open = $state(false);
	let snippet = $state<HostPolicySnippet | null>(null);
	let message = $state<string | null>(null);
	let post = $state<FieldsPost | null>(null);

	$effect(() => {
		if (!open || snippet != null) return;
		message = null;
		post?.send({ entry, host_id: hostId });
	});

	function take(_fields: Record<string, string>, data: Record<string, unknown>) {
		const got = (data.hostPolicy as HostPolicySnippet | null | undefined) ?? null;
		snippet = got;
		message = got ? null : typeof data.hostPolicyMessage === 'string' ? data.hostPolicyMessage : 'The snippet could not be loaded.';
	}
</script>

<FieldsPost bind:this={post} {action} onResult={take} />
<Dialog.Root bind:open>
	<Dialog.Trigger class={buttonVariants({ variant: 'outline', size: 'sm' })}>
		<ShieldCheck class="size-4" />
		Allow on host
	</Dialog.Trigger>
	<Dialog.Content class="flex max-h-[calc(100dvh-2rem)] flex-col overflow-y-auto sm:max-w-2xl">
		<Dialog.Header class="pr-8">
			<Dialog.Title>Allow this image on {hostName ?? 'the host'}</Dialog.Title>
			<Dialog.Description class="max-w-full min-w-0 break-words">
				Only the host's root can change /etc/bot-agent/runner.toml. Send these lines to whoever has root on the host, over SSH or Cloud Assistant.
			</Dialog.Description>
		</Dialog.Header>
		<div class="grid min-w-0 gap-3">
			{#if message}
				<div class="max-w-full min-w-0 break-words">
					<Notice kind="error">{message}</Notice>
				</div>
			{:else if snippet == null}
				<p class="text-muted-foreground text-sm">Loading…</p>
			{:else}
				<p class="text-sm">Add to the lists in /etc/bot-agent/runner.toml, keeping what is there, then restart bot-agent:</p>
				<CommandBlock command={snippet.toml} label="Copy runner.toml lines" ariaLabel="runner.toml lines" />
				<p class="text-sm">Or, on a host with no policy yet:</p>
				<CommandBlock command={snippet.command} label="Copy install command" ariaLabel="Install command" />
				{#if snippet.note}
					<p class="text-muted-foreground text-sm break-words">{snippet.note}</p>
				{/if}
			{/if}
		</div>
		<div>
			<Dialog.Close type="button" class={buttonVariants({ variant: 'outline' })}>Close</Dialog.Close>
		</div>
	</Dialog.Content>
</Dialog.Root>
