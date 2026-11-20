// Links for the public site (deploybot.dev): a static site on Cloudflare Pages, separate from the
// console (umbrella docs/spec-site-pages.md).

/** The console's sign-in page. Absolute: the site and the console are separate hosts. */
export const SIGN_IN_URL = 'https://console.deploybot.dev/signin';

export const SUPPORT_EMAIL = 'support@deploybot.dev';

/**
 * Public source repos, optionally a folder in one (bot-control-plane holds api/, mcp/ and console/).
 * Links use main, the latest release (each repo's default branch); dev is work in progress.
 */
export const REPO_URL = (repo: string, dir?: string) =>
	`https://github.com/more-than-code/${repo}/tree/main${dir ? `/${dir}` : ''}`;

/** The product's front-door repo (umbrella README). */
export const GITHUB_URL = 'https://github.com/more-than-code/deploybot';

/** The bot-agent install guide, on main like the repo links. */
export const INSTALL_GUIDE_URL = 'https://github.com/more-than-code/bot-agent/blob/main/docs/install.md';

/** The public site's origin (the apex). Canonical, og:url and sitemap URLs use it on every host, pages.dev included. */
export const SITE_URL = 'https://deploybot.dev';
