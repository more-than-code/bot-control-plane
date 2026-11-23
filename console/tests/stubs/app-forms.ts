/** Server render does not run actions. The export keeps `use:enhance` resolvable. */
export function enhance(): Record<string, never> {
	return {};
}

/** The reveal dialog parses its own action fetch. Server render never calls it. */
export function deserialize(text: string): { type: string; data?: unknown } {
	return JSON.parse(text) as { type: string; data?: unknown };
}

/** Server render does not run actions, so nothing is applied. */
export function applyAction(): Promise<void> {
	return Promise.resolve();
}
