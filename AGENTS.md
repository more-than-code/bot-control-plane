# bot-control-plane

The Deploybot server side in one repo: the API (`api/`), the MCP gateway (`mcp/`) and the web console (`console/`). One Go module (`github.com/more-than-code/bot-control-plane`) builds the server binary `cmd/deploybot-server` (`deploybot-server api [--migrate]` or `deploybot-server mcp`; called as `bot-api-server` or `bot-mcp-server`, it runs that mode) and the user CLI `cmd/deploybot`. The host agent is the separate repo bot-agent.

Tenancy (api-v1 v1.24): an organization owns hosts and contains projects. Org owners and admins manage hosts, join tokens and per-project host access (`metrics` or `deploy`) and act as admin on every project in the org. Projects keep their own members and roles; a project member need not be in the org. The agent sees only project slugs.

Umbrella: `/Users/joe/workspace/deploybot` (cross-repo design `docs/design-bot-agent-deploy.md`, ledger `tasks/todo.md`).

The public site (deploybot.dev) is the static app `site/` (`site/README.md`), deployed to Cloudflare Pages by ops with `site/scripts/deploy.sh` (umbrella `docs/spec-site-pages.md`); until the cutover the console still serves the same pages.

## Rules for every part

- The contracts in `docs/contracts/` (`api-v1.md`, `oauth-v1.md`, `agent-protocol-v1.md`) are frozen copies of the umbrella source; report problems instead of editing them. They hold only implemented versions. The api-v1 copy includes v1.24 (organizations, phase 1), implemented on dev 2026-11-14 (spec `docs/spec-organizations.md`). v1.24.1 (an image entry accepts the other kind's wait field at 0, so GET then PUT round-trips; spec `docs/spec-release-v0231.md` §2.7) is implemented on dev 2026-11-14. v1.25 (shared networks: ImageSpec `networks`, `HostPrograms.shared_networks`, two deploy/rollback gates; spec `docs/spec-cross-project-network.md` rev 2 with §12 amendments) is implemented on dev 2026-11-15, with agent-protocol v1.13. v1.26 (organizations phase 2: `Host` loses `project_id`, `relation` and `share_level`; the shares and catalog-approval routes answer `410 gone` from a stub; migration 0016, forward only; spec `docs/spec-organizations.md` §4.1 and §5) is implemented on dev 2026-11-15. v1.27 (env generate, renumbered from v1.20: generated secret values, show-once reveal, `${KEY}` references, env_hash to agents; migration 0017; spec `docs/spec-env-generate.md` rev 4) is implemented on dev 2026-11-15 for items 1–3, 7, 8 and 10; items 4–6 (phase B: create env sets with key names over MCP, delegated reads, `needs_value`) are not implemented yet. agent-protocol v1.14 (env hash, renumbered from v1.10) is implemented in bot-agent with it. bot-agent keeps its own copy of `agent-protocol-v1.md`. v1.37 (notifications and the header, migration 0024) is implemented on dev 2026-11-19. v1.38 (host names unique among live hosts, `PATCH /v1/hosts/{id}` rename, audit `host.rename`, migration 0025; spec `docs/spec-host-names.md` rev 1) is implemented on dev 2026-11-20. v1.39 (Connect GitHub failure logs and `last_failure` on connect requests, kept in memory; the fill page line-break hint; plain env keys named `_FILE`, `_PATH` or `_DIR` exempt from the secret-looking rule everywhere it checks a plain value (inline env refusal, which now names the key; the env-set plan reminder; the program run gate), one rule `catalog.LooksSecretNotPath`; `last_failure` only while the request is pending; no migration; spec `docs/spec-v0241-small-fixes.md` rev 1 with §8 amendments: A, B1, C1) is implemented on dev 2026-11-20. v1.40 (remove a revoked host from the lists: `POST /v1/hosts/{id}/archive`, `Host.archived_at`, org and project host lists and project services omit archived hosts, audit `host.archive`, migration 0026; spec `docs/spec-host-remove.md` rev 1) is implemented on dev 2026-11-20. v1.41 (problems in the bell: stored `host_offline`, `registry_login` and `deploy` problems in `app.problems`, bell items of kind `problem` with `level` and `state`, `GET /v1/orgs/{id}/problems` and `GET /v1/projects/{id}/problems`, MCP `list_problems`, `BOT_API_PROBLEMS=off`, migration 0027; spec `docs/spec-bell-problems.md` rev 1) is implemented in the `work-bell-problems` worktree 2026-11-21.
- Do not print, log, or commit `.env` or any secret. Log only a short redacted error line.
- Do not commit unless the user asks. Do not push or add a remote.

## Go (API and MCP)

From the repository root:

```bash
go build ./... && go vet ./... && go test ./...
go build -o bin/ ./cmd/...
```

## API (`api/`)

Go service; package `api` exposes `Run`, started by `deploybot-server api`. Design: `api/docs/design.md`.

Do not connect to pi4 or any shared database except through the `DATABASE_URL` you are given. Do not create roles or databases.

Database tests run only when `DEPLOYBOT_TEST_DATABASE_URL` is set; without it they skip.

## MCP (`mcp/`)

Go service and CLI; package `mcp` exposes `Run`, started by `deploybot-server mcp`; the `deploybot login|logout` logic is in `mcp/cli`, built from `cmd/deploybot`. Design: `mcp/docs/design.md`.

No database. One client secret that only allows exchanging user tokens; never act without a user token. Calls to the API `/v1` use only the delegated token from that exchange.

Organizations are read-only over MCP (`list_orgs`; `list_projects` shows each project's org; `list_hosts` takes `project_id` or `org_id`). Do not add a tool for org, membership, join-token or host-access writes: the API refuses delegated sessions on those routes.

## Console (`console/`)

SvelteKit dashboard for Deploybot. Design v2 (2026-10-13): `console/docs/design.md`. The console is a BFF for the API. `BOT_API_URL` is required. There is no Postgres login. The frozen HTTP contract is `docs/contracts/api-v1.md` at the repository root.

Do not apply SQL, install or reload Telegraf, restart systemd units, or change pi4 from an agent session. Do not print `.env`.

### Commands

Always put user-local Node 24 on `PATH`. `/usr/bin/node` is v18. Do not change it.

```bash
export PATH="$HOME/.local/opt/node/bin:$PATH"
cd console
pnpm install
pnpm check
pnpm test
pnpm build
PORT=5180 HOST=0.0.0.0 node build
```

`pnpm dev` and `pnpm preview` bind `0.0.0.0:5180` with `strictPort`. Do not leave a dev server running. If you need a temporary server, use a port other than 5180 and 5176 and stop it.

SvelteKit 2 (not 3): config stays in `svelte.config.js`, imports use `$lib`, and private env is `$env/dynamic/private`. Adapter is `@sveltejs/adapter-node`.

### Layout

Paths are relative to `console/`.

```
src/hooks.server.ts          security headers, no-store, required BOT_API_URL, session gate, / → /projects
src/lib/server/api.ts        API client (Bearer, envelope, 35s timeout; deploy and rollback pass a longer one)
src/lib/server/session.ts    bot_session cookie, safe next path, public routes
src/lib/server/guard.ts      503 when BOT_API_URL is missing or invalid; require a user
src/lib/host-view.ts         host tabs, metric series names, service and process columns, chart alignment
src/lib/service-label.ts     service display label, rename rule, process link
src/lib/service-view.ts      copy status word, badge tooltip text, `catalog.logs` deep-link helpers
src/lib/catalog-copies.ts    group service rows under catalog entries, host filter, copy search text
src/lib/table.ts             client sort, search, filters, and paging
src/lib/url.ts               actionUrl, actionUrlFrom, and backTo for named form actions
src/lib/confirm.ts           type-to-confirm match and submit disabled state
src/lib/theme.ts             bot_theme cookie, html marker, class apply
src/lib/components/ui/       shadcn-svelte primitives (generated; import only)
src/lib/ranges.ts            1h / 6h / 24h / 7d → from/to
src/lib/format.ts            viewer time zone and number formatting
src/lib/time-zone.ts         bot_tz cookie and the viewer zone shared with LocalTime
src/lib/series.ts            merge helper kept for the old chart shape (not on the request path)
src/routes/+page.*           redirect / to /projects
src/routes/signin/           Google sign-in
src/routes/projects/         projects (with org), hosts (access level, Leave), catalog & services, members, settings
src/routes/orgs/[id]/        org projects, hosts (per-project access), members, join tokens, settings
src/routes/hosts/[id]/       overview and processes tabs, Access section (org admins)
src/lib/host-access.ts       access level text, grant choices, runner.toml note, access failure shape
src/lib/host-name.ts         host-name rule, revoked hosts last, rename eligibility (api-v1 v1.38)
src/lib/org-view.ts          org tabs, org member roles, org action data
src/lib/server/access-actions.ts  host access (grant, level, remove or leave; 409 → reassign), revoke credential, join-token body
src/routes/device/           device-code approval
src/routes/oauth/consent/    MCP consent (Allow / Deny)
src/routes/account/apps/     Connected apps and revoke
src/lib/header-nav.ts        header Org / Project model
src/lib/notifications.ts     bell label, route name, read body
src/lib/notifications-bell.ts  bell behaviour (refresh, stale guard, mark read, Mark all read), tested with stub fetch/goto
src/lib/server/notifications.ts  parse items and read body, one-time seen-version import
src/routes/notifications/    console route for the bell: GET list, POST read; proxies api-v1 v1.37
src/routes/theme/            public POST /theme sets bot_theme
tests/                       node:test, import paths end in .ts
```

### Conventions

- Svelte 5 runes. UI stack: Tailwind CSS v4, shadcn-svelte (`components.json` style `nova`, baseColor `neutral`, iconLibrary `lucide`), bits-ui, and `@lucide/svelte`. `cn()` is `src/lib/utils.ts`. Do not edit `src/lib/components/ui/**` and do not add a dependency for a one-off widget.
- `svelte-sonner` (pinned 1.2.1) is the shadcn-svelte dependency for `ui/sonner`. `ui/sonner/sonner.svelte` is adapted: its theme follows the console's `dark` class on `<html>` (`toasterTheme` in `src/lib/theme.ts`) instead of mode-watcher, which the console does not use. Like the adapted `ui/tabs`, keep that change when re-adding the component. Charts stay SVG in `LineChart.svelte` and use `var(--chart-n)`, `--border`, and `--muted-foreground`.
- Colours are tokens in `src/app.css` only (light on `:root`, dark on `.dark`, plus `--status-*`). No `#hex`, `rgb(`, `oklch(`, or `color-mix(in oklch` literal in a `.svelte` file outside `src/lib/components/ui/**`, and no inline colour style (`style=` or `style:`). Do not pass `forceMount` or `portalProps` outside that folder. Table-row actions are icon-only (`RowAction`).
- JavaScript is required. Dialogs, the theme switch, and chart refresh need it. URL state (search, sort, page, tab, filter) and server form actions stay. Do not add a `?confirm=` or `?catalog.edit=` path.
- The browser never calls the API. Server loads and form actions do.
- `BOT_API_URL` must be an origin with no path, query, or userinfo. `https`, or `http` for loopback, or `http` elsewhere only when `BOT_API_ALLOW_INSECURE_HTTP=1`. Anything else is 503 `Console is not configured (BOT_API_URL)` on every route except static assets.
- Host ids are UUIDs. `/hosts/[id]?tab=overview|processes` (default overview) and `?range=1h|6h|24h|7d` (default 1h). Unknown tab or range falls back to those defaults. `?tab=services` redirects (303) to `/projects/<id>?tab=catalog&catalog.host=<host id>` for the first project with `deploy` access to the host: it checks at most the first 20 `/v1/me` projects (those in the host's org first, then session order) and uses the first whose host list has the host at `access_level` `deploy`. Each check is one page (`limit=200`) and does not follow `next_cursor`. If none match, the host page renders Overview. Range becomes `from`/`to`. `step` is omitted. The redirect does not keep `range`.
- Overview metrics are seven parallel `GET /v1/hosts/{id}/metrics` calls (`cpu`, `mem`, `disk`, `net`, `diskio`, `system`, `temp`). A failed call sets that chart's message. The page still renders. Null points are gaps. The tab depends on `host:metrics` and invalidates it every 30 seconds while visible. Overview does not call `/services`.
- Project Catalog & services (`/projects/[id]?tab=catalog`, every member) lists the catalog and, nested under each entry, its running copies: owned rows of this project from `GET /v1/projects/{id}/services?view=deploybot&include=health_checks&limit=200`, following `next_cursor` for at most 5 pages (then a notice). Discovered rows and other projects' rows are not shown; there is no Show select and no Project column. `?tab=services` redirects (303) to `?tab=catalog` with `services.q`, `services.host` and `services.logs` mapped to `catalog.*` and the old sort, cursor, page and `show` dropped. Sort, search (`catalog.q`, also matching a copy's host or label), the Kind filter and paging (25 entries, never splitting an entry) run in the browser. The host filter (`catalog.host`) offers only hosts this project has `deploy` access to (`access_level`); any other value is ignored and the page shows a Notice. Rename is a dialog on a copy that may be renamed. It posts `display_name` (max 64) to the `rename` action. A failure shows a Notice and keeps the typed value. A row with no `health_checks` key (an API older than v1.18) shows "Health checks need API v1.18 or later."; a malformed one shows "Health checks could not be read." Copy rows carry `id="service-{id}"`. A copy with `gone_at` set shows a stopped badge. Container states are read for at most 100 copies per load (then an info notice). Entries are collapsed by default (open state per project in per-tab storage, through `src/lib/catalog-open.ts` only); the Services column summarizes the copies by state, and an open entry shows a nested services table (`ServicesTable`: Host | Status | Port | Digest | Uptime | actions). An entry with no copy rows shows "Not deployed" only when the copy list was read completely; a program gets a "Not started" row per eligible host it never ran on, with Start only. Row actions sit in fixed slots with a "⋯" menu (`RowMenu`); below `sm` only the menu shows.
- Organizations (api-v1 v1.24, spec-organizations §4.3). Session orgs come from `/v1/me` `orgs`; a project or org that does not parse is dropped, not the session. The header's Org ▾ menu (`HeaderNav`) lists the user's orgs (links to `/orgs/<id>`), with Organization settings for owners and admins; the org page has no breadcrumb. `/orgs/[id]?tab=projects|hosts|members|tokens|settings` (default projects); Hosts, Join tokens and Settings are for org owners and admins (others fall back to Projects). Org owners and admins add hosts (org join token, optional `grant`), rename live hosts in the org Hosts table (api-v1 v1.38), revoke credentials, remove revoked hosts from the lists (Remove in the org Hosts table, api-v1 v1.40; the host page then says when), and edit host access on the host page (Access section) and in the org Hosts table (per-host dialog). Remove access that returns 409 offers a second confirm that posts `reassign=1` (`?reassign=org`). A project admin can Leave a host on the project Hosts tab with the same 409 path. A project admin who is not an org admin sees "Ask an org admin to add a host." Join tokens are org-only; the project `?tab=tokens` lands on Hosts. There is no Catalog approvals UI. `programs.projects` (the runner.toml allowlist) is shown whenever the API sends it (org admins only); the Host policy line shows when it is present or the host carries `access`.
- Processes calls `GET /v1/hosts/{id}/processes?sort=cpu|memory&limit=200` and `GET /v1/hosts/{id}/services` for labels. A 403 on that service list leaves the process rows up with no labels and no links. DataTable search (`processes.q`) filters pid, process, service, user, and cmdline on the loaded rows. It is not sent to the API. `processes.sort=cpu|memory` (or the legacy `sort` param when the table sort is absent) is the API `sort`. Any other column sorts those rows in the page and the request uses `cpu`. The Process cell shows `process_name`. The Service cell shows `serviceLabel` (or the word `service` when the id is unknown) and, for an owned service with a catalog entry, links to `/projects/<service project>?tab=catalog&catalog.host=<host>&catalog.q=<entry>` only when that project is in the caller's `/v1/me` list. `catalog.q` is capped at 100 code points.
- `node --test` loads TypeScript directly. A value import from a module under test needs the `.ts` extension (`allowImportingTsExtensions`). Type-only imports are erased and can stay extensionless.

### Shared components

Route pages use the shared pieces below and the primitives in `src/lib/components/ui/`. Do not reimplement them under `src/routes/`. `tests/components-reuse.test.ts` fails if a route adds a `<table>`, a tab strip (`role="tablist"` or `class="nav tabs"`), `role="alert"`, `confirm(`, a `StatusText` import, or an `<h1` (the heading comes from `PageHeader`). It also fails on a colour literal or an inline colour style outside `src/lib/components/ui/**`, on `forceMount` or `portalProps` outside that folder, on a text-labelled button inside a DataTable `{#snippet row}`, and on a raw `<button` or `<input` outside `src/lib/components/ui/**` and `src/lib/components/*.svelte`.

The app shell header (spec `docs/spec-header-org-project-bell.md`, umbrella) is the logo, then `HeaderNav` (`src/lib/header-nav.ts` model): Org ▾ (the current org: the open project's, the open org page's, the host page's, else the first by name; trigger "Organization: <name>"; menu: the user's orgs with a check, Organization settings for owners and admins or Organization for members, All projects; a project-only member sees the org name from `/v1/me` `org_name` as plain text) and Project ▾ (trigger "Project: <name>"; the org's projects with a check, New project for org owners and admins; "All projects" on `/projects`, "Select a project" elsewhere; below `sm` only this menu shows and starts with the org items); then `NotificationsBell` (api-v1 v1.37: reads the console route `/notifications` after load, on open and, since v1.41, every 2 minutes while the tab is visible and the popover closed; a dot (red while an unread open problem needs action) and "Notifications, N unread, M need action"; problem rows (v1.41) with ServerOff, KeyRound or RotateCcw and "open 25 min" or "resolved 3 h ago"; a row click marks it read then navigates; Mark all read; See all changes → `/whats-new`), `ThemeSwitch`, and (when signed in) the user's name (an account menu with Connected apps, Open requests and What's new) and Sign out. The org and project pages have no breadcrumb. The signed-in row wraps, and the account trigger shrinks so a long name truncates instead of widening the page. At 640 px and below the theme switch is icons only (each item has an `aria-label` and a tooltip) and sits on the next header line. Wide tables scroll inside the table container. Dialogs use the generated portal and mount only while open.

- `DataTable` — every table, on the shadcn `table`. Sort headers and the pager are `button` links. Client sort, search, filters, and paging are pure functions in `src/lib/table.ts`, driven by namespaced URL params (`<id>.q`, `<id>.sort`, `<id>.dir`, `<id>.page`, `<id>.<filter>`). The one-shot `<id>.logs` is never carried on by table links or forms. A column's `firstDir` is the direction used the first time that column is chosen (`asc` when omitted). Clicking the active column still toggles. Process CPU, RSS, memory, and threads open descending. The processes table default is `defaultDir="desc"`.
- `Tabs` — host, project and org link strips (`aria-current="page"`), with a Lucide icon beside the label. Not a `tablist`.
- `PageHeader` — breadcrumbs (`breadcrumb`), title, optional subtitle and actions. Every routed page with an `h1`.
- `StatusBadge` — `badge` plus a CSS dot. Known labels: `online`, `offline`, `asleep`, `revoked`, `up`, `down`, `unknown`, `stopped`, `gone`, `succeeded`, `unchanged`, `failed`, `timeout`, `rolled_back`, `rollback_failed`, `image_unavailable`, `forbidden_spec`, `conflict`. `stopped` uses the offline token and `gone` uses the unknown token. `succeeded` and `unchanged` use the up token, `failed`, `timeout`, `rollback_failed`, and `image_unavailable` use the down token, `rolled_back` uses the asleep token, `forbidden_spec` uses the revoked token, and `conflict` uses the offline token. Anything else renders as `unknown`. `StatusText` is gone.
- `Notice` — shadcn `alert`. `error` stays `role="alert"`. `info` and `success` are `role="status"`.
- `Toaster` — `ui/sonner`, mounted once at `bottom-center` in the root layout's app shell (never on site pages). Success confirmations only, through `src/lib/success-toast.ts`, fired after the dialog has finished closing (`Saved <name>.`, `Saved health checks.`, `Added {email}.`, `Renamed <old> to <new>.`, `Removed <host>.`). Errors and info stay inline as `Notice`.
- `SearchField` — shadcn `input` and `button`. The label is visually hidden.
- `CopyButton` — shadcn `button` with the Copy and Check icons. It stays a text button on the join-token card.
- `RowAction` — ghost icon button for every action inside a table row, with an `aria-label` and a tooltip (`"<Action> <row label>"`). Catalog Run on a container and Deploy append one sentence to that tooltip. Destructive rows use the destructive text colour. Rename (a service, or a host in the org Hosts table) is `Pencil`, probe `Activity`, revoke credential `ShieldOff`, remove member `UserMinus`, change role `UserCog`, revoke join token `Ban`, delete `Trash2`, remove a revoked host `Trash2`, manage host access `ShieldCheck`, change access level `KeyRound`, remove access `UserMinus`, leave a host `LogOut`.
- `RowMenu` — a row's "⋯" menu on the vendored `dropdown-menu`: one fixed action slot, items from `src/lib/row-menu.ts` (phone-only items for actions whose icons hide below `sm`), a 44 px button below `sm`. Items bump a counter that opens the row's dialog (`openRequest`); dialogs opened from it return focus to the ⋯ button (`fallbackFocusId`).
- `ThemeSwitch` — `toggle-group` with System, Light, and Dark (Monitor, Sun, Moon). The form posts `/theme` with `name="theme"` on each item, a hidden `theme`, and a hidden `next`. It sets the class immediately, then posts with `use:enhance` and does not follow the redirect. At 640 px and below the labels hide.
- `LocalTime` — one absolute timestamp in the viewer's time zone. Pass the ISO string as `iso`. A missing or unparseable value renders `n/a`. Chart axis labels use the same zone through `formatTick`.
- `ConfirmAction` — `alert-dialog` holding the POST form. Inside a table row the trigger is a `RowAction`. Delete project and delete organization set `typeToConfirm` so the user types the project or org name. Remove access, Leave (host), Leave organization and Transfer org ownership are ConfirmActions too; a failed one on the org page closes and focuses the section Notice (`ORG_ERROR_ID`). Confirm stays disabled until `typeToConfirmMatches` is true. The server check stays the authority. No hidden field carries that name. The dialog body is `ConfirmForm`.
- `AddHostSection`, `AddHostGuide`, `JoinTokenResult` — the Add a host card, its guide and the once-shown token, shared by the project and org Hosts tabs (org mode adds an optional project grant).
- `HostAccessEditor` — a host's project access (level select, Remove access, grant form, runner.toml note, 409 reassign confirm), on the host page and in the org Hosts dialog.
- `NewProject` — the New project dialog, with an organization select when the caller administers several orgs. The header's Project ▾ menu also opens it without its own trigger (showTrigger, openRequest, fallbackFocusId, ownResult).
- `CommandText` — one truncated monospace line for a command, with the full text in `title` and a tooltip.
- `actionUrl`, `actionUrlFrom`, and `backTo` in `src/lib/url.ts` — named form actions, `?<params>&/<name>`. `actionUrlFrom` keeps the current query (including table state) and drops `confirm` and `/name` keys. Host and project forms use it. `actionUrl` builds a query when there is no current URL. `backTo` is the same path and query after a successful same-page action: it drops `confirm` and `/name` and does not append an action. Project revoke, member, join-token revoke, project rename, and service rename redirects use it. Delete project still redirects to `/projects`. Creating a project still redirects to the new project. Project probe and host snapshot do not redirect. The project `rename` action is shared: a `service_id` field patches the service; without it, the project name is patched.

Theme cookie `bot_theme` is `system`, `light`, or `dark` (`src/lib/theme.ts`). Missing or any other value is `system`. `POST /theme` is public (`isPublicPath`). The action stores the allowlisted value, never the raw form string. Cookie options: `HttpOnly`, `SameSite=Lax`, `Path=/`, max-age 31536000, `Secure` only when `url.protocol` is `https:`. `hooks.server.ts` `transformPageChunk` writes exactly one marker on the first `<html`: `class="dark"`, nothing, or `data-theme="system"`. The inline script in `src/app.html` uses `nonce="%sveltekit.nonce%"`. Early 503 responses return before `resolve`, so they do not get that marker.

Timestamps use the viewer's time zone (`src/lib/time-zone.ts`, `LocalTime`). The layout load reads cookie `bot_tz` and, when it is missing or not an IANA name, uses the server's zone. That same string is what the server HTML and the first client render share. After mount, if the browser zone differs, the page switches to it and stores `bot_tz` (`SameSite=Lax`, `Path=/`, max-age 31536000, `Secure` only on https, not `HttpOnly`). The date shape stays `en-NZ` with a 24-hour clock, and the zone abbreviation is the viewer's (NZDT, PDT, UTC, and so on). Chart ticks use that zone and do not print the abbreviation.
