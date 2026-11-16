<script lang="ts">
	import Notice from '$lib/components/Notice.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Tabs from '$lib/components/Tabs.svelte';
	import { servicesHealthView } from '$lib/health-checks';
	import { servicesRenameView, type ProjectActionData, type ProjectScope } from '$lib/project-action';
	import { toastHealthSaved, toastImageSaved } from '$lib/success-toast';
	import { actionUrlFrom } from '$lib/url';
	import { accessFailureOf } from '$lib/host-access';
	import CatalogEntryDialog from './CatalogEntryDialog.svelte';
	import CatalogImageDialog from './CatalogImageDialog.svelte';
	import AddHostSection from '$lib/components/AddHostSection.svelte';
	import CatalogSection from './CatalogSection.svelte';
	import HostsSection from './HostsSection.svelte';
	import { page } from '$app/state';
	import { replaceState } from '$app/navigation';
	import MembersSection from './MembersSection.svelte';
	import SettingsSection from './SettingsSection.svelte';

	let { data, form } = $props();

	// The Connect GitHub note arrives once as `?github=` (api-v1 v1.29). Drop it from the address so later
	// actions, which keep the query, do not show it again.
	$effect(() => {
		if (!page.url.searchParams.has('github')) return;
		const url = new URL(page.url);
		url.searchParams.delete('github');
		replaceState(url, page.state);
	});

	const action = $derived((form ?? null) as ProjectActionData | null);
	const scope = $derived<ProjectScope>(action?.scope ?? data.tab ?? 'hosts');
	// A dialog form's message (createToken, addMember) shows in its dialog, not on the page.
	const sectionMessage = $derived(action?.scope === scope && !action.field ? (action.message ?? null) : null);
	const createTokenMessage = $derived(action?.field === 'createToken' ? (action.message ?? null) : null);
	const accessFailure = $derived(accessFailureOf(form));
	const addMemberMessage = $derived(action?.field === 'addMember' ? (action.message ?? null) : null);
	const rename = $derived(servicesRenameView(action));
	const healthEditor = $derived(servicesHealthView(action));
	const catalogEditor = $derived(action?.scope === 'catalog' ? (action.catalogEditor ?? null) : null);
	const serviceRun = $derived(action?.scope === 'catalog' ? (action.catalogAction ?? null) : null);
	const imageEditor = $derived(action?.scope === 'catalog' ? (action.imageEditor ?? null) : null);
	const imageDeploy = $derived(action?.scope === 'catalog' ? (action.imageDeploy ?? null) : null);
	/** A host left out of the list may open more shared networks (the image form's hint says so). */
	const hostsIncomplete = $derived((data.hostsTruncated ?? false) || data.hostsMessage != null);
	const catalogMessage = $derived.by(() => {
		if (catalogEditor || imageEditor || imageDeploy) return data.catalogMessage ?? null;
		return sectionMessage ?? data.catalogMessage ?? null;
	});

	const tabs = $derived(
		data.project
			? [
					{ key: 'hosts', label: 'Hosts', href: `/projects/${data.project.id}?tab=hosts` },
					// No hover preload: the load reads container status from the hosts.
					{ key: 'catalog', label: 'Catalog & services', href: `/projects/${data.project.id}?tab=catalog`, preload: 'off' as const },
					{ key: 'members', label: 'Members', href: `/projects/${data.project.id}?tab=members` },
					...(data.canAdmin
						? [{ key: 'settings', label: 'Settings', href: `/projects/${data.project.id}?tab=settings` }]
						: [])
				]
			: []
	);
</script>

<svelte:head>
	<title>{data.project ? data.project.name : 'Project'} · Deploybot</title>
</svelte:head>

{#if !data.project}
	<PageHeader title="Project" breadcrumbs={[{ label: 'Projects', href: '/projects' }, { label: 'Project' }]} />
	<Notice kind="error">{data.message ?? 'This project is not available.'}</Notice>
{:else}
	<PageHeader
		title={data.project.name}
		subtitle="{data.project.slug} · your role {data.project.role}{data.project.via === 'org' ? ' (organization admin)' : ''}"
		breadcrumbs={data.orgName && data.project.org_id
			? [{ label: data.orgName, href: `/orgs/${data.project.org_id}` }, { label: data.project.name }]
			: [{ label: 'Projects', href: '/projects' }, { label: data.project.name }]}
	>
		{#snippet actions()}
			{#if scope === 'catalog' && data.canOwner}
				<CatalogEntryDialog
					mode="create"
					hosts={data.hosts ?? []}
					saveAction={actionUrlFrom(page.url, 'catalogCreate')}
					parseAction={actionUrlFrom(page.url, 'catalogParse')}
					setsAction={actionUrlFrom(page.url, 'catalogEnvSets')}
					revealed={catalogEditor?.mode === 'create'}
					editor={catalogEditor?.mode === 'create' ? catalogEditor : null}
				/>
				<CatalogImageDialog
					mode="create"
					hosts={data.hosts ?? []}
					{hostsIncomplete}
					projectSlug={data.project.slug}
					saveAction={actionUrlFrom(page.url, 'catalogCreate')}
					setsAction={actionUrlFrom(page.url, 'catalogEnvSets')}
					filesAction={actionUrlFrom(page.url, 'catalogFiles')}
					tagsAction={actionUrlFrom(page.url, 'catalogImageTags')}
					digestAction={actionUrlFrom(page.url, 'catalogImageDigest')}
					revealed={imageEditor?.mode === 'create'}
					editor={imageEditor?.mode === 'create' ? imageEditor : null}
					onSaved={toastImageSaved}
				/>
			{/if}
		{/snippet}
	</PageHeader>
	<Tabs label="Project sections" current={scope} items={tabs} />

	{#if scope === 'hosts'}
		<HostsSection
			hosts={data.hosts}
			omitted={data.hostsOmitted}
			truncated={data.hostsTruncated}
			canAdmin={data.canAdmin}
			canOrgAdmin={data.canOrgAdmin}
			projectId={data.project.id}
			orgId={data.project.org_id}
			message={accessFailure ? null : (sectionMessage ?? data.hostsMessage)}
			hostsMessage={data.hostsMessage}
			{accessFailure}
		>
			{#if data.canOrgAdmin}
				<AddHostSection
					tokens={data.tokens}
					hosts={data.hosts}
					created={action?.scope === 'hosts' ? (action.created ?? null) : null}
					slug={data.project.slug}
					grant="{data.project.name} (deploy)"
					open={data.hosts.length === 0 && !data.hostsMessage}
					message={createTokenMessage}
					tokensMessage={data.tokensMessage ?? null}
				/>
			{/if}
		</HostsSection>
	{:else if scope === 'catalog'}
		<!-- A new project starts with fresh open entries and states. -->
		{#key data.project.id}
		<CatalogSection
			canOwner={data.canOwner}
			canAdmin={data.canAdmin}
			entries={data.catalog ?? []}
			omitted={data.catalogOmitted ?? 0}
			truncated={data.catalogTruncated ?? false}
			message={catalogMessage}
			hosts={data.hosts ?? []}
			members={data.members ?? []}
			editor={catalogEditor?.mode === 'edit' ? catalogEditor : null}
			imageEditor={imageEditor?.mode === 'edit' ? imageEditor : null}
			imageDeploy={imageDeploy}
			onImageSaved={toastImageSaved}
			projectId={data.project.id}
			projectSlug={data.project.slug}
			{hostsIncomplete}
			copies={data.copies ?? {}}
			copiesComplete={data.copiesComplete ?? true}
			copiesOmitted={data.copiesOmitted ?? 0}
			copiesMessage={data.copiesMessage ?? null}
			statusCapped={data.statusCapped ?? false}
			notRunning={data.notRunning ?? []}
			notRunningKnown={data.notRunningKnown ?? true}
			serviceStates={data.serviceStates ?? null}
			runResult={serviceRun}
			probe={action?.scope === 'catalog' ? (action.probe ?? null) : null}
			probeServiceId={action?.scope === 'catalog' ? (action.probeServiceId ?? null) : null}
			editorProbe={healthEditor.editorProbe}
			renameServiceId={rename.renameServiceId}
			typedName={rename.typedName}
			healthServiceId={healthEditor.serviceId}
			healthField={healthEditor.field}
			healthIndex={healthEditor.index}
			healthMessage={healthEditor.message}
			probeIndex={action?.scope === 'catalog' ? (action.probeIndex ?? null) : null}
			probeMessage={action?.scope === 'catalog' ? (action.probeMessage ?? null) : null}
			onHealthSaved={toastHealthSaved}
		/>
		{/key}
	{:else if scope === 'members'}
		<MembersSection
			members={data.members}
			omitted={data.membersOmitted}
			truncated={data.membersTruncated}
			canAdmin={data.canAdmin}
			canOwner={data.canOwner}
			currentUserId={data.userId ?? null}
			message={sectionMessage ?? data.membersMessage}
			{addMemberMessage}
			invites={data.invites}
			invitesOmitted={data.invitesOmitted}
			invitesTruncated={data.invitesTruncated}
			invitesMessage={data.invitesMessage}
			inviteUrl={action?.scope === 'members' ? (action.inviteUrl ?? null) : null}
			inviteRole={action?.scope === 'members' ? (action.inviteRole ?? null) : null}
			inviteMessage={action?.scope === 'members' ? (action.inviteMessage ?? null) : null}
		/>
	{:else if scope === 'settings'}
		<SettingsSection
			project={data.project}
			canOwner={data.canOwner}
			canAdmin={data.canAdmin}
			message={sectionMessage}
			settingsField={action?.scope === 'settings' ? (action.settingsField ?? null) : null}
			envSets={data.envSets ?? []}
			envSetsMessage={data.envSetsMessage ?? null}
			files={data.files ?? []}
			filesMessage={data.filesMessage ?? null}
			viewedFile={action?.scope === 'settings' ? (action.viewedFile ?? null) : null}
			settingsName={action?.scope === 'settings' ? (action.settingsName ?? null) : null}
			registries={data.registries ?? []}
			registriesMessage={data.registriesMessage ?? null}
			githubConnect={data.githubConnect ?? false}
			githubNote={action?.scope === 'settings' && action.githubNote ? action.githubNote : (data.githubNote ?? null)}
			githubRefused={data.githubRefused ?? false}
			catalog={data.catalog ?? []}
			catalogKnown={data.catalogMessage == null && !(data.catalogTruncated ?? false) && (data.catalogOmitted ?? 0) === 0}
			hosts={data.hosts ?? []}
			{hostsIncomplete}
		/>
	{/if}
{/if}
