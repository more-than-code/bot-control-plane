<script lang="ts">
	import { Label } from '$lib/components/ui/label';
	import * as Select from '$lib/components/ui/select';
	import * as ToggleGroup from '$lib/components/ui/toggle-group';
	import { GENERATE_KINDS, GENERATE_LABELS, isGenerateKind, type GenerateKind } from '$lib/env-generate';

	/**
	 * Type a value or Generate one (api-v1 v1.27 item 1): the source toggle and, for Generate, the kind
	 * select. Shared by the Add variables dialog and the value-request fill page; each keeps its own value input.
	 */
	let {
		idOf,
		source,
		kind,
		onSource,
		onKind,
		typeLabel = 'Type a value',
		generateDisabled = false,
		generateDescribedBy
	}: {
		/** Element id for a part name: source-label, source-value, source-generate, kind. */
		idOf: (part: string) => string;
		source: 'value' | 'generate';
		kind: GenerateKind;
		onSource: (next: 'value' | 'generate') => void;
		onKind: (next: GenerateKind) => void;
		typeLabel?: string;
		generateDisabled?: boolean;
		generateDescribedBy?: string;
	} = $props();
</script>

<div class="grid gap-1.5">
	<span id={idOf('source-label')} class="text-sm font-medium">Value</span>
	<ToggleGroup.Root
		type="single"
		variant="outline"
		size="sm"
		class="justify-start"
		aria-labelledby={idOf('source-label')}
		bind:value={
			() => source,
			(next) => {
				// Clicking the pressed item sends '': ignore it, so one item always stays pressed.
				if (next === 'value' || next === 'generate') onSource(next);
			}
		}
	>
		<ToggleGroup.Item value="value" id={idOf('source-value')}>{typeLabel}</ToggleGroup.Item>
		<ToggleGroup.Item value="generate" id={idOf('source-generate')} disabled={generateDisabled} aria-describedby={generateDescribedBy}>
			Generate
		</ToggleGroup.Item>
	</ToggleGroup.Root>
</div>
{#if source === 'generate'}
	<div class="grid gap-1.5">
		<Label for={idOf('kind')}>Kind</Label>
		<Select.Root
			type="single"
			value={kind}
			onValueChange={(next) => {
				if (isGenerateKind(next)) onKind(next);
			}}
		>
			<Select.Trigger id={idOf('kind')} class="w-full sm:w-64">
				<Select.Value>{GENERATE_LABELS[kind]}</Select.Value>
			</Select.Trigger>
			<Select.Content>
				{#each GENERATE_KINDS as item (item)}
					<Select.Item value={item} label={GENERATE_LABELS[item]}>{GENERATE_LABELS[item]}</Select.Item>
				{/each}
			</Select.Content>
		</Select.Root>
		<p class="text-muted-foreground text-sm">Deploybot makes the value. You see it once after saving.</p>
	</div>
{/if}
