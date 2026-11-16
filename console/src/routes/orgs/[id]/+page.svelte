<script lang="ts">
	import { page } from '$app/state';
	import AddHostSection from '$lib/components/AddHostSection.svelte';
	import NewProject from '$lib/components/NewProject.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Tabs from '$lib/components/Tabs.svelte';
	import { accessFailureOf } from '$lib/host-access';
	import { joinGrantText, type OrgActionData, type OrgScope } from '$lib/org-view';
	import { actionUrlFrom } from '$lib/url';
	import OrgDomainsSection from './OrgDomainsSection.svelte';
	import OrgHostsSection from './OrgHostsSection.svelte';
	import OrgMembersSection from './OrgMembersSection.svelte';
	import OrgProjectsSection from './OrgProjectsSection.svelte';
	import OrgSettingsSection from './OrgSettingsSection.svelte';
	import TokensSection from './TokensSection.svelte';

	let { data, form } = $props();

	const action = $derived((form ?? null) as OrgActionData | null);
	const accessFailure = $derived(accessFailureOf(form));
	const scope = $derived<OrgScope>(action?.scope ?? data.tab ?? 'projects');
	// A dialog form's message shows in its dialog, not on the page.
	const sectionMessage = $derived(action?.scope === scope && !action.field && !accessFailure ? (action.message ?? null) : null);
	const fieldMessage = (field: OrgActionData['field']) => (action?.field === field ? (action?.message ?? null) : null);
	const accessProjects = $derived(data.projects.items.map((project) => ({ id: project.id, name: project.name, slug: project.slug })));
	const projectNames = $derived(Object.fromEntries(data.projects.items.map((project) => [project.id, project.name])));

	const tabs = $derived(
		data.org
			? [
					{ key: 'projects', label: 'Projects', href: `/orgs/${data.org.id}?tab=projects` },
					...(data.canAdmin ? [{ key: 'hosts', label: 'Hosts', href: `/orgs/${data.org.id}?tab=hosts` }] : []),
					{ key: 'members', label: 'Members', href: `/orgs/${data.org.id}?tab=members` },
					...(data.canAdmin
						? [
								{ key: 'tokens', label: 'Join tokens', href: `/orgs/${data.org.id}?tab=tokens` },
								{ key: 'settings', label: 'Settings', href: `/orgs/${data.org.id}?tab=settings` }
							]
						: [])
				]
			: []
	);
</script>

<svelte:head>
	<title>{data.org ? data.org.name : 'Organization'} · Deploybot</title>
</svelte:head>

{#if !data.org}
	<PageHeader title="Organization" breadcrumbs={[{ label: 'Projects', href: '/projects' }, { label: 'Organization' }]} />
	<Notice kind="error">{data.message ?? 'This organization is not available.'}</Notice>
{:else}
	<PageHeader
		title={data.org.name}
		subtitle="Organization · your role {data.org.role}"
		breadcrumbs={[{ label: 'Projects', href: '/projects' }, { label: data.org.name }]}
	>
		{#snippet actions()}
			{#if scope === 'projects' && data.canAdmin}
				<NewProject
					action={actionUrlFrom(page.url, 'createProject')}
					failed={fieldMessage('createProject') != null}
					message={fieldMessage('createProject')}
				/>
			{/if}
		{/snippet}
	</PageHeader>
	<Tabs label="Organization sections" current={scope} items={tabs} />

	{#if scope === 'projects'}
		<OrgProjectsSection
			projects={data.projects.items}
			omitted={data.projects.omitted}
			truncated={data.projects.truncated}
			message={sectionMessage ?? data.projects.message}
			canAdmin={data.canAdmin}
		/>
	{:else if scope === 'hosts'}
		<OrgHostsSection
			hosts={data.hosts.items}
			omitted={data.hosts.omitted}
			truncated={data.hosts.truncated}
			projects={accessProjects}
			message={sectionMessage ?? data.hosts.message ?? data.projects.message}
			{accessFailure}
		>
			<AddHostSection
				tokens={data.tokens.items}
				hosts={data.hosts.items}
				created={action?.field === 'createToken' ? (action.created ?? null) : null}
				slug={action?.field === 'createToken' ? (action.createdSlug ?? null) : null}
				grant={action?.field === 'createToken' ? joinGrantText(action.createdGrant ?? null, projectNames) : null}
				open={data.hosts.items.length === 0 && !data.hosts.message}
				message={fieldMessage('createToken')}
				tokensMessage={data.tokens.message}
				pollKey="org:enrol"
				grantProjects={accessProjects}
				description="The host joins this organization. You can give one project access with the token."
			/>
		</OrgHostsSection>
	{:else if scope === 'members'}
		<OrgMembersSection
			members={data.members.items}
			omitted={data.members.omitted}
			truncated={data.members.truncated}
			canAdmin={data.canAdmin}
			canOwner={data.canOwner}
			currentUserId={data.userId}
			message={sectionMessage ?? data.members.message}
			addMemberMessage={fieldMessage('addMember')}
		/>
	{:else if scope === 'tokens'}
		<TokensSection
			tokens={data.tokens.items}
			omitted={data.tokens.omitted}
			truncated={data.tokens.truncated}
			message={sectionMessage ?? data.tokens.message}
			{projectNames}
		/>
	{:else if scope === 'settings'}
		<OrgDomainsSection
			domains={data.domains.items}
			omitted={data.domains.omitted}
			projects={data.projects.items}
			message={data.domains.message}
			addMessage={fieldMessage('addDomain')}
			rowMessage={fieldMessage('domain')}
			rowMessageId={action?.domainId ?? null}
		/>
		<OrgSettingsSection org={data.org} canOwner={data.canOwner} message={sectionMessage} />
	{/if}
{/if}
