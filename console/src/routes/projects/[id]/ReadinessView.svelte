<script lang="ts">
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Notice from '$lib/components/Notice.svelte';
	import { Badge } from '$lib/components/ui/badge';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import { checkLabel, fixAsk, fixHref, fixLabel, readinessSummary, statusWord, type FixRights, type Readiness, type ReadyStatus } from '$lib/settings-auto';
	import GitHubConnect from './GitHubConnect.svelte';
	import HostPolicyDialog from './HostPolicyDialog.svelte';

	/**
	 * The readiness list as shown (api-v1 v1.29 item 7). `ReadinessList` loads it; this only renders, so the states
	 * can be rendered on their own. A fix the person cannot act on shows who to ask instead of a dead link.
	 */
	let {
		result,
		message = null,
		loading = false,
		rights,
		entry,
		hostId = null,
		hostName = null,
		projectPath,
		connectAction,
		snippetAction,
		onRefresh
	}: {
		result: Readiness | null;
		message?: string | null;
		loading?: boolean;
		rights: FixRights;
		entry: string;
		hostId?: string | null;
		hostName?: string | null;
		projectPath: string;
		connectAction: string;
		snippetAction: string;
		onRefresh?: () => void;
	} = $props();

	const headingId = $props.id();

	function badgeVariant(status: ReadyStatus): 'outline' | 'destructive' | 'secondary' {
		if (status === 'missing') return 'destructive';
		if (status === 'ok') return 'outline';
		return 'secondary';
	}
</script>

<section class="grid min-w-0 gap-2" aria-labelledby={headingId} aria-busy={loading}>
	<div class="flex flex-wrap items-center justify-between gap-2">
		<h3 id={headingId} class="text-sm font-medium">Readiness</h3>
		{#if onRefresh}
			<Button type="button" variant="ghost" size="sm" onclick={onRefresh} disabled={loading}>
				<RefreshCw class="size-4" />
				Check again
			</Button>
		{/if}
	</div>
	{#if loading}
		<p class="text-muted-foreground text-sm" role="status">Checking…</p>
	{:else if message}
		<div class="max-w-full min-w-0 break-words">
			<Notice kind="error">{message}</Notice>
		</div>
	{:else if result}
		<p class="text-sm break-words" role="status">{readinessSummary(result)}</p>
		<ul class="grid min-w-0 gap-2">
			{#each result.checks as check, i (`${check.check}-${i}`)}
				{@const label = fixLabel(check.fix)}
				{@const href = fixHref(check.fix, projectPath)}
				{@const ask = fixAsk(check.fix, rights)}
				<li class="flex min-w-0 flex-wrap items-start justify-between gap-2 border-b pb-2 last:border-b-0" data-check={check.check}>
					<div class="grid min-w-0 flex-1 basis-48 gap-0.5">
						<p class="flex flex-wrap items-center gap-2 text-sm">
							<Badge variant={badgeVariant(check.status)}>{statusWord(check.status)}</Badge>
							<span class="font-medium">{checkLabel(check.check)}</span>
						</p>
						<p class="text-muted-foreground text-sm break-words">{check.message}</p>
					</div>
					{#if check.status !== 'ok' && label}
						<div class="flex min-w-0 shrink-0 flex-wrap gap-2">
							{#if ask}
								<p class="text-muted-foreground text-sm break-words" data-fix-ask>{ask}</p>
							{:else if check.fix.action === 'connect_github' && rights.githubConnect}
								<GitHubConnect action={connectAction} />
							{:else if check.fix.action === 'host_policy_snippet' && hostId}
								<HostPolicyDialog action={snippetAction} {entry} {hostId} {hostName} />
							{:else}
								{@const target = href ?? `${projectPath}?tab=settings#registries`}
								{@const text = check.fix.action === 'connect_github' ? 'Add registry login' : label}
								<a class={buttonVariants({ variant: 'outline', size: 'sm' })} href={target} target="_blank" rel="noopener noreferrer">
									{text}<span class="sr-only">{' (opens in a new tab)'}</span>
								</a>
							{/if}
						</div>
					{/if}
				</li>
			{/each}
		</ul>
	{/if}
</section>
