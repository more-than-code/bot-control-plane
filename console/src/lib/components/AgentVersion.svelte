<script lang="ts">
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import * as Tooltip from '$lib/components/ui/tooltip';
	import AgentUpgradeButton from '$lib/components/AgentUpgradeButton.svelte';
	import type { Host } from '$lib/api-types';
	import { agentUpdateTooltip, upgradeOffer } from '$lib/agent-upgrade';

	// A host's agent version, with "Update available" when the API says a newer bot-agent exists (api-v1 v1.34),
	// and Upgrade for org owners and admins when the host allows it (api-v1 v1.48).
	let {
		host,
		canUpgrade = false,
		compact = false
	}: {
		host: Pick<Host, 'agent_version'> &
			Partial<Pick<Host, 'id' | 'name' | 'latest_agent_version' | 'update_available' | 'agent_upgrade_allowed' | 'agent_upgrade' | 'revoked_at'>>;
		/** Org owner or admin of the host's org. */
		canUpgrade?: boolean;
		/** In a table row: the Upgrade trigger is icon-only. */
		compact?: boolean;
	} = $props();
	const latest = $derived(host.update_available && host.latest_agent_version ? host.latest_agent_version : null);
	const offer = $derived(upgradeOffer(host, canUpgrade));
	const tooltip = $derived(latest ? agentUpdateTooltip(latest, offer) : '');
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
				<Tooltip.Content class="max-w-sm break-words">{tooltip}</Tooltip.Content>
			</Tooltip.Root>
		</Tooltip.Provider>
		<span id={detailId} class="sr-only">{tooltip}</span>
	{/if}
	{#if host.id && host.name && (offer === 'upgrade' || offer === 'running')}
		<AgentUpgradeButton
			hostId={host.id}
			hostName={host.name}
			from={host.agent_version}
			latest={host.agent_upgrade && offer === 'running' ? host.agent_upgrade.to : (latest ?? '')}
			running={offer === 'running'}
			{compact}
		/>
	{/if}
</span>
