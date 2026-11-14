<script lang="ts">
	import { browser } from '$app/environment';
	import { invalidate } from '$app/navigation';
	import Notice from '$lib/components/Notice.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Tabs from '$lib/components/Tabs.svelte';
	import { METRIC_REFRESH_MS } from '$lib/host-view';
	import HostAccessEditor from '$lib/components/HostAccessEditor.svelte';
	import { page } from '$app/state';
	import { actionUrlFrom } from '$lib/url';
	import { accessFailureOf } from '$lib/host-access';
	import OverviewTab from './OverviewTab.svelte';
	import ProcessesTab from './ProcessesTab.svelte';

	let { data, form } = $props();

	// A member of the host's org goes back to its Hosts tab; a project collaborator outside the org to Projects.
	const crumbs = $derived(
		data.host && data.org
			? [
					{ label: data.org.name, href: `/orgs/${data.org.id}?tab=hosts` },
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

<PageHeader title={data.host ? data.host.name : 'Host'} breadcrumbs={crumbs} />
{#if !data.host}
	<Notice kind="error">{data.message ?? 'This host is not available.'}</Notice>
{:else}
	{#if data.message || (form?.message && !accessFailure)}
		<Notice kind="error">{(accessFailure ? null : form?.message) ?? data.message}</Notice>
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
		<section id="access" class="mt-8">
			<h2 class="mb-3 text-lg font-semibold tracking-tight">Access</h2>
			{#if data.accessMessage}
				<Notice kind="error">{data.accessMessage}</Notice>
			{/if}
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
		</section>
	{/if}
{/if}
