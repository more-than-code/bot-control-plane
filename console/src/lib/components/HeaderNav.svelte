<script lang="ts">
	import { page } from '$app/state';
	import Check from '@lucide/svelte/icons/check';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import Plus from '@lucide/svelte/icons/plus';
	import NewOrganization from '$lib/components/NewOrganization.svelte';
	import NewProject from '$lib/components/NewProject.svelte';
	import { buttonVariants } from '$lib/components/ui/button';
	import * as DropdownMenu from '$lib/components/ui/dropdown-menu';
	import { headerNav, mobileOrgItems, orgTriggerName, projectTriggerName, type NavOrg, type NavProject } from '$lib/header-nav';
	import { cn } from '$lib/utils';

	/**
	 * The header's `Org ▾ / Project ▾` (spec-header-org-project-bell §3.1). Below `sm` only the Project ▾ part
	 * shows, and its menu starts with the org items.
	 */
	let {
		orgs,
		projects,
		pageOrgId = null
	}: {
		orgs: NavOrg[];
		projects: NavProject[];
		/** The org a page names when its path does not (the host page). */
		pageOrgId?: string | null;
	} = $props();

	const nav = $derived(headerNav({ path: page.url.pathname, orgs, projects, pageOrgId }));
	const creatable = $derived(nav.canCreateProject && nav.org ? [{ id: nav.org.id, name: nav.org.name }] : []);

	const ORG_ID = 'header-org';
	const PROJECT_ID = 'header-project';
	const PROJECT_MOBILE_ID = 'header-project-mobile';
	let newProjectRequest = $state(0);
	let newProjectFrom = $state(PROJECT_ID);

	function newProject(from: string) {
		newProjectFrom = from;
		newProjectRequest += 1;
	}

	let newOrgRequest = $state(0);
	let newOrgFrom = $state(ORG_ID);

	function newOrg(from: string) {
		newOrgFrom = from;
		newOrgRequest += 1;
	}

	// 44 px touch targets below sm (like RowMenu), the normal size from sm.
	const trigger = cn(buttonVariants({ variant: 'ghost', size: 'sm' }), 'max-w-48 min-w-0 font-normal max-sm:h-11');
	// Below sm the project trigger takes at most 40vw, so logo, trigger and bell fit one row at 375 px.
	const mobileTrigger = cn(trigger, 'max-w-[40vw] sm:max-w-48 sm:hidden');
	const menu = 'w-auto min-w-56 max-w-[calc(100vw-2rem)]';
</script>

{#snippet slash(extra: string)}
	<span aria-hidden="true" class={cn('text-muted-foreground shrink-0', extra)}>/</span>
{/snippet}

{#snippet orgItems(from: string)}
	{#each nav.orgs as org (org.id)}
		<DropdownMenu.Item>
			{#snippet child({ props })}
				<a href={org.href} aria-current={org.current ? 'page' : undefined} {...props}>
					<Check aria-hidden="true" class={cn('size-4', org.current ? '' : 'invisible')} />
					<span class="min-w-0 truncate">{org.name}</span>
					{#if org.current}<span class="sr-only"> (current)</span>{/if}
				</a>
			{/snippet}
		</DropdownMenu.Item>
	{/each}
	<!-- spec-new-org: any signed-in user; offered wherever these org items render (and always on /projects). -->
	<DropdownMenu.Item onSelect={() => newOrg(from)}><Plus class="size-4" />New organization</DropdownMenu.Item>
	{#if nav.orgs.length > 0}
		<DropdownMenu.Separator />
	{/if}
	{#if nav.orgPage}
		<DropdownMenu.Item>
			{#snippet child({ props })}
				<a href={nav.orgPage?.href} {...props}>{nav.orgPage?.label}</a>
			{/snippet}
		</DropdownMenu.Item>
	{/if}
	<DropdownMenu.Item>
		{#snippet child({ props })}
			<a href="/projects" {...props}>All projects</a>
		{/snippet}
	</DropdownMenu.Item>
{/snippet}

{#snippet projectItems(from: string)}
	{#each nav.projects as project (project.id)}
		<DropdownMenu.Item>
			{#snippet child({ props })}
				<a href={project.href} aria-current={project.current ? 'page' : undefined} {...props}>
					<Check aria-hidden="true" class={cn('size-4', project.current ? '' : 'invisible')} />
					<span class="min-w-0 truncate">{project.name}</span>
					{#if project.current}<span class="sr-only"> (current)</span>{/if}
				</a>
			{/snippet}
		</DropdownMenu.Item>
	{:else}
		<DropdownMenu.Label>No projects here yet.</DropdownMenu.Label>
	{/each}
	{#if nav.canCreateProject}
		<DropdownMenu.Separator />
		<DropdownMenu.Item onSelect={() => newProject(from)}><Plus class="size-4" />New project</DropdownMenu.Item>
	{/if}
{/snippet}

<nav class="flex min-w-0 items-center gap-1" aria-label="Organization and project">
	{#if nav.org}
		{@render slash('hidden sm:inline')}
		{#if nav.org.menu}
			<DropdownMenu.Root>
				<DropdownMenu.Trigger id={ORG_ID} class={cn(trigger, 'hidden sm:inline-flex')} aria-label={orgTriggerName(nav.org.name)}>
					<span class="min-w-0 truncate">{nav.org.name}</span>
					<ChevronDown class="size-4" />
				</DropdownMenu.Trigger>
				<DropdownMenu.Content align="start" class={menu}>
					{@render orgItems(ORG_ID)}
				</DropdownMenu.Content>
			</DropdownMenu.Root>
		{:else}
			<!-- A project-only member: the org's name, with no menu and no link (they cannot open the org page). -->
			<span data-org-text class="hidden max-w-48 min-w-0 truncate px-2 text-sm sm:inline">
				<span class="sr-only">Organization:</span>
				{nav.org.name}
			</span>
		{/if}
	{/if}
	{@render slash('hidden sm:inline')}
	<!-- The Project menu renders twice: this desktop one (sm and up) and the phone one below, which adds the org
	     items. Only one trigger is visible at a time, so New project returns focus to the trigger it came from
	     (fallbackFocusId); after a resize across sm that trigger is hidden and focus falls back to the page. -->
	<DropdownMenu.Root>
		<DropdownMenu.Trigger id={PROJECT_ID} class={cn(trigger, 'hidden sm:inline-flex')} aria-label={projectTriggerName(nav.projectLabel)}>
			<span class={cn('min-w-0 truncate', nav.projectId ? '' : 'text-muted-foreground')}>{nav.projectLabel}</span>
			<ChevronDown class="size-4" />
		</DropdownMenu.Trigger>
		<DropdownMenu.Content align="start" class={menu}>
			{@render projectItems(PROJECT_ID)}
		</DropdownMenu.Content>
	</DropdownMenu.Root>
	<DropdownMenu.Root>
		<DropdownMenu.Trigger id={PROJECT_MOBILE_ID} class={mobileTrigger} aria-label={projectTriggerName(nav.projectLabel)}>
			<span class={cn('min-w-0 truncate', nav.projectId ? '' : 'text-muted-foreground')}>{nav.projectLabel}</span>
			<ChevronDown class="size-4" />
		</DropdownMenu.Trigger>
		<DropdownMenu.Content align="start" class={menu}>
			{#if mobileOrgItems(nav)}
				<DropdownMenu.Group data-menu-group="orgs">
					<DropdownMenu.GroupHeading>Organizations</DropdownMenu.GroupHeading>
					{@render orgItems(PROJECT_MOBILE_ID)}
				</DropdownMenu.Group>
				<DropdownMenu.Separator />
			{/if}
			<DropdownMenu.Group data-menu-group="projects">
				<DropdownMenu.GroupHeading>{nav.org ? `Projects in ${nav.org.name}` : 'Projects'}</DropdownMenu.GroupHeading>
				{@render projectItems(PROJECT_MOBILE_ID)}
			</DropdownMenu.Group>
		</DropdownMenu.Content>
	</DropdownMenu.Root>
</nav>
{#if nav.canCreateProject}
	<!-- Keyed on the org: the dialog's org is read once when it mounts. -->
	{#key nav.org?.id}
		<NewProject
			action="/projects?/create"
			failed={false}
			orgs={creatable}
			showTrigger={false}
			openRequest={newProjectRequest}
			fallbackFocusId={newProjectFrom}
			ownResult
		/>
	{/key}
{/if}
<!-- spec-new-org: posts to /projects like New project; a success lands on the new org's Projects tab. -->
<NewOrganization action="/projects?/createOrg" showTrigger={false} openRequest={newOrgRequest} fallbackFocusId={newOrgFrom} ownResult />
