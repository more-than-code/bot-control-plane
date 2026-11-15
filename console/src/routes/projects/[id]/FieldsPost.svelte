<script lang="ts">
	import { enhance } from '$app/forms';

	/**
	 * A hidden form that posts some named fields to a named action and hands the answer back with those fields,
	 * like ImageLookupPost. The page's form state is not touched.
	 */
	let {
		action,
		onResult
	}: {
		action: string;
		onResult: (fields: Record<string, string>, data: Record<string, unknown>, ok: boolean) => void;
	} = $props();

	let form = $state<HTMLFormElement | null>(null);
	let pending: Record<string, string> = {};

	export function send(fields: Record<string, string>): void {
		pending = fields;
		form?.requestSubmit();
	}
</script>

<form
	bind:this={form}
	method="POST"
	{action}
	class="hidden"
	use:enhance={({ formData }) => {
		// Captured now, so overlapping answers each go back with their own request.
		const requested = pending;
		for (const [name, value] of Object.entries(requested)) formData.set(name, value);
		return async ({ result }) => {
			const data = result.type === 'success' || result.type === 'failure' ? result.data : null;
			onResult(requested, data != null && typeof data === 'object' ? (data as Record<string, unknown>) : {}, result.type === 'success');
		};
	}}
></form>
