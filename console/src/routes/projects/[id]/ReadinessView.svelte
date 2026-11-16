<script lang="ts">
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Notice from '$lib/components/Notice.svelte';
	import RowAction from '$lib/components/RowAction.svelte';
	import { Badge } from '$lib/components/ui/badge';
	import { buttonVariants } from '$lib/components/ui/button';
	import {
		checkLabel,
		fixAsk,
		fixHref,
		fixLabel,
		readinessSummary,
		statusWord,
		type FixRights,
		type Readiness,
		type ReadinessCheck,
		type ReadyStatus
	} from '$lib/settings-auto';
	import GitHubConnect from './GitHubConnect.svelte';
	import HostPolicyDialog from './HostPolicyDialog.svelte';

	/**
	 * The readiness list as shown (api-v1 v1.29 item 7). `ReadinessList` loads it; this only renders, so the states
	 * can be rendered on their own. A fix the person cannot act on shows who to ask instead of a dead link.
	 * Compact (spec-agent-usability-1 §3 A): with nothing missing or to check, one line and a closed Details;
	 * otherwise only the rows that are not OK, and the passed ones behind a closed toggle. Open state is not kept.
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

	const passed = $derived(result?.checks.filter((c) => c.status === 'ok') ?? []);
	const attention = $derived(result?.checks.filter((c) => c.status !== 'ok') ?? []);
	// `unknown` (checked at deploy) does not need action, as in the summary; `missing` and `warn` do.
	const compact = $derived(
		!loading && !message && result !== null && result.ready && !result.checks.some((c) => c.status === 'missing' || c.status === 'warn')
	);
	const summaryClass = 'text-muted-foreground hover:text-foreground w-fit cursor-pointer text-sm underline-offset-4 hover:underline';

	function badgeVariant(status: ReadyStatus): 'outline' | 'destructive' | 'secondary' {
		if (status === 'missing') return 'destructive';
		if (status === 'ok') return 'outline';
		return 'secondary';
	}
</script>

<section class="grid min-w-0 gap-2" aria-labelledby={headingId} aria-busy={loading}>
	<div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2">
		<!-- In the compact state the line says "Ready:", so the heading is for screen readers only. -->
		<h3 id={headingId} class={compact ? 'sr-only' : 'text-sm font-medium'}>Readiness</h3>
		{#if compact && result}
			<p class="flex min-w-0 items-center gap-1.5 text-sm break-words" role="status" data-readiness-line>
				<CircleCheck class="text-status-up size-4 shrink-0" aria-hidden="true" />
				Ready: {passed.length} check{passed.length === 1 ? '' : 's'} passed
			</p>
			<!-- Open, it takes its own row after Check again. -->
			<details class="min-w-0 open:order-last open:basis-full" data-readiness-details>
				<summary class={summaryClass}>Details</summary>
				<div class="mt-2 grid min-w-0 gap-2">
					<p class="text-muted-foreground text-sm break-words">{readinessSummary(result)}</p>
					{@render list(result.checks)}
				</div>
			</details>
		{/if}
		{#if onRefresh}
			<div class="ml-auto">
				<RowAction label="Check readiness again" icon={RefreshCw} disabled={loading} onclick={onRefresh} />
			</div>
		{/if}
	</div>
	{#if loading}
		<p class="text-muted-foreground text-sm" role="status">Checking…</p>
	{:else if message}
		<div class="max-w-full min-w-0 break-words">
			<Notice kind="error">{message}</Notice>
		</div>
	{:else if result && !compact}
		<p class="text-sm break-words" role="status">{readinessSummary(result)}</p>
		{@render list(attention)}
		{#if passed.length > 0}
			<details class="min-w-0" data-readiness-passed>
				<summary class={summaryClass}>Show {passed.length} passed check{passed.length === 1 ? '' : 's'}</summary>
				<div class="mt-2 min-w-0">{@render list(passed)}</div>
			</details>
		{/if}
	{/if}
</section>

{#snippet list(checks: ReadinessCheck[])}
	<ul class="grid min-w-0 gap-2">
		{#each checks as check, i (`${check.check}-${i}`)}
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
{/snippet}
