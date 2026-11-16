<script lang="ts">
	import { tick } from 'svelte';
	import Network from '@lucide/svelte/icons/network';
	import CommandBlock from '$lib/components/CommandBlock.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import * as Dialog from '$lib/components/ui/dialog';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import * as Select from '$lib/components/ui/select';
	import {
		networkSnippetHeading,
		networkSnippetProblem,
		sameSnippetAsk,
		type NetworkSnippetAsk,
		type NetworkSnippetResult
	} from '$lib/settings-auto';
	import FieldsPost from './FieldsPost.svelte';

	/**
	 * Ask the host's root for a shared network (spec-agent-usability-1 H3, api-v1 v1.33 item 7): the runner.toml lines
	 * that would open a network to this project, with Copy. Modelled on HostPolicyDialog. Deploybot never edits
	 * runner.toml; creating a network stays root-only on the host (v1.25).
	 */
	let {
		action,
		hosts
	}: {
		/** `?/networkSnippet`. */
		action: string;
		/** The project's hosts it may deploy to. */
		hosts: Array<{ id: string; name: string }>;
	} = $props();

	let open = $state(false);
	let hostId = $state('');
	let network = $state('');
	let subnet = $state('');
	// The shown answer, with what was asked: a later change to the fields never relabels it.
	let result = $state<NetworkSnippetResult | null>(null);
	let message = $state<string | null>(null);
	// What the shown answer (or the pending request) is for. Null when nothing is shown or pending.
	let asked = $state<NetworkSnippetAsk | null>(null);
	let loading = $state(false);
	let post = $state<FieldsPost | null>(null);
	let resultEl = $state<HTMLDivElement | null>(null);

	function reset() {
		hostId = hosts.length === 1 ? (hosts[0]?.id ?? '') : '';
		network = '';
		subnet = '';
		result = null;
		message = null;
		asked = null;
		loading = false;
	}

	// Opening starts clean, and closing drops the fields and the answer.
	$effect(() => {
		if (!open) reset();
	});

	// Any change to a field makes the shown lines or message stale: they go, and a pending answer is dropped.
	$effect(() => {
		const now = { hostId, network, subnet };
		if (asked != null && !sameSnippetAsk(asked, now)) {
			result = null;
			message = null;
			asked = null;
			loading = false;
		}
	});

	async function focusResult() {
		await tick();
		// The Copy button, or the error.
		const target = resultEl?.querySelector<HTMLElement>('button, [data-snippet-error]');
		target?.focus();
	}

	function show(event: SubmitEvent) {
		event.preventDefault();
		if (loading) return;
		result = null;
		const ask = { hostId, network: network.trim(), subnet: subnet.trim() };
		message = hostId === '' ? 'Choose a host.' : networkSnippetProblem(ask.network, ask.subnet);
		asked = { hostId, network, subnet };
		if (message) {
			void focusResult();
			return;
		}
		loading = true;
		post?.send({ host_id: ask.hostId, network: ask.network, subnet: ask.subnet });
	}

	function take(fields: Record<string, string>, data: Record<string, unknown>) {
		// Dropped when the fields changed (or the dialog closed) since it was asked.
		if (asked == null || fields.host_id !== asked.hostId || fields.network !== asked.network.trim() || fields.subnet !== asked.subnet.trim()) return;
		loading = false;
		const snippet = typeof data.networkSnippet === 'string' ? data.networkSnippet : null;
		const hostName = hosts.find((host) => host.id === fields.host_id)?.name ?? 'the host';
		result = snippet ? { hostName, network: fields.network, subnet: fields.subnet, snippet } : null;
		message = snippet ? null : typeof data.networkSnippetMessage === 'string' ? data.networkSnippetMessage : 'The lines could not be loaded.';
		void focusResult();
	}
</script>

<FieldsPost bind:this={post} {action} onResult={take} />
<Dialog.Root bind:open>
	<Dialog.Trigger class={buttonVariants({ variant: 'outline', size: 'sm' })}>
		<Network class="size-4" />
		Ask for a network
	</Dialog.Trigger>
	<Dialog.Content class="flex max-h-[calc(100dvh-2rem)] flex-col overflow-y-auto sm:max-w-2xl">
		<Dialog.Header class="pr-8">
			<Dialog.Title>Ask for a shared network</Dialog.Title>
			<Dialog.Description class="max-w-full min-w-0 break-words">
				Only the host's root can change /etc/bot-agent/runner.toml. Choose the host, a name and a subnet, then send the lines to whoever has root on the host.
			</Dialog.Description>
		</Dialog.Header>
		<form class="grid min-w-0 gap-3" onsubmit={show}>
			<div class="grid gap-1.5">
				<Label for="network-snippet-host">Host</Label>
				<Select.Root type="single" bind:value={hostId}>
					<Select.Trigger id="network-snippet-host" class="w-full">
						<Select.Value placeholder="Choose a host" />
					</Select.Trigger>
					<Select.Content>
						{#each hosts as host (host.id)}
							<Select.Item value={host.id} label={host.name}>{host.name}</Select.Item>
						{/each}
					</Select.Content>
				</Select.Root>
			</div>
			<Label class="grid gap-1.5" for="network-snippet-name">
				Network name
				<Input id="network-snippet-name" bind:value={network} maxlength={32} autocomplete="off" spellcheck="false" placeholder="shared-net" />
			</Label>
			<Label class="grid gap-1.5" for="network-snippet-subnet">
				Subnet
				<Input id="network-snippet-subnet" bind:value={subnet} maxlength={18} autocomplete="off" spellcheck="false" placeholder="10.200.10.0/24" />
			</Label>
			<div>
				<!-- Not `disabled` while loading, so focus stays on it; a second submit is ignored. -->
				<Button type="submit" variant="outline" aria-busy={loading} aria-disabled={loading}>{loading ? 'Loading…' : 'Show lines'}</Button>
			</div>
		</form>
		<div bind:this={resultEl} class="grid min-w-0 gap-3" aria-live="polite">
			{#if message}
				<div class="max-w-full min-w-0 break-words outline-none" tabindex="-1" data-snippet-error>
					<Notice kind="error">{message}</Notice>
				</div>
			{:else if result}
				<p class="text-sm break-words">{networkSnippetHeading(result)}</p>
				<CommandBlock command={result.snippet} label="Copy runner.toml lines" ariaLabel="runner.toml lines" />
			{/if}
		</div>
		<div>
			<Dialog.Close type="button" class={buttonVariants({ variant: 'outline' })}>Close</Dialog.Close>
		</div>
	</Dialog.Content>
</Dialog.Root>
