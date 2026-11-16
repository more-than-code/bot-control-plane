<script lang="ts">
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import * as Tooltip from '$lib/components/ui/tooltip';
	import type { Host } from '$lib/api-types';
	import { agentUpdateText } from '$lib/host-view';

	// A host's agent version, with "Update available" when the API says a newer bot-agent exists (api-v1 v1.34).
	let { host }: { host: Pick<Host, 'agent_version' | 'latest_agent_version' | 'update_available'> } = $props();
	const latest = $derived(host.update_available && host.latest_agent_version ? host.latest_agent_version : null);
	const detailId = $props.id();
	/** Controlled so a tap (click) toggles it: bits-ui opens tooltips on mouse hover and keyboard focus, not on touch. */
	let open = $state(false);
</script>

<span class="inline-flex flex-wrap items-center gap-1.5">
	<span class="font-mono">{host.agent_version ?? 'n/a'}</span>
	{#if latest}
		<!-- A button: hover and keyboard focus open the tooltip, a click or tap toggles it, and screen readers get the text once, as its description. -->
		<Tooltip.Provider>
			<Tooltip.Root bind:open disableCloseOnTriggerClick>
				<Tooltip.Trigger>
					{#snippet child({ props })}
						<Button
							{...props}
							type="button"
							variant="ghost"
							class="h-auto p-0 hover:bg-transparent"
							aria-describedby={detailId}
							data-agent-update={latest}
							onclick={() => (open = !open)}
						>
							<Badge variant="secondary">Update available</Badge>
						</Button>
					{/snippet}
				</Tooltip.Trigger>
				<Tooltip.Content class="max-w-sm break-words">{agentUpdateText(latest)}</Tooltip.Content>
			</Tooltip.Root>
		</Tooltip.Provider>
		<span id={detailId} class="sr-only">{agentUpdateText(latest)}</span>
	{/if}
</span>
