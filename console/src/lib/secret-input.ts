/**
 * Secret inputs without the browser's "save password?" prompt.
 *
 * Browsers offer to save or update a password whenever a submitted form holds an
 * `<input type="password">`, and they ignore `autocomplete="off"` and `"new-password"`
 * for that. A Deploybot secret (an env value, a registry token) is not the user's
 * password and must not land in a password manager. So a masked secret is a
 * `type="text"` input drawn with dots by CSS (`-webkit-text-security`), and password
 * managers are told to ignore it. A browser without that CSS keeps `type="password"`,
 * so a secret is never shown in clear by accident.
 */

/** Tailwind arbitrary property: draws the text as dots. */
export const TEXT_MASK_CLASS = '[-webkit-text-security:disc]';

/** Attributes password managers honour to skip a field (1Password, LastPass, Bitwarden, Dashlane). */
export const PASSWORD_MANAGER_IGNORE = {
	'data-1p-ignore': '',
	'data-lpignore': 'true',
	'data-bwignore': '',
	'data-form-type': 'other'
} as const;

/** Whether this browser can mask a text input with CSS. False during server rendering. */
export function supportsTextMask(): boolean {
	return typeof CSS !== 'undefined' && typeof CSS.supports === 'function' && CSS.supports('-webkit-text-security', 'disc');
}

export type SecretInputProps = {
	type: 'text' | 'password';
	autocomplete: 'off' | 'new-password';
	/** Extra class for the input: the mask, or empty. */
	maskClass: string;
};

/**
 * The input type, autocomplete and mask class for a secret field.
 * @param hidden the value should be masked (not "Show value").
 * @param canMask the result of `supportsTextMask()` in the browser; false on the server.
 */
export function secretInput(hidden: boolean, canMask: boolean): SecretInputProps {
	if (!hidden) return { type: 'text', autocomplete: 'off', maskClass: '' };
	if (canMask) return { type: 'text', autocomplete: 'off', maskClass: TEXT_MASK_CLASS };
	return { type: 'password', autocomplete: 'new-password', maskClass: '' };
}
