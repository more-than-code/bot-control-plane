# Deploybot site (`deploybot.dev`)

The public site: the About page (`/`), `/privacy`, `/terms`, a 404 page, `llms.txt`, `robots.txt`,
`sitemap.xml`, the manifest and the brand files. It is a static site on Cloudflare Pages (project
`deploybot-site`), separate from the console and from the server release. Spec: umbrella
`docs/spec-site-pages.md` (rev 1, approved 2026-11-21).

**Until the cutover** (spec §3.4) the console still serves the same pages on `deploybot.dev` through
Caddy. This app was copied from the console's `(site)` route group, `src/lib/site`, `src/lib/legal`
and its static site files; the console copies go in the next server release after the apex moves.

## How it is built

SvelteKit 2 with `@sveltejs/adapter-static` (`strict: true`). `src/routes/+layout.ts` sets
`prerender = true`, `csr = false` and `trailingSlash = 'never'`, so every page is HTML written at build
time and the browser runs no JavaScript. `scripts/postbuild.mjs` deletes the `.js`, `__data.json` and
`version.json` files SvelteKit still emits. `build/` then holds only HTML, CSS, images and text files.

- `/` is the About page (`build/index.html`); `/privacy` and `/terms` are `privacy.html` and `terms.html`
  (Pages serves them without the extension).
- `src/routes/404/+page.svelte` is emitted as `build/404.html`, listed in `prerender.entries` because
  nothing links to it. Pages answers every unknown path with it and status 404, keeping the URL.
- `kit.paths.relative = false`: asset URLs are absolute (`/_app/...`), so `404.html` loads its CSS at any
  depth (`/a/b/c`).
- Dark mode follows the system: the Tailwind `dark` variant is `@media (prefers-color-scheme: dark)`
  and the dark tokens sit in the same media query. No script, no cookie, no theme switch (the apex has
  only ever run in system mode).
- Sign in and Get started link to `https://console.deploybot.dev/signin` (`src/lib/site/links.ts`).
- No page has a robots meta. Preview hosts and a direct `/404` request get `X-Robots-Tag: noindex` from
  `_headers` instead.

## Commands

Node 24 from `$HOME/.local/opt/node/bin` (see the repo `AGENTS.md`).

```bash
export PATH="$HOME/.local/opt/node/bin:$PATH"
cd site
pnpm install --frozen-lockfile
pnpm check          # svelte-check, warnings fail
pnpm test           # source tests (node --test)
pnpm build          # vite build + postbuild prune -> build/
pnpm guard          # tests/guard.build.ts over build/
pnpm build:diagram  # after editing design/pitch-diagram*.svg
```

`pnpm dev` and `pnpm preview` use `127.0.0.1:5190`. Local preview does not apply `_redirects` or
`_headers`; only Pages does.

## Tests

- `about-page`, `seo-site`, `site-pages`, `llms-txt`, `pitch-diagram`, `brand`: the console's site tests,
  adapted (no hooks or session; absolute sign-in links; `/` is About).
- `parity`: the site's theme tokens (`:root`, the dark block, `@theme inline`, `@layer base` in
  `src/app.css`) equal `console/src/app.css` (the console's `.dark` block is compared with the site's
  `prefers-color-scheme` block); the brand files the console keeps after the cutover (lockups,
  favicons, icons, `apple-touch-icon.png`, listed by name) are byte-equal; `favicon.ico`, `llms.txt`,
  `robots.txt` and `sitemap.xml` equal the console copies while the console has them (that check goes at
  the console cleanup, spec §3.4 step 5); the manifest differs only in `start_url` (`/` here). When the console's theme or brand files change,
  copy them here, or this test fails at the site gates. The console gates do not run it.
- `colour-literals`: the console's colour rule (same regexes as `console/tests/components-reuse.test.ts`):
  no `#hex`, `rgb(`, `oklch(` or `color-mix(in oklch` literal and no inline colour style in any `.svelte`
  file under `src/`, nor in `src/app.css` outside the light and dark token blocks. `src/app.html` (its
  `theme-color` metas) is the only file not scanned.
- `deploy-script`: `scripts/deploy.sh` with a stub `pnpm` and a fake `HOME` in a throwaway git repo
  (with a local bare repo as `origin` for the `origin/dev` rule; no network), run from `site/` and from the
  repo root.
- `guard.build.ts` (`pnpm guard`, needs `build/`): no `.js` file or `<script>`; no robots meta; every URL
  is on `deploybot.dev`, `console.deploybot.dev`, `mcp.deploybot.dev` or under
  `github.com/more-than-code/`, apart from links the site already had: `https://www.privacy.org.nz`
  (Privacy) and the SVG and sitemap XML namespaces; no `api2`/`console2`; no `/tree/dev` or `/blob/dev`
  links; the sitemap's three URLs; the robots `Sitemap:` line; `llms.txt` starts with `# Deploybot`;
  `_redirects` and `_headers` exactly as below; `404.html` exists with absolute asset URLs.

## Pages files (`static/_redirects`, `static/_headers`)

`_redirects` (spec §3.2). These URLs are indexed and linked today, so they keep working:

```
/about           /                                         301
/signin          https://console.deploybot.dev/signin      301
/signin/*        https://console.deploybot.dev/signin/:splat   301
/projects        https://console.deploybot.dev/projects    301
/projects/*      https://console.deploybot.dev/projects/:splat 301
```

`_headers`:

```
/*
  X-Content-Type-Options: nosniff
  X-Frame-Options: DENY
  Referrer-Policy: no-referrer

/llms.txt
  Content-Type: text/plain; charset=utf-8

/404
  X-Robots-Tag: noindex

https://:project.pages.dev/*
  X-Robots-Tag: noindex

https://:version.:project.pages.dev/*
  X-Robots-Tag: noindex
```

- The security headers match the console's `hooks.server.ts` for the site pages (`no-referrer`).
- The noindex rules use Cloudflare's documented host placeholders ("Prevent your pages.dev deployments
  showing in search results" in the Pages headers docs): `:project.pages.dev` is the project host
  (`deploybot-site.pages.dev`) and `:version.:project.pages.dev` every preview and branch alias host.
  The spec's wording `https://*.deploybot-site.pages.dev/*` is not usable as written: `_headers` allows
  one splat per rule, and it would miss the bare project host. A placeholder never matches across a
  `.`, so neither rule can match `deploybot.dev`, and no rule names the apex.
- `/404`: a direct request for `/404` answers 200 with the 404 page, so it gets noindex. An unknown path
  is served `404.html` with status 404 and keeps its own URL, which the `/404` rule does not match, so it
  carries no `X-Robots-Tag` (a 404 is not indexed anyway). `/404.html` needs no rule: Pages answers it with
  a 308 redirect to `/404`.
- Pages joins a header matched by two rules with a comma. Only `X-Robots-Tag` on `/404` of a `pages.dev`
  host matches two rules (`noindex, noindex`, still valid); every other header is set by one rule.
- Redirects are applied before headers.

## Deploy (ops, on Joe's yes)

Direct upload with the pinned Wrangler in `package.json`; no Git integration, nothing deploys by itself.
Joe created the Pages project `deploybot-site` and an API token limited to Cloudflare Pages edit. The
project's production branch is `main`, confirmed by the API when the project was created (2026-11-21).

| What | Where |
|---|---|
| API token (secret) | `~/.config/deploybot/cloudflare-pages-token`, mode 600, read only by `deploy.sh`, never printed |
| Account id (not secret, required for an upload) | `CLOUDFLARE_ACCOUNT_ID`, or `~/.config/deploybot/cloudflare-account-id` |

```bash
sh scripts/deploy.sh                 # preview: branch "preview", https://preview.deploybot-site.pages.dev
BRANCH=main sh scripts/deploy.sh     # production, after Joe's yes in the ops chat
DRY_RUN=1 sh scripts/deploy.sh       # every gate, no upload, reads no credentials
```

It also runs from the repo root as `sh site/scripts/deploy.sh`. In order, `deploy.sh`:

1. Refuses `DRY_RUN` or `ALLOW_DIRTY_FOR_TEST` set to anything but `0` or `1` (both default to `0`; an
   empty value is refused too), and a `BRANCH` that is not lowercase letters, digits and hyphens.
2. Refuses a dirty working tree (the whole repo).
3. For `BRANCH=main` (production) only: runs `git fetch -q origin dev` and refuses unless HEAD is already
   on `origin/dev` (`git merge-base --is-ancestor HEAD origin/dev`). A dry run applies this rule too.
4. Unless `DRY_RUN=1`: checks the token file exists with mode 600 and is not empty, and that an account id
   is set (`CLOUDFLARE_ACCOUNT_ID` or the account-id file); Wrangler needs both.
5. Runs install, check, test, build and guard.
6. Checks the tree is still clean and HEAD has not moved, so the upload is exactly that commit.
7. Prints the commit hash and the current git branch, then runs
   `pnpm exec wrangler pages deploy build --project-name deploybot-site --branch <branch> --commit-hash <HEAD> --commit-dirty=false`
   with `WRANGLER_SEND_METRICS=false` (a dry run prints this command instead).

`ALLOW_DIRTY_FOR_TEST=1` skips both clean-tree checks, and only together with `DRY_RUN=1`.

`BRANCH=main` is production because the Pages project's production branch is `main`. Any other branch is
a preview deploy, on `<branch>.deploybot-site.pages.dev` and a per-deploy `<hash>.deploybot-site.pages.dev`.

`pnpm install` does not run workerd's install script (`pnpm-workspace.yaml`): `wrangler pages deploy`
only uploads files and never starts the local runtime.

## Cutover

Spec §3.4: check the Pages host (pages, redirects, 404, noindex), Joe adds `deploybot.dev` as a custom
domain, ops runs the apex checks, the Caddy apex block goes after a day, and the next server release
drops the console's copies. Rollback before the Caddy step: remove the custom domain and restore the apex
A record to us1.
