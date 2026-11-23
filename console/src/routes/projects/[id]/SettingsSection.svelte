<script lang="ts">
	import { untrack } from 'svelte';
	import { enhance } from '$app/forms';
	import { page } from '$app/state';
	import ConfirmAction from '$lib/components/ConfirmAction.svelte';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import FormDialog from '$lib/components/FormDialog.svelte';
	import EnvSetDialog from './EnvSetDialog.svelte';
	import EnvSetTable from './EnvSetTable.svelte';
	import FileViewDialog, { viewButtonId } from './FileViewDialog.svelte';
	import ProjectFileForm from './ProjectFileForm.svelte';
	import RegistryForm from './RegistryForm.svelte';
	import GitHubConnect from './GitHubConnect.svelte';
	import NetworksCard from './NetworksCard.svelte';
	import { Badge } from '$lib/components/ui/badge';
	import {
		envSetUsers,
		fileUsers,
		GITHUB_NOTES,
		GITHUB_REGISTRY,
		refusalText,
		refusedTitle,
		registryUsers,
		usedByText,
		type GitHubNoteCode,
		type GitHubRefused
	} from '$lib/settings-auto';
	import { ENV_SET_STATUS_ID } from './env-set-panel';
	import type { CatalogEntry, EnvSet, Host, Project, ProjectFile, Registry } from '$lib/api-types';
	import { formatBytes } from '$lib/format';
	import { actionUrlFrom } from '$lib/url';
	import { Button } from '$lib/components/ui/button';
	import * as Card from '$lib/components/ui/card';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';

	let {
		project,
		canOwner,
		canAdmin = false,
		message,
		settingsField = null,
		envSets = [],
		envSetsMessage = null,
		files = [],
		filesMessage = null,
		viewedFile = null,
		settingsName = null,
		registries = [],
		registriesMessage = null,
		githubConnect = false,
		githubNote = null,
		githubRefused = false,
		catalog = [],
		catalogKnown = true,
		hosts = [],
		hostsIncomplete = false
	}: {
		project: Project;
		canOwner: boolean;
		canAdmin?: boolean;
		message: string | null;
		settingsField?: 'dotenv' | 'token' | 'file' | 'merge' | null;
		envSets?: EnvSet[];
		envSetsMessage?: string | null;
		files?: ProjectFile[];
		filesMessage?: string | null;
		viewedFile?: { name: string; content: string } | null;
		settingsName?: string | null;
		registries?: Registry[];
		registriesMessage?: string | null;
		/** Connect GitHub is offered: the registries list's github_connect (api-v1 v1.29 item 5). */
		githubConnect?: boolean;
		/** The note after Connect GitHub (`?github=`) or a Disconnect whose revoke failed. */
		githubNote?: GitHubNoteCode | null;
		/** ghcr.io refused the GitHub connection for an image in this project's catalog, with GitHub's reasons (api-v1 v1.52). */
		/** Streamed: its registry lookups never hold up the tab. */
		githubRefused?: GitHubRefused | Promise<GitHubRefused>;
		/** The catalog, for each card's "Used by" note (names only). */
		catalog?: CatalogEntry[];
		/** False when the catalog failed to load, was truncated or left entries out: the notes say usage is unknown. */
		catalogKnown?: boolean;
		/** The project's hosts, for the Networks card (spec-agent-usability-1 H2). */
		hosts?: Host[];
		/** The host list was truncated or could not be read. */
		hostsIncomplete?: boolean;
	} = $props();

	const githubRow = $derived(registries.find((row) => row.host === GITHUB_REGISTRY && row.source === 'github') ?? null);
	const manualGhcr = $derived(registries.find((row) => row.host === GITHUB_REGISTRY && row.source !== 'github') ?? null);
	const connectAction = $derived(actionUrlFrom(page.url, 'connectGitHub'));
	const note = $derived(githubNote ? GITHUB_NOTES[githubNote] : null);

	// The Files card's "Saved file {name}." line, set by the Add file dialog.
	let savedFile = $state<string | null>(null);
	// The Registries card's "Saved registry {host}." line, set by the Add registry dialog.
	let savedRegistry = $state<string | null>(null);
	// The plain file whose View result is shown in the file dialog. A view at page load (no-JS post) shows too.
	let viewing = $state<string | null>(untrack(() => viewedFile?.name ?? null));
	let viewOpen = $state(untrack(() => viewedFile != null));
	// Secret files are never shown: only a plain row in the list can be viewed.
	const viewedRow = $derived(viewing != null && viewedFile?.name === viewing ? (files.find((file) => file.name === viewing && !file.is_secret) ?? null) : null);
	// Entry-private sets (`@<entry>`, api-v1 v1.33 item 6) belong to their entry's drawer, not this card.
	const sharedSets = $derived(envSets.filter((set) => !set.name.startsWith('@')));
	// The Env sets card's status line ("Saved env set {name}."), set by the Add env set dialog.
	let envSetNote = $state('');
</script>

{#snippet refusedBadge(state: GitHubRefused)}
	<Badge variant="destructive" class="ml-1" title={refusedTitle(state)}>Refused</Badge>
{/snippet}

<!-- api-v1 v1.52: why ghcr.io refused, when GitHub told; an organization's grant is on GitHub, not here. -->
{#snippet refusalLines(state: GitHubRefused)}
	{#if typeof state === 'object'}
		{#each state.refusals as refusal (`${refusal.reason}|${refusal.owner ?? ''}`)}
			{@const text = refusalText(refusal)}
			{#if text}
				<p class="w-full min-w-0 text-sm break-words" data-refusal={refusal.reason}>
					{text}
					{#if refusal.reason === 'org_restricted' && refusal.url}
						<a class="underline underline-offset-3" href={refusal.url} rel="noopener noreferrer" target="_blank"
							>Grant on GitHub<span class="sr-only">{' (opens in a new tab)'}</span></a
						>
					{/if}
				</p>
			{/if}
		{/each}
	{/if}
{/snippet}

{#if message && !settingsField && !settingsName}
	<div id="settings-error" tabindex="-1" class="mb-4 max-w-full min-w-0 break-words focus-visible:ring-3 focus-visible:ring-ring">
		<Notice kind="error">{message}</Notice>
	</div>
{/if}
{#if envSetsMessage}
	<Notice kind="error">{envSetsMessage}</Notice>
{/if}
{#if filesMessage}
	<Notice kind="error">{filesMessage}</Notice>
{/if}
{#if registriesMessage}
	<Notice kind="error">{registriesMessage}</Notice>
{/if}
{#if canOwner}
	<Card.Root class="mb-4">
		<Card.Header>
			<Card.Title>Rename</Card.Title>
		</Card.Header>
		<Card.Content>
			<form method="POST" action={actionUrlFrom(page.url, 'rename')} class="grid gap-3" use:enhance>
				<Label class="grid gap-1.5" for="project-rename">
					Project name
					<Input id="project-rename" name="name" value={project.name} required maxlength={80} autocomplete="off" />
				</Label>
				<div>
					<Button type="submit">Rename project</Button>
				</div>
			</form>
		</Card.Content>
	</Card.Root>
	<Card.Root>
		<Card.Header>
			<Card.Title>Delete</Card.Title>
			<Card.Description>Hosts stay in the organization.</Card.Description>
		</Card.Header>
		<Card.Content>
			<ConfirmAction
				id="delete-project"
				action={actionUrlFrom(page.url, 'deleteProject')}
				label="Delete project"
				confirmLabel="Delete project"
				message="Delete {project.name}? Its services are marked gone and its access to hosts is removed. Hosts stay in the organization."
				typeToConfirm={{ name: 'confirm', expected: project.name, label: `Type ${project.name} to confirm` }}
			/>
		</Card.Content>
	</Card.Root>
{:else if !canAdmin}
	<p class="text-muted-foreground text-sm">Only the owner can change these settings.</p>
{:else}
	<p class="text-muted-foreground text-sm">Only the owner can rename or delete this project.</p>
{/if}
{#if canAdmin}
	<Card.Root id="env-sets" class="mt-4">
		<Card.Header>
			<Card.Title>Env sets</Card.Title>
			<Card.Description>Plain values are shown. A secret shows only a short preview. A generated value is shown once.</Card.Description>
			<Card.Action>
				<EnvSetDialog action={actionUrlFrom(page.url, 'saveEnvSet')} {message} {settingsField} onStatus={(text) => (envSetNote = text)} />
			</Card.Action>
		</Card.Header>
		<Card.Content class="grid gap-4">
			<!-- Always present (sr-only while empty), so the live region exists before text arrives. -->
			<p id={ENV_SET_STATUS_ID} role="status" tabindex="-1" class={envSetNote ? 'text-sm break-all rounded-md focus-visible:ring-3 focus-visible:ring-ring' : 'sr-only'}>{envSetNote}</p>
			{#if sharedSets.length === 0}
				<p class="text-muted-foreground text-sm">No env sets.</p>
			{/if}
			{#each sharedSets as set (set.name)}
				<div class="grid min-w-0 gap-1">
				<EnvSetTable
					{set}
					putAction={actionUrlFrom(page.url, 'putEnvKey')}
					deleteAction={actionUrlFrom(page.url, 'deleteEnvKey')}
					deleteSetAction={actionUrlFrom(page.url, 'deleteEnvSet')}
					makeSecretAction={actionUrlFrom(page.url, 'makeEnvKeysSecret')}
					mergeAction={actionUrlFrom(page.url, 'mergeEnvSet')}
					revealAction={actionUrlFrom(page.url, 'revealEnvKey')}
					mergeError={settingsField === 'merge' && settingsName === set.name ? message : null}
					onAction={() => (envSetNote = '')}
				/>
				<p class="text-muted-foreground min-w-0 text-xs break-words" data-used-by="env-set">{set.used_by ? usedByText(set.used_by) : usedByText(envSetUsers(catalog, set.name), catalogKnown)}</p>
				</div>
			{/each}
		</Card.Content>
	</Card.Root>
	<Card.Root id="files" class="mt-4">
		<Card.Header>
			<Card.Title>Files</Card.Title>
			<Card.Description>Secret files stay on the API. The paste box is never filled in.</Card.Description>
			<Card.Action>
				<FormDialog id="add-file" label="Add file" title="Add file" description="A secret file stays on the API. Its content is not shown again." failed={settingsField === 'file' && message != null}>
					{#snippet children(settle, cancel)}
						<ProjectFileForm
							action={actionUrlFrom(page.url, 'saveProjectFile')}
							error={settingsField === 'file' ? message : null}
							onSaved={(name) => (savedFile = name)}
							onSettled={settle}
							{cancel}
						/>
					{/snippet}
				</FormDialog>
			</Card.Action>
		</Card.Header>
		<Card.Content class="grid gap-4">
			{#if files.length === 0}
				<p class="text-muted-foreground text-sm">No files.</p>
			{/if}
			{#each files as file (file.name)}
				<div class="grid gap-2">
					<div class="flex flex-wrap items-center justify-between gap-2">
						<p class="min-w-0 text-sm break-all">
							<span class="font-mono">{file.name}</span>
							<span class="text-muted-foreground">{file.is_secret ? 'secret' : 'plain'}</span>
							<span class="text-muted-foreground">{formatBytes(file.size)}</span>
							<span class="text-muted-foreground"><LocalTime iso={file.updated_at} /></span>
						</p>
						<p class="text-muted-foreground w-full min-w-0 text-xs break-words" data-used-by="file">{usedByText(fileUsers(catalog, file.name), catalogKnown)}</p>
						<div class="flex flex-wrap items-center gap-2">
							{#if !file.is_secret}
								<form
									method="POST"
									action={actionUrlFrom(page.url, 'viewProjectFile')}
									use:enhance={() => {
										return async ({ result, update }) => {
											await update();
											// A failure shows on the row, as before; only a success opens the dialog.
											viewing = result.type === 'success' ? file.name : null;
											viewOpen = viewing != null;
										};
									}}
								>
									<Input type="hidden" name="name" value={file.name} />
									<Button type="submit" id={viewButtonId(file.name)} variant="outline" size="sm" aria-label="View {file.name}">View</Button>
								</form>
							{/if}
							<ConfirmAction
								id="delete-file-{file.name}"
								action={actionUrlFrom(page.url, 'deleteProjectFile')}
								label="Delete"
								triggerLabel="Delete file {file.name}"
								confirmLabel="Delete"
								message="Delete file {file.name}?"
								messageClass="max-w-full min-w-0 break-all"
								closeOnFailure
								hidden={[{ name: 'name', value: file.name }]}
							/>
						</div>
					</div>
					{#if message && !settingsField && settingsName === file.name}
						<div class="max-w-full min-w-0 break-all">
							<Notice kind="error">{message}</Notice>
						</div>
					{/if}
				</div>
			{/each}
			{#if savedFile}
				<Notice kind="success">Saved file {savedFile}.</Notice>
			{/if}
			{#if viewedRow && viewedFile}
				<FileViewDialog
					file={viewedRow}
					content={viewedFile.content}
					bind:open={viewOpen}
					onClosed={() => (viewing = null)}
				/>
			{/if}
		</Card.Content>
	</Card.Root>
	<Card.Root id="registries" class="mt-4">
		<!-- Below sm the actions stack under the description, so the buttons never squeeze the text at 375 px. -->
		<Card.Header class="max-sm:has-data-[slot=card-action]:grid-cols-1">
			<Card.Title>Registries</Card.Title>
			<Card.Description>
				The token is write-only. It is not shown again.{githubConnect ? ' For ghcr.io, Connect GitHub signs in with GitHub instead of a token.' : ''}
			</Card.Description>
			<Card.Action class="flex flex-wrap justify-end gap-2 max-sm:col-start-1 max-sm:row-span-1 max-sm:row-start-3 max-sm:justify-self-start max-sm:justify-start">
				{#if githubConnect && !githubRow}
					<GitHubConnect action={connectAction} replaces={manualGhcr?.username ?? null} />
				{/if}
				<FormDialog
					id="add-registry"
					label="Add registry"
					title="Add registry"
					description="Sign-in for a private image registry, such as ghcr.io. Saving the same host again replaces it."
					failed={settingsField === 'token' && message != null}
				>
					{#snippet children(settle, cancel)}
						<RegistryForm
							action={actionUrlFrom(page.url, 'saveRegistry')}
							error={settingsField === 'token' ? message : null}
							onSaved={(host) => (savedRegistry = host)}
							onSettled={settle}
							{cancel}
						/>
					{/snippet}
				</FormDialog>
			</Card.Action>
		</Card.Header>
		<Card.Content class="grid gap-4">
			{#if note}
				<div class="max-w-full min-w-0 break-words">
					<Notice kind={note.kind}>{note.text}</Notice>
				</div>
			{/if}
			{#if savedRegistry}
				<Notice kind="success">Saved registry {savedRegistry}.</Notice>
			{/if}
			{#if registries.length === 0}
				<p class="text-muted-foreground text-sm">No registries.</p>
			{/if}
			{#each registries as row (row.host)}
				<div class="flex flex-wrap items-center justify-between gap-2">
					<p class="text-muted-foreground order-last w-full min-w-0 text-xs break-words" data-used-by="registry">{usedByText(registryUsers(catalog, row.host), catalogKnown)}</p>
					{#if row.source === 'github'}
						<p class="min-w-0 text-sm break-words">
							<span class="font-mono">{row.host}</span>
							<span class="text-muted-foreground">
								— connected to GitHub as <span class="text-foreground font-mono">{row.username}</span>{row.connected_by?.name ? ` by ${row.connected_by.name}` : ''},
								<LocalTime iso={row.updated_at} />
							</span>
							{#if !(githubRefused instanceof Promise)}
								{#if githubRefused}{@render refusedBadge(githubRefused)}{/if}
							{:else}
								{#await githubRefused then refused}
									{#if refused}{@render refusedBadge(refused)}{/if}
								{/await}
							{/if}
						</p>
						{#if !(githubRefused instanceof Promise)}
							{#if githubRefused}{@render refusalLines(githubRefused)}{/if}
						{:else}
							{#await githubRefused then refused}
								{#if refused}{@render refusalLines(refused)}{/if}
							{/await}
						{/if}
						<p class="text-muted-foreground w-full min-w-0 text-xs break-words" data-delete-packages={row.delete_packages ? 'true' : 'false'}>
							{row.delete_packages
								? 'Can delete image versions: org owners and admins can delete unused versions from Image versions in the catalog.'
								: 'Cannot delete image versions. Allow it to clean up stale versions from the catalog; GitHub asks you to grant delete:packages.'}
						</p>
						<div class="flex flex-wrap items-center gap-2">
							{#if githubConnect}
								<GitHubConnect action={connectAction} label="Reconnect" />
								{#if !row.delete_packages}
									<GitHubConnect action={connectAction} label="Allow deleting image versions" deletePackages />
								{/if}
							{/if}
							<ConfirmAction
								id="disconnect-registry-{row.host}"
								action={actionUrlFrom(page.url, 'deleteRegistry')}
								label="Disconnect"
								triggerLabel="Disconnect GitHub from {row.host}"
								confirmLabel="Disconnect"
								message="Disconnect GitHub? The ghcr.io login is deleted here and Deploybot's access is revoked at GitHub."
								hidden={[{ name: 'host', value: row.host }]}
							/>
						</div>
					{:else}
						<p class="min-w-0 text-sm break-all"><span class="font-mono">{row.host}</span> <span class="text-muted-foreground">{row.username}</span></p>
						<ConfirmAction
							id="delete-registry-{row.host}"
							action={actionUrlFrom(page.url, 'deleteRegistry')}
							label="Delete"
							triggerLabel="Delete registry {row.host}"
							confirmLabel="Delete"
							message="Delete registry {row.host}?"
							hidden={[{ name: 'host', value: row.host }]}
						/>
					{/if}
				</div>
			{/each}
		</Card.Content>
	</Card.Root>
	<NetworksCard {hosts} {hostsIncomplete} projectSlug={project.slug} {catalog} snippetAction={actionUrlFrom(page.url, 'networkSnippet')} />
{/if}
