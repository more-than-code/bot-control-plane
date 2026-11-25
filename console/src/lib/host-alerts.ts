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

/** The deep-link parameter of each threshold (api-v1 v1.60); `alerts.enabled` is `on` or `off`. */
export const ALERT_LINK_PARAMS: Readonly<Record<AlertField, string>> = {
	mem_min_available_percent: 'alerts.mem',
	disk_max_used_percent: 'alerts.disk',
	cpu_max_busy_percent: 'alerts.cpu'
};

/** Every alert deep-link parameter, dropped from the URL on Save so a reload does not stage again. */
export const ALERT_LINK_KEYS: readonly string[] = ['alerts.enabled', ...Object.values(ALERT_LINK_PARAMS)];

/** Whether the URL carries any alert deep-link parameter. */
export function hasAlertLink(params: URLSearchParams): boolean {
	return ALERT_LINK_KEYS.some((key) => params.has(key));
}

function onOff(on: boolean): string {
	return on ? 'on' : 'off';
}

/**
 * The change an MCP deep link stages on the Alerts card (api-v1 v1.60): `alerts.enabled=on|off`, and `alerts.mem`,
 * `alerts.disk`, `alerts.cpu` as a whole number in range or `default`. A missing parameter keeps the stored value; a
 * repeated, out-of-range or unreadable one is dropped and counted. `changed` names the fields that differ from `base`,
 * and `changes` says each one as "old → new". The link never saves anything.
 */
export function alertsStagedFromUrl(
	params: URLSearchParams,
	base: HostAlertSettings
): { values: HostAlertSettings; changed: Array<AlertField | 'alerts_enabled'>; changes: string[]; dropped: number } {
	const values: HostAlertSettings = { ...base };
	const changed: Array<AlertField | 'alerts_enabled'> = [];
	const changes: string[] = [];
	let dropped = 0;
	const enabled = params.getAll('alerts.enabled');
	if (enabled.length === 1 && (enabled[0] === 'on' || enabled[0] === 'off')) {
		values.alerts_enabled = enabled[0] === 'on';
	} else if (enabled.length > 0) {
		dropped++;
	}
	if (values.alerts_enabled !== base.alerts_enabled) {
		changed.push('alerts_enabled');
		changes.push(`Alerts ${onOff(base.alerts_enabled)} → ${onOff(values.alerts_enabled)}`);
	}
	for (const field of ALERT_FIELDS) {
		const raw = params.getAll(ALERT_LINK_PARAMS[field.name]);
		if (raw.length === 0) continue;
		const text = raw.length === 1 ? raw[0].trim() : '';
		const value = text === 'default' ? null : /^\d{1,3}$/.test(text) ? Number(text) : NaN;
		if (!alertFieldValid(field, value)) {
			dropped++;
			continue;
		}
		values[field.name] = value;
		if (value !== base[field.name]) {
			changed.push(field.name);
			changes.push(`${field.label} ${alertValueText(base, field)} → ${alertValueText(values, field)}`);
		}
	}
	return { values, changed, changes, dropped };
}
