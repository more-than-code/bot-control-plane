<script lang="ts">
	import { browser } from '$app/environment';
	import { invalidate } from '$app/navigation';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Tabs from '$lib/components/Tabs.svelte';
	import { METRIC_REFRESH_MS } from '$lib/host-view';
	import HostAccessEditor from '$lib/components/HostAccessEditor.svelte';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import { buttonVariants } from '$lib/components/ui/button';
	import { page } from '$app/state';
	import { actionUrlFrom } from '$lib/url';
	import { accessFailureOf } from '$lib/host-access';
	import HostEdgeSection from './HostEdgeSection.svelte';
	import OverviewTab from './OverviewTab.svelte';
	import ProcessesTab from './ProcessesTab.svelte';

	let { data, form } = $props();

	// A member of the host's org goes back to its Hosts tab; a project collaborator outside the org to Projects.
	const crumbs = $derived(
		data.host && data.org
			? [
					// Only org owners and admins have the Hosts tab; a member goes to the org's Projects.
					{ label: data.org.name, href: data.canManageAccess ? `/orgs/${data.org.id}?tab=hosts` : `/orgs/${data.org.id}` },
					{ label: data.host.name }
				]
			: [
					{ label: 'Projects', href: '/projects' },
					{ label: data.host ? data.host.name : 'Host' }
				]
	);
	const accessFailure = $derived(accessFailureOf(form));

	const tabs = $derived([
		{ key: 'overview', label: 'Overview', href: `?tab=overview&range=${data.range}` },
		{ key: 'processes', label: 'Processes', href: `?tab=processes&sort=${data.processSort}` }
	]);

	$effect(() => {
		if (!browser || data.tab !== 'overview' || !data.host) return;
		let timer: ReturnType<typeof setInterval> | undefined;
		const stop = () => {
			if (timer) clearInterval(timer);
			timer = undefined;
		};
		const start = () => {
			stop();
			timer = setInterval(() => {
				void invalidate('host:metrics');
			}, METRIC_REFRESH_MS);
		};
		const onVisibility = () => {
			if (document.visibilityState === 'hidden') stop();
			else start();
		};
		if (document.visibilityState !== 'hidden') start();
		document.addEventListener('visibilitychange', onVisibility);
		return () => {
			stop();
			document.removeEventListener('visibilitychange', onVisibility);
		};
	});
</script>

<svelte:head>
	<title>{data.host ? data.host.name : 'Host'} · Deploybot</title>
</svelte:head>

<PageHeader title={data.host ? data.host.name : 'Host'} breadcrumbs={crumbs}>
	{#snippet actions()}
		{#if data.host && data.canManageAccess}
			<a class={buttonVariants({ variant: 'outline' })} href="#access"><ShieldCheck class="size-4" />Manage access</a>
		{/if}
	{/snippet}
</PageHeader>
{#if !data.host}
	<Notice kind="error">{data.message ?? 'This host is not available.'}</Notice>
{:else}
	{#if data.message || (form?.message && !accessFailure)}
		<Notice kind="error">{(accessFailure ? null : form?.message) ?? data.message}</Notice>
	{/if}
	{#if data.host.archived_at}
		<p class="text-muted-foreground mb-4 text-sm">This host was removed from the lists on <LocalTime iso={data.host.archived_at} />.</p>
	{/if}
	<Tabs label="Host sections" current={data.tab} items={tabs} />

	{#if data.tab === 'overview'}
		<OverviewTab host={data.host} range={data.range} metrics={data.metrics} snapshot={form?.snapshot ?? null} />
	{:else}
		<ProcessesTab
			sort={data.processSort}
			processes={data.processes}
			message={data.processesMessage}
			serviceLinks={data.serviceLinks}
		/>
	{/if}
	{#if data.canManageAccess}
		<HostEdgeSection
			hostName={data.host.name}
			edge={data.edge ?? null}
			loadMessage={data.edgeMessage ?? null}
			actionMessage={typeof form?.edgeMessage === 'string' ? form.edgeMessage : null}
		/>
		<section id="access" class="mt-8">
			<h2 class="mb-3 text-lg font-semibold tracking-tight">Access</h2>
			{#if data.accessMessage}
				<Notice kind="error">{data.accessMessage}</Notice>
			{/if}
			{#if data.accessLoaded}
			<HostAccessEditor
				hostId={data.host.id}
				hostName={data.host.name}
				rows={data.access ?? []}
				projects={data.accessProjects ?? []}
				programs={data.host.programs}
				setAction={actionUrlFrom(page.url, 'setAccess')}
				removeAction={actionUrlFrom(page.url, 'removeAccess')}
				message={accessFailure?.message ?? null}
				conflictProjectId={accessFailure?.accessConflict?.projectId ?? null}
			/>
			{/if}
		</section>
	{/if}
{/if}
