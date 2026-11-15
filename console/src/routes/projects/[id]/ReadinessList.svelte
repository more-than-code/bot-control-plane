<script lang="ts">
	import { page } from '$app/state';
	import { actionUrlFrom } from '$lib/url';
	import type { Readiness } from '$lib/settings-auto';
	import FieldsPost from './FieldsPost.svelte';
	import ReadinessView from './ReadinessView.svelte';

	/**
	 * Loads the readiness check of one entry (api-v1 v1.29 item 7), for the chosen host when there is one, and
	 * shows it with `ReadinessView`. Advisory: Deploy and Run stay enabled. It checks again when the host or
	 * `refreshKey` changes (a deploy or run result) and on Check again. Never shows a value or a token.
	 */
	let {
		entry,
		hostId = null,
		hostName = null,
		refreshKey = null
	}: {
		entry: string;
		hostId?: string | null;
		hostName?: string | null;
		/** A new value (a deploy or run result) checks again. */
		refreshKey?: unknown;
	} = $props();

	const readinessAction = $derived(actionUrlFrom(page.url, 'catalogReadiness'));
	const snippetAction = $derived(actionUrlFrom(page.url, 'hostPolicySnippet'));
	const connectAction = $derived(actionUrlFrom(page.url, 'connectGitHub'));
	const projectPath = $derived(page.url.pathname);
	const canAdmin = $derived(page.data.canAdmin === true);
	const canOrgAdmin = $derived(page.data.canOrgAdmin === true);

	let post = $state<FieldsPost | null>(null);
	let result = $state<Readiness | null>(null);
	let message = $state<string | null>(null);
	let loading = $state(true);
	// The registries list's github_connect, sent with the answer only when a fix is Connect GitHub.
	let githubConnect = $state(false);
	// The host the shown answer is for; an answer for an earlier host is dropped.
	let wanted = '';

	function load() {
		wanted = hostId ?? '';
		loading = true;
		message = null;
		post?.send({ entry, host_id: wanted });
	}

	$effect(() => {
		void hostId;
		void refreshKey;
		if (post) load();
	});

	function take(fields: Record<string, string>, data: Record<string, unknown>) {
		if (fields.host_id !== wanted || fields.entry !== entry) return;
		loading = false;
		result = (data.readiness as Readiness | null | undefined) ?? null;
		githubConnect = data.readinessGitHub === true;
		message = result ? null : typeof data.readinessMessage === 'string' ? data.readinessMessage : 'Readiness could not be checked.';
	}
</script>

<FieldsPost bind:this={post} action={readinessAction} onResult={take} />
<ReadinessView
	{result}
	{message}
	{loading}
	rights={{ canAdmin, canOrgAdmin, githubConnect }}
	{entry}
	{hostId}
	{hostName}
	{projectPath}
	{connectAction}
	{snippetAction}
	onRefresh={load}
/>
