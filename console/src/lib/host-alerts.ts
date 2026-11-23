import type { HostAlertSettings } from './api-types';

/**
 * Host resource alerts (api-v1 v1.49): the three per-host thresholds, their ranges and defaults, and the form
 * reading shared by the Alerts card and its action. An empty field means the default (null).
 */

export type AlertField = 'mem_min_available_percent' | 'disk_max_used_percent' | 'cpu_max_busy_percent';

export const ALERT_FIELDS: ReadonlyArray<{ name: AlertField; label: string; help: string; min: number; max: number; fallback: number }> = [
	{ name: 'mem_min_available_percent', label: 'Memory available below', help: 'for 10 minutes; the default also alerts under 96 MiB, a set value uses the percent alone', min: 5, max: 50, fallback: 15 },
	{ name: 'disk_max_used_percent', label: 'Disk used at or above', help: 'for 10 minutes, any mount', min: 50, max: 99, fallback: 90 },
	{ name: 'cpu_max_busy_percent', label: 'CPU busy at or above', help: 'for 15 minutes', min: 50, max: 100, fallback: 90 }
];

export const DEFAULT_ALERT_SETTINGS: HostAlertSettings = {
	alerts_enabled: true,
	mem_min_available_percent: null,
	disk_max_used_percent: null,
	cpu_max_busy_percent: null
};

/** A field's shown value: the number, or "Default (15 %)". */
export function alertValueText(settings: HostAlertSettings, field: (typeof ALERT_FIELDS)[number]): string {
	const value = settings[field.name];
	return value == null ? `Default (${field.fallback} %)` : `${value} %`;
}

/** The refusal text for a field, shared by the form check and its callers. */
export function alertFieldMessage(field: (typeof ALERT_FIELDS)[number]): string {
	return `${field.label}: enter a whole number from ${field.min} to ${field.max}, or leave it empty for the default.`;
}

/** Whether value is an allowed value of field: null (the default) or a whole number in its range. */
export function alertFieldValid(field: (typeof ALERT_FIELDS)[number], value: unknown): value is number | null {
	return value === null || (typeof value === 'number' && Number.isInteger(value) && value >= field.min && value <= field.max);
}

/**
 * The settings from the card's form: `alerts_enabled` present means on; each threshold is empty (default) or a whole
 * number in range. The error names the field in plain words.
 */
export function alertSettingsFromForm(form: FormData): { ok: true; settings: HostAlertSettings } | { ok: false; message: string } {
	const settings: HostAlertSettings = { ...DEFAULT_ALERT_SETTINGS, alerts_enabled: form.get('alerts_enabled') != null };
	for (const field of ALERT_FIELDS) {
		const raw = form.get(field.name);
		const text = typeof raw === 'string' ? raw.trim() : '';
		if (text === '') continue;
		const value = /^\d{1,3}$/.test(text) ? Number(text) : NaN;
		if (!alertFieldValid(field, value)) return { ok: false, message: alertFieldMessage(field) };
		settings[field.name] = value;
	}
	return { ok: true, settings };
}
