# bot-control-plane

The Deploybot server side in one repo: the API (`api/`), the MCP gateway (`mcp/`) and the web console (`console/`). One Go module (`github.com/more-than-code/bot-control-plane`) builds the server binary `cmd/deploybot-server` (`deploybot-server api [--migrate]` or `deploybot-server mcp`; called as `bot-api-server` or `bot-mcp-server`, it runs that mode) and the user CLI `cmd/deploybot`. The host agent is the separate repo bot-agent.

Tenancy (api-v1 v1.24): an organization owns hosts and contains projects. Org owners and admins manage hosts, join tokens and per-project host access (`metrics` or `deploy`) and act as admin on every project in the org. Projects keep their own members and roles; a project member need not be in the org. The agent sees only project slugs.

Umbrella: `/Users/joe/workspace/deploybot` (cross-repo design `docs/design-bot-agent-deploy.md`, ledger `tasks/todo.md`).

The public site (deploybot.dev) is the static app `site/` (`site/README.md`), deployed to Cloudflare Pages by ops with `site/scripts/deploy.sh` (umbrella `docs/spec-site-pages.md`); until the cutover the console still serves the same pages.

## Rules for every part

- The contracts in `docs/contracts/` (`api-v1.md`, `oauth-v1.md`, `agent-protocol-v1.md`) are frozen copies of the umbrella source; report problems instead of editing them. They hold only implemented versions. Which version is implemented where, and which are not in the frozen copies yet: `docs/contract-status.md`.
- Do not print, log, or commit `.env` or any secret. Log only a short redacted error line.
- Do not commit unless the user asks. Do not push or add a remote.

## How the Deploybot team works

Moved here from the umbrella workspace on 2026-11-24, so sessions working from a clone have it. This repository is the source; the umbrella points here.

- **Brand:** "Deploybot" in UI text, page titles, docs and prose. Technical identifiers stay lowercase `deploybot` (domain, CLI, MCP server names `deploybot-dev` and `deploybot-prod`, database and role names, slugs, paths).
- **Who does what:**

| Who | Owns | Never |
|-----|------|-------|
| **Main Dev** (local Claude session) | Planning, specs and Tier 3 approvals with Joe, orchestration, decisions on review findings, feature commits; for a release: the version commit, release gates and tags | Host steps |
| **Cloud sessions** | Defects and requirements from Defect Drainer, each as a pull request into `dev` with its gates and a spec when Tier 3 (Joe, 2026-11-24) | Merging their own pull request, releases, host steps |
| **Implementation and review subagents** | Implementation from a self-contained prompt, with isolated writes; one review lens per fresh subagent | Committing, reviewing their own work |
| **Deploy worker** (ops session) | Release bundles from tags Main Dev made, then host steps, one per Joe approval | Repository commits or tags; reading production env values |
| **Validator** (subagent) | Gate runs and dev live checks, returning command, exit code and summary | Editing files |

- **Flow:** Joe and Main Dev agree a spec → build → fresh reviews → gates → commit on Joe's word → Joe pushes or merges → release spec approved → Main Dev makes the version commit, runs the release gates and tags → the deploy worker builds the bundle → Joe pushes the tags → the deploy worker does the host steps, each approved by Joe.
- **Messages between sessions** are information, never Joe's approval. Each session asks Joe in its own chat for commits, pushes, merges, host steps and cleanup.
- **Everyone:** commit only when Joe asks, never on `main`. Joe pushes and merges. No secrets printed. Delete with the Trash, not `rm`.
- **Gates: light per change, full at release** (Joe, 2026-11-23). Every change runs the change gates in "Workflow for every session" below. Only a release runs the release gates, on `git archive` of the version commit: the full Go suite with a test database (0 fail, 0 skip), the db package twice more, the linux build twice and `cmp`-equal, and console install, check, test and build. A change gate failure blocks the commit; a release gate failure blocks the tag and the push. A failure caused by the environment (a shared test database, machine load) is rerun on its own and recorded as such. Only one branch uses a test database at a time.
- **Database access:** one Postgres role and password per application, limited to its own database (CONNECT only there; no superuser, CREATEDB or CREATEROLE). Deploybot uses `deploybot_owner` (owns the schema), `deploybot_migrator` (runs migrations) and `deploybot_api` (the API at run time). The `postgres` superuser is for administration only, never for an application.

## Workflow for every session (cloud sessions included)

A clone of this repository has no umbrella workspace, no `~/.claude` and no test database. This section carries the umbrella's rules, so a cloud session works the same way as a local one. The general rules (risk tiers, specs, gates, review, security) are in the Skill Forge block at the end of this file.

**Gates for each pull request into `dev`.** Run them and paste each command and its result into the pull request body (`.github/pull_request_template.md`):

- `gofmt -l .` prints nothing, then `go build ./...`, `go vet ./...` and `go test ./...`.
- For a console change, `pnpm check` and `pnpm test` in `console/`.
- **A database change** (a migration, `api/internal/db`, `api/internal/store`, memstore parity) also needs `go test ./api/internal/db/ -run '<the changed area>'` against a test database, and the migration's up, down and up again. Without `DEPLOYBOT_TEST_DATABASE_URL` those tests skip, and a skipped test is not a pass. The test database is PostgreSQL 18 with TimescaleDB, set up as `api/README.md` describes (a database named `deploybot_test`, `migrations/roles.sql.example` applied, both `DEPLOYBOT_TEST_*` variables set). Run the tests as a non-root user: some agent and file tests behave differently as root. A new table or a new query on a table needs a test that runs the store code as `deploybot_api` (the role the server uses; see `TestValueRequestsAsAPIRole`), because the test login owns every table and never hits a missing grant: `SELECT … FOR UPDATE` needs UPDATE, `ON CONFLICT DO UPDATE` needs UPDATE on the set columns (0035 shipped without it on 2026-11-24). If you cannot set up the test database, write "Database gates not run" in the pull request body, and the pull request waits until Main Dev runs them.
- **A new migration that changes a table an older migration created:** some tests take the older migration down, forget it in `app.schema_migrations` and run `Migrate` again (`dropEdgeSchema`, `forgetEdgeMigrations`, `dropProblemsSchema`, `restoreEdgeMigration`). Add your migration to what those helpers forget. Otherwise `Migrate` rebuilds the table with the old definition while yours stays recorded, and later tests fail. Migration 0036 did this to 0023's check on 2026-11-24.

**Tools.** Use in-harness subagents only: no external workers (Grok, the `external-worker-delegation` pattern), whatever the Skill Forge block below says about them (Joe, 2026-11-24). Containers mean Docker; `podman-utilization` is a macOS workstation skill and is not vendored here.

**Review.** The session that wrote a change cannot be its reviewer: `/code-review` in the same session is a self-check, not a review. Ask for a fresh session per lens (spec, security, code, tests), or leave the review to Main Dev, and say in the pull request which one you chose.

**Specs and approval.** A change to an API or agent-protocol contract, a migration or a user-visible flow is Tier 3. Write the spec in `docs/spec-<name>.md`, including its contract diff, and get Joe's approval before writing code. Quote his words and the date in the spec and in the pull request.

**Contract version numbers.** Before you claim api-v1 v1.N or agent-protocol v1.N, list the open and recently merged pull requests in both repositories (`gh pr list --state all --limit 30`, here and in `more-than-code/bot-agent`) and take the next free number. Put it in the pull request title. If someone else merges that number first, renumber on your branch before merging. Add one sentence for the new version to `docs/contract-status.md`. Main Dev copies the contract section into the umbrella source and the frozen copies at release.

**Merging and releases.** Do not merge your own pull request: open it, report, and stop. Merging into `dev` is Joe's step. While a release is being cut (Main Dev has a version commit or release gates running), pull requests wait for the tag, because a merge moves `dev` under the release.

**After the merge.** If the pull request fixes a Defect Drainer item, mark that item resolved with the pull request link, so the ledger matches `dev`.

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

Node 22 or newer (`console/package.json` engines). On a machine whose system Node is older, put a newer user-local Node first on `PATH`; do not change the system Node.

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
src/lib/server/api.ts        API client (Bearer, envelope, 35s timeout; deploy, rollback and their confirm pass 80s, IMAGE_ACTION_WAIT_MS; a host-file apply passes 620s, the API's 610 s wait plus a margin, HOST_FILE_APPLY_WAIT_MS, on the default dispatcher)
src/lib/server/session.ts    bot_session cookie, safe next path, public routes
src/lib/server/guard.ts      503 when BOT_API_URL is missing or invalid; require a user
src/lib/host-view.ts         host tabs, metric series names, service and process columns, chart alignment
src/lib/agent-upgrade.ts     agent upgrade offer (Upgrade, opt-in, running), tooltip, state and reason words, poll step (api-v1 v1.48)
src/lib/server/agent-upgrade.ts  start an agent upgrade (140 s wait) and read the history
src/routes/hosts/[id]/agent-upgrade/  POST: start an upgrade (same origin, JSON); GET: the history, polled by the badge
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
src/lib/components/NewOrganization.svelte  New organization dialog (any signed-in user; posts /projects?/createOrg)
src/routes/projects/         projects (with org; New project, New organization and the createOrg action), hosts (access level, Leave), catalog & services, members, settings
src/routes/orgs/[id]/        org projects, hosts (per-project access), members, join tokens, settings
src/routes/hosts/[id]/       overview (disk mount picker), processes and config files tabs (ConfigFilesTab, PlainFileEditor, SecretFileEditor, SecretKeyDialog, FileDiff), Edge and Access sections (org admins), Alerts card (HostAlertsSection; everyone, editable by org admins)
src/routes/hosts/[id]/file-applies/  GET: the console's tracked host-file applies, polled by the Config files tab (no API call)
src/lib/host-alerts.ts       alert thresholds: fields, ranges, defaults, form reading (api-v1 v1.49)
src/lib/server/host-alerts.ts alert settings and mounts parsers and loads, the saveAlerts action
src/lib/host-files.ts        config files: status and outcome words, line diff, managed_url line, secret staging, apply poll step (api-v1 v1.44)
src/lib/server/host-files.ts config files load and actions (save, secret save, apply with a 25 s answer, tracked runs, a lost answer kept as result unknown)
src/lib/image-versions.ts    image versions of a ghcr.io entry: parse, stale groups, holder and delete text, the dialog read (api-v1 v1.45)
src/lib/server/image-versions.ts  image versions list and guarded delete actions
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
src/lib/hosts-refresh.ts     Hosts tabs' 30 s re-read while visible (one at a time) and the hosts.json reader
src/routes/{projects,orgs}/[id]/hosts.json/  GET { hosts }: the API host list for that re-read (lib/server/hosts-refresh.ts)
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
- Host ids are UUIDs. `/hosts/[id]?tab=overview|processes|files` (default overview; `files` is Config files, with `files.name=<name>` opening a file and `files.v=<n>&files.mode=view|diff` one saved version) and `?range=1h|6h|24h|7d` (default 1h), and on Overview `?disk=<mount>` for the disk chart (api-v1 v1.49; unknown or invalid is `/`, and a host with exactly one mount charts it without the parameter). Unknown tab or range falls back to those defaults. `?tab=services` redirects (303) to `/projects/<id>?tab=catalog&catalog.host=<host id>` for the first project with `deploy` access to the host: it checks at most the first 20 `/v1/me` projects (those in the host's org first, then session order) and uses the first whose host list has the host at `access_level` `deploy`. Each check is one page (`limit=200`) and does not follow `next_cursor`. If none match, the host page renders Overview. Range becomes `from`/`to`. `step` is omitted. The redirect does not keep `range`.
- Overview metrics are seven parallel `GET /v1/hosts/{id}/metrics` calls (`cpu`, `mem`, `disk`, `net`, `diskio`, `system`, `temp`). Memory draws Used and Available (`available / total`, api-v1 v1.49); the disk call adds `path` for the mount chosen with `?disk=` (default `/`), and Overview also reads `GET /v1/hosts/{id}/disks` for the picker, shown with more than one mount. A failed call sets that chart's message. The page still renders. Null points are gaps. The tab depends on `host:metrics` and invalidates it every 30 seconds while visible. Overview does not call `/services`.
- The project and org Hosts tabs re-read only their host list every 30 s while visible and once on return (`src/lib/hosts-refresh.ts`, `GET /projects/[id]/hosts.json` and `/orgs/[id]/hosts.json`, which call the same API lists as the page load and answer a failure with a status and a fixed message); a success replaces the shown list until the next load, a failure keeps it.
- Project Catalog & services (`/projects/[id]?tab=catalog`, every member) lists the catalog and, nested under each entry, its running copies: owned rows of this project from `GET /v1/projects/{id}/services?view=deploybot&include=health_checks&limit=200`, following `next_cursor` for at most 5 pages (then a notice). Discovered rows and other projects' rows are not shown; there is no Show select and no Project column. `?tab=services` redirects (303) to `?tab=catalog` with `services.q`, `services.host` and `services.logs` mapped to `catalog.*` and the old sort, cursor, page and `show` dropped. Sort, search (`catalog.q`, also matching a copy's host or label), the Kind filter and paging (25 entries, never splitting an entry) run in the browser. The host filter (`catalog.host`) offers only hosts this project has `deploy` access to (`access_level`); any other value is ignored and the page shows a Notice. Rename is a dialog on a copy that may be renamed. It posts `display_name` (max 64) to the `rename` action. A failure shows a Notice and keeps the typed value. A row with no `health_checks` key (an API older than v1.18) shows "Health checks need API v1.18 or later."; a malformed one shows "Health checks could not be read." Copy rows carry `id="service-{id}"`. A copy with `gone_at` set shows a stopped badge. Container states are read for at most 100 copies per load (then an info notice). Entries are collapsed by default (open state per project in per-tab storage, through `src/lib/catalog-open.ts` only); the Services column summarizes the copies by state, and an open entry shows a nested services table (`ServicesTable`: Host | Status | Port | Digest | Uptime | actions). An entry with no copy rows shows "Not deployed" only when the copy list was read completely; a program gets a "Not started" row per eligible host it never ran on, with Start only. Row actions sit in fixed slots with a "⋯" menu (`RowMenu`); below `sm` only the menu shows.
- Organizations (api-v1 v1.24, spec-organizations §4.3). Session orgs come from `/v1/me` `orgs`; a project or org that does not parse is dropped, not the session. The header's Org ▾ menu (`HeaderNav`) lists the user's orgs (links to `/orgs/<id>`), then New organization (any signed-in user, spec-new-org: `POST /v1/orgs`, the caller becomes owner and lands on `/orgs/<id>?tab=projects`; offered wherever the Org menu or the phone org items render, and always on `/projects`), with Organization settings for owners and admins; the org page has no breadcrumb. `/orgs/[id]?tab=projects|hosts|members|tokens|settings` (default projects); Hosts, Join tokens and Settings are for org owners and admins (others fall back to Projects). Org owners and admins add hosts (org join token, optional `grant`), rename live hosts in the org Hosts table (api-v1 v1.38), revoke credentials, remove revoked hosts from the lists (Remove in the org Hosts table, api-v1 v1.40; the host page then says when), and edit host access on the host page (Access section) and in the org Hosts table (per-host dialog). Remove access that returns 409 offers a second confirm that posts `reassign=1` (`?reassign=org`). A project admin can Leave a host on the project Hosts tab with the same 409 path. A project admin who is not an org admin sees "Ask an org admin to add a host." Join tokens are org-only; the project `?tab=tokens` lands on Hosts. There is no Catalog approvals UI. `programs.projects` (the runner.toml allowlist) is shown whenever the API sends it (org admins only); the Host policy line shows when it is present or the host carries `access`.
- Processes calls `GET /v1/hosts/{id}/processes?sort=cpu|memory&limit=200` and `GET /v1/hosts/{id}/services` for labels. A 403 on that service list leaves the process rows up with no labels and no links. DataTable search (`processes.q`) filters pid, process, service, user, and cmdline on the loaded rows. It is not sent to the API. `processes.sort=cpu|memory` (or the legacy `sort` param when the table sort is absent) is the API `sort`. Any other column sorts those rows in the page and the request uses `cpu`. The Process cell shows `process_name`. The Service cell shows `serviceLabel` (or the word `service` when the id is unknown) and, for an owned service with a catalog entry, links to `/projects/<service project>?tab=catalog&catalog.host=<host>&catalog.q=<entry>` only when that project is in the caller's `/v1/me` list. `catalog.q` is capped at 100 code points.
- `node --test` loads TypeScript directly. A value import from a module under test needs the `.ts` extension (`allowImportingTsExtensions`). Type-only imports are erased and can stay extensionless.

### Shared components

Route pages use the shared pieces below and the primitives in `src/lib/components/ui/`. Do not reimplement them under `src/routes/`. `tests/components-reuse.test.ts` fails if a route adds a `<table>`, a tab strip (`role="tablist"` or `class="nav tabs"`), `role="alert"`, `confirm(`, a `StatusText` import, or an `<h1` (the heading comes from `PageHeader`). It also fails on a colour literal or an inline colour style outside `src/lib/components/ui/**`, on `forceMount` or `portalProps` outside that folder, on a text-labelled button inside a DataTable `{#snippet row}`, and on a raw `<button` or `<input` outside `src/lib/components/ui/**` and `src/lib/components/*.svelte`.

The app shell header (spec `docs/spec-header-org-project-bell.md`, umbrella) is the logo, then `HeaderNav` (`src/lib/header-nav.ts` model): Org ▾ (the current org: the open project's, the open org page's, the host page's, else the first by name; trigger "Organization: <name>"; menu: the user's orgs with a check, New organization (any signed-in user; offered wherever the Org menu or the phone org items render, and always on `/projects`), Organization settings for owners and admins or Organization for members, All projects; a project-only member sees the org name from `/v1/me` `org_name` as plain text) and Project ▾ (trigger "Project: <name>"; the org's projects with a check, New project for org owners and admins; "All projects" on `/projects`, "Select a project" elsewhere; below `sm` only this menu shows and starts with the org items); then `NotificationsBell` (api-v1 v1.37: reads the console route `/notifications` after load, on open and, since v1.41, every 2 minutes while the tab is visible and the popover closed; a dot (red while an unread open problem needs action) and "Notifications, N unread, M need action"; problem rows (v1.41) with ServerOff, KeyRound or RotateCcw and "open 25 min" or "resolved 3 h ago"; a release row shows its CHANGELOG day ("24 Nov", in UTC) and other rows their age ("2 h ago"), each after the truncated detail so it is never cut off; a row click marks it read then navigates; Mark all read; See all changes → `/whats-new`), `ThemeSwitch`, and (when signed in) the user's name (an account menu with Connected apps, Open requests and What's new) and Sign out. The org and project pages have no breadcrumb. The signed-in row wraps, and the account trigger shrinks so a long name truncates instead of widening the page. At 640 px and below the theme switch is icons only (each item has an `aria-label` and a tooltip) and sits on the next header line. Wide tables scroll inside the table container. Dialogs use the generated portal and mount only while open.

- `DataTable` — every table, on the shadcn `table`. Sort headers and the pager are `button` links. Client sort, search, filters, and paging are pure functions in `src/lib/table.ts`, driven by namespaced URL params (`<id>.q`, `<id>.sort`, `<id>.dir`, `<id>.page`, `<id>.<filter>`). The one-shot `<id>.logs` is never carried on by table links or forms. A column's `firstDir` is the direction used the first time that column is chosen (`asc` when omitted). Clicking the active column still toggles. Process CPU, RSS, memory, and threads open descending. The processes table default is `defaultDir="desc"`.
- `Tabs` — host, project and org link strips (`aria-current="page"`), with a Lucide icon beside the label. Not a `tablist`.
- `PageHeader` — breadcrumbs (`breadcrumb`), title, optional subtitle and actions. Every routed page with an `h1`.
- `StatusBadge` — `badge` plus a CSS dot. Known labels: `online`, `offline`, `asleep`, `revoked`, `up`, `down`, `unknown`, `stopped`, `gone`, `succeeded`, `unchanged`, `failed`, `timeout`, `rolled_back`, `rollback_failed`, `image_unavailable`, `forbidden_spec`, `conflict`, `in_flight` (shown as "running", api-v1 v1.42). `stopped` uses the offline token and `gone` uses the unknown token. `succeeded` and `unchanged` use the up token, `failed`, `timeout`, `rollback_failed`, and `image_unavailable` use the down token, `rolled_back` uses the asleep token, `forbidden_spec` uses the revoked token, `conflict` uses the offline token, and `in_flight` uses the asleep token. Anything else renders as `unknown`. `StatusText` is gone.
- `Notice` — shadcn `alert`. `error` stays `role="alert"`. `info` and `success` are `role="status"`.
- `Toaster` — `ui/sonner`, mounted once at `bottom-center` in the root layout's app shell (never on site pages). It carries success confirmations, through `src/lib/success-toast.ts`, fired after the dialog has finished closing (`Saved <name>.`, `Saved health checks.`, `Added {email}.`, `Renamed <old> to <new>.`, `Removed <host>.`), and problem alerts (`src/lib/problem-alerts.ts`; umbrella `docs/spec-console-live-updates.md` §0): one toast per problem that newly needs action, staying until dismissed. With desktop notifications on and every console tab hidden, those alerts become system notifications instead (`src/lib/desktop-notify.ts`). Form errors and info stay inline as `Notice`.
- `SearchField` — shadcn `input` and `button`. The label is visually hidden.
- `CopyButton` — shadcn `button` with the Copy and Check icons. It stays a text button on the join-token card.
- `RowAction` — ghost icon button for every action inside a table row, with an `aria-label` and a tooltip (`"<Action> <row label>"`). Catalog Run on a container and Deploy append one sentence to that tooltip. Destructive rows use the destructive text colour. Rename (a service, or a host in the org Hosts table) is `Pencil`, probe `Activity`, revoke credential `ShieldOff`, remove member `UserMinus`, change role `UserCog`, revoke join token `Ban`, delete `Trash2`, remove a revoked host `Trash2`, manage host access `ShieldCheck`, change access level `KeyRound`, remove access `UserMinus`, leave a host `LogOut`.
- `RowMenu` — a row's "⋯" menu on the vendored `dropdown-menu`: one fixed action slot, items from `src/lib/row-menu.ts` (phone-only items for actions whose icons hide below `sm`), a 44 px button below `sm`. Items bump a counter that opens the row's dialog (`openRequest`); dialogs opened from it return focus to the ⋯ button (`fallbackFocusId`).
- `ThemeSwitch` — `toggle-group` with System, Light, and Dark (Monitor, Sun, Moon). The form posts `/theme` with `name="theme"` on each item, a hidden `theme`, and a hidden `next`. It sets the class immediately, then posts with `use:enhance` and does not follow the redirect. At 640 px and below the labels hide.
- `LocalTime` — one absolute timestamp in the viewer's time zone. Pass the ISO string as `iso`. A missing or unparseable value renders `n/a`. Chart axis labels use the same zone through `formatTick`.
- `ConfirmAction` — `alert-dialog` holding the POST form. Inside a table row the trigger is a `RowAction`. Delete project and delete organization set `typeToConfirm` so the user types the project or org name. Remove access, Leave (host), Leave organization and Transfer org ownership are ConfirmActions too; a failed one on the org page closes and focuses the section Notice (`ORG_ERROR_ID`). Confirm stays disabled until `typeToConfirmMatches` is true. The server check stays the authority. No hidden field carries that name. The dialog body is `ConfirmForm`.
- `AddHostSection`, `AddHostGuide`, `JoinTokenResult` — the Add a host card, its guide and the once-shown token, shared by the project and org Hosts tabs (org mode adds an optional project grant).
- `HostAccessEditor` — a host's project access (level select, Remove access, grant form, runner.toml note, 409 reassign confirm), on the host page and in the org Hosts dialog.
- `NewOrganization` — the New organization dialog (one Name field, Create and Cancel, a submit guard), on `/projects` and opened from the header's Org ▾ menu without its own trigger (showTrigger, openRequest, fallbackFocusId, ownResult).
- `NewProject` — the New project dialog, with an organization select when the caller administers several orgs. The header's Project ▾ menu also opens it without its own trigger (showTrigger, openRequest, fallbackFocusId, ownResult).
- `CommandText` — one truncated monospace line for a command, with the full text in `title` and a tooltip.
- `actionUrl`, `actionUrlFrom`, and `backTo` in `src/lib/url.ts` — named form actions, `?<params>&/<name>`. `actionUrlFrom` keeps the current query (including table state) and drops `confirm` and `/name` keys. Host and project forms use it. `actionUrl` builds a query when there is no current URL. `backTo` is the same path and query after a successful same-page action: it drops `confirm` and `/name` and does not append an action. Project revoke, member, join-token revoke, project rename, and service rename redirects use it. Delete project still redirects to `/projects`. Creating a project still redirects to the new project. Project probe and host snapshot do not redirect. The project `rename` action is shared: a `service_id` field patches the service; without it, the project name is patched.

Theme cookie `bot_theme` is `system`, `light`, or `dark` (`src/lib/theme.ts`). Missing or any other value is `system`. `POST /theme` is public (`isPublicPath`). The action stores the allowlisted value, never the raw form string. Cookie options: `HttpOnly`, `SameSite=Lax`, `Path=/`, max-age 31536000, `Secure` only when `url.protocol` is `https:`. `hooks.server.ts` `transformPageChunk` writes exactly one marker on the first `<html`: `class="dark"`, nothing, or `data-theme="system"`. The inline script in `src/app.html` uses `nonce="%sveltekit.nonce%"`. Early 503 responses return before `resolve`, so they do not get that marker.

Timestamps use the viewer's time zone (`src/lib/time-zone.ts`, `LocalTime`). The layout load reads cookie `bot_tz` and, when it is missing or not an IANA name, uses the server's zone. That same string is what the server HTML and the first client render share. After mount, if the browser zone differs, the page switches to it and stores `bot_tz` (`SameSite=Lax`, `Path=/`, max-age 31536000, `Secure` only on https, not `HttpOnly`). The date shape stays `en-NZ` with a 24-hour clock, and the zone abbreviation is the viewer's (NZDT, PDT, UTC, and so on). Chart ticks use that zone and do not print the abbreviation.

<!-- >>> skill-forge instructions full@0.1.0 >>> -->
# Agent Core Instructions (Spec-Driven, Risk-Tiered, Skills-Compatible)

This file contains shared process instructions for agentic coding tools. It is a source artifact, not a runtime target by itself.

Tool-specific files should include these rules and add only the runtime paths, delegation model, and instruction-loading behavior for that tool.

Domain expertise is packaged as Agent Skills in project and home-profile skill directories managed by Skill Forge (`skill-forge.json` + `skf sync`). This file references them by name.

### Skill Activation Protocol
1. **Discovery:** At session start, scan skill metadata only. Do not load full skill bodies during discovery.
2. **Discovery paths:**
   - Project skills: `.agents/skills/*/SKILL.md` — vendored by `skf sync` or hand-authored via `skill-forge.json` `skills.local`.
   - Home-profile skills (machine-wide): `~/.agents/skills/*/SKILL.md` — vendored by `skf home sync`.
   - Tool-specific project/user skill directories listed in the active tool's overlay.
3. **Metadata load:** Read frontmatter (`name`, `description`, and small metadata fields) only. These fields decide activation.
4. **Precedence and duplicates:**
   - Resolve same-name skills in this order:
     1. `.agents/skills/<name>/SKILL.md` (and the tool's project skill directory) for project skills.
     2. `~/.agents/skills/<name>/SKILL.md` (and the tool's user-level skill directory) for home-profile skills.
     3. The active tool's system/platform skills, only when they are relevant to the requested workflow.
   - Never activate more than one copy of the same skill name.
   - Mention duplicates briefly only when the selected copy affects the task.
5. **Activation priority:** Activate the minimal useful set, in this order:
   - Explicit user mention of a skill, plugin, file type, or domain.
   - Strong task/domain match from skill metadata.
   - Default implementation skills for code changes: `security-baseline`, `coding-discipline`, and `code-quality`.
   - Default UI/frontend implementation or review skill: `ui-portability-baseline` when full design-system governance is not required.
   - Risk-tier conditional skills from §3, such as `api-contracts`, `data-migration`, `production-readiness`, `failure-analysis`, `llm-integration`, or `audit-trail`.
   - Review-lens skills from §6, such as `testing-strategy`, `security-baseline`, `code-quality`, `frontend-engineering`, or `ui-portability-baseline`.
   - File-format, plugin, or tool-specific skills when the artifact or user request clearly calls for them.
6. **Container runtime activation:** Activate `podman-utilization` when a task involves Docker, Podman, Compose, containers, local service dependencies, image builds, container logs, port checks, or any Docker-like command. Use it as guidance for detecting the available runtime and translating Docker-style workflows to Podman when appropriate.
7. **Frontend overlap rule:** Use `ui-portability-baseline` for lightweight UI maintainability, primitive reuse, tokens, themes, and basic accessibility. Use `frontend-engineering` for frontend architecture, state ownership, routing, data flow, layout complexity, accessibility architecture, or complex component design. Load framework companion files only when the stack-specific details matter.
8. **Full load:** Read complete `SKILL.md` only after activation. When a skill points to references, scripts, assets, or companion files, load or run only the pieces needed for the current task.
9. **Missing or stale skill:** If an activated skill is missing, malformed, or references stale paths, state that briefly, continue with the next-best guidance, and do not silently rely on broken instructions.
10. **Project skill selection:** If the repo contains `skill-forge.json`, or the user asks which skills a project should use, activate `skill-forge-project` and manage project skills via the `skf` CLI (`project add`/`sync`) — never by hand-copying skill directories.
11. **Profile staleness check:** In a repo containing `skill-forge.json`, run `skf sync --check` before relying on its skills. It is read-only. Nothing outside the repo will ever run it — registry-side commands only know the registry checkout and `$HOME`, so a consumer they have not been run in is invisible and can lag indefinitely. On a non-zero exit, state which skills are affected and at what severity before doing the work, not after; `skill-forge-project` carries the triage and the `$HOME` shadowing comparison that `--check` cannot make.

### Skill Location Policy
- Skill placement is managed by Skill Forge profiles: the repo's `skill-forge.json` + `skf sync` for project skills, and the `$HOME` profile (`skf home init|add|sync`) for machine-wide skills.
- Never hand-copy skills into skill directories; change the owning profile and re-sync (the `skill-forge-project` skill covers the workflow).
- One checkout needing skills its repo's profile does not declare (typically a git worktree slice) gets them through the untracked local overlay — `skf project add <skill> --overlay`, then `skf sync` — never by editing the tracked profile it would then commit. Clear it with `skf project overlay --clear` before removing the worktree.
- Hand-authored project-specific skills are declared under `skill-forge.json` `skills.local` with their own name (a name cannot be both a registry dependency and a local skill); keep the delta narrow and point back to the registry skill it refines.
- Keep platform workflow skills in the owning tool's system directory.

**Goal:** Maximize quality per unit time. Rigor where risk is high, speed where risk is low.

**Philosophy:** Spec-driven development. Explore → Spec → Implement. Code is the last step.

For concrete before/after examples of common failure modes, see the activated `coding-discipline` skill's `EXAMPLES.md` companion (in whichever directory the skill was activated from).

---

## 1) Core Principles (Priority Order)

When principles conflict, higher rank wins.

1. **Security and safety** — Non-negotiable hard floor. All tiers, all times. Activate: `security-baseline`, `coding-discipline`
2. **Correctness and contract integrity** — Outputs satisfy contracts. Tests pass. Deterministic stays deterministic.
3. **Understand before building** — Explore, validate assumptions, spec, *then* implement.
4. **Readable and maintainable by default** — Optimize for human comprehension first. Code must be easy for both humans and agents to trace end-to-end.
5. **Simplicity first** — Minimum code that solves the problem. Nothing speculative.
6. **Outcome over ceremony** — Lightest process that satisfies #1–#5.
7. **Risk-proportional rigor** — Scale process to blast radius.
8. **No silent assumptions** — State and validate with code, tests, or data.

### Assumption Management
Before implementing any change:
- State assumptions explicitly. If uncertain, ask — don't guess.
- If multiple interpretations exist, present them. Don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.

---

## 2) Risk Tiers (Required First Step)

Classify by **blast radius** — how far damage travels if you get it wrong.

| Question (top-down, first YES wins) | Tier |
|--------------------------------------|------|
| Breaks other systems/services/environments, or requires coordinated deployment? | **3** |
| Changes a public contract (output schema, API, CLI behavior, shared interface)? | **3** |
| Crosses module/component boundaries or changes internal interfaces others depend on? | **2** |
| Fully contained — one module, one file, one function? | **1** |

When in doubt, tier up.

### Tier 1 — Contained
Nothing else breaks. Single-function fix, internal refactor, typo, log change.

### Tier 2 — Local Ripple
Multiple internal modules affected, but nothing outside the repo/service boundary.

### Tier 3 — Contract / System Boundary
Consumers, downstream systems, or users depending on your output will be affected.

| Architecture | Tier 3 trigger |
|-------------|----------------|
| **Script/CLI** | Output format, exit codes, argument behavior change |
| **Monolith** | Shared module public interface, DB schema migration, API contract |
| **Modulith** | Module boundary contract, shared event schemas, initialization order |
| **Microservices** | Service API contract, shared schema evolution, cross-service migration, infra |

### Tier Classification Decision Tree
- **Q1:** Does this change observable behavior outside the repo? → YES: Tier 3.
- **Q2:** Does this change a contract (API, schema, CLI output)? → YES: Tier 3.
- **Q3:** Does this affect multiple modules/components? → YES: Tier 2. Else: Tier 1.

| Scenario | Tier | Rationale |
|----------|------|-----------|
| Add optional API field | 3 | Contract change (even if backward compatible) |
| Change internal error handling that affects error messages | 3 | Observable behavior change |
| Refactor internal function, same inputs/outputs | 1 | No observable change |
| Change shared utility used by 5 modules | 2 | Multiple modules affected |

### Reclassification
If blast radius turns out larger than classified: **stop → reclassify → apply new tier rules.**

---

## 3) Explore → Spec → Implement

Depth scales with tier. Sequence is always: **understand → define success → build.**

### Goal Transformation (All Tiers)
Before exploring, transform the request into verifiable goals:
- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"
- "Make X faster" → "Measure current latency, set target, verify after change"

For multi-step tasks, state a brief plan with verification at each step:
```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

Strong success criteria let agents loop independently. Weak criteria ("make it work") require constant clarification.

### Tier 1
- **Explore:** Read relevant code. Confirm change is contained.
- **Spec:** 1-2 sentences — what changes, expected outcome.
- **Implement.**

### Tier 2
- **Explore** (isolated helper/subagent recommended when the active tool supports it — §7):
  - Read affected modules, tests, data flow.
  - Identify callers, consumers, side effects.
  - Validate assumptions with evidence.
- **Spec** — write before coding:
  - **Goal** — one sentence.
  - **Changes** — files/modules and what changes in each.
  - **Contracts preserved** — what stays the same (explicit).
  - **Acceptance criteria** — concrete, testable.
  - **Tests** — add/update.
  - **Verification** — commands and expected results.
- **Implement** per spec. Divergence → update spec first.

### Tier 3
- **Explore** (isolated helper/subagent mandatory when the active tool supports it; otherwise perform the same exploration explicitly in the main context and state that no helper capability was available — §7):
  - All Tier 2 exploration, plus:
  - Map downstream consumers and contracts.
  - Research alternatives and trade-offs.
  - Assess rollback feasibility.
  - Identify applicable skills (see below).
  - **Validate findings before spec:** After exploration produces a gap or recommendation list, re-evaluate each item against the actual execution path — not the abstract design. For each finding, ask: "If I remove this recommendation, does the end-to-end outcome change?" If not, it is an optimization or diagnostic, not a spec-worthy gap. This prevents over-recommending and keeps specs focused on architecturally load-bearing changes.
- **Spec** — write and get **explicit user approval** (see Tier 3 Spec Approval Protocol below):
  - **Goal** — problem and desired outcome.
  - **Approach** — chosen solution, alternatives rejected with rationale.
  - **Changes** — all files, modules, contracts, configs.
  - **Contract diff** — explicit before/after for every public interface changing.
  - **Acceptance criteria** — numbered, testable.
  - **Rollback strategy.**
  - **Tests** — new, updated, regression.
  - **Verification gates** — commands and expected outputs.
  - **Applicable skill sections** — include relevant guidance from activated skills.
- **Implement** per approved spec. Divergence → stop → update spec → re-approve.

#### Tier 3 Spec Approval Protocol
1. **Present spec** as markdown with clear sections (§3).
2. **Request approval:** "Approve this spec? (yes/no/modify)"
3. **Wait for response.** Do not proceed without explicit approval.
4. **Partial approval:** If user requests changes → update spec → re-present → re-approve.
5. **Approval record:** Log approval in `tasks/todo.md` with timestamp.

#### Tier 3 — Skill-Based Conditional Sections

Include in the spec when the change involves these concerns. Activate the relevant skill for detailed patterns and checklists.

| Concern | When to include | Skill | Phase |
|---------|----------------|-------|-------|
| Coding discipline | All implementation tasks (overengineering, assumptions, surgical changes) | `coding-discipline` | Implementation |
| Migration / deprecation | Changing a contract with active consumers | `migration-deprecation` | Exploration |
| Data migration | Schema, format, or storage changes | `data-migration` | Exploration |
| Cross-service coordination | Multiple services must update | `production-readiness` | Exploration |
| Deployment strategy | Can't deploy atomically or carries prod risk | `deployment-strategy` | Spec |
| Backward compatibility | Consumers can't all update simultaneously | `api-contracts` | Exploration |
| Infrastructure | DB, queues, networking, config changes | `production-readiness` | Exploration |
| Observability | Production behavior needs monitoring | `production-readiness` | Spec |
| Failure mode analysis | Partial rollout, async, distributed changes | `failure-analysis` | Exploration |
| LLM in the workflow | System invokes LLM as a processing step | `llm-integration` | Exploration |
| Audit and provenance | Outputs must be auditable, or agents and humans both modify artifacts | `audit-trail` | Spec |
| Container runtime usage | Docker, Podman, Compose, local service dependencies, image builds, logs, ports, or Docker-like commands | `podman-utilization` | Implementation / Review |
| UI portability baseline | Any frontend/UI implementation or review where full design-system governance is not required | `ui-portability-baseline` | Implementation / Review |
| Frontend / UI architecture | Component design, state management, layout, editor integration, real-time UI, accessibility architecture | `frontend-engineering` | Exploration |
| Cost-asymmetric delegation | An external agent is materially cheaper or faster and the primary's budget is binding | `external-worker-delegation` | Exploration |
### Spec is the source of truth
Implementation is measured against the spec. Verification confirms acceptance criteria. Review checks conformance. Wrong spec → fix spec first, then code.

---

## 4) Execution Rules

1. Change only what the spec calls for. No scope creep.
2. Preserve existing conventions and style. Match quote styles, spacing, naming conventions — even if you'd do it differently.
3. Simple and readable over clever.
4. Optimize for readability and maintainability first. New code should be easy for a human reader to understand without reconstructing hidden context.
5. Avoid spaghetti code. Prefer straightforward control flow, explicit data movement, and limited indirection over cleverness, dense abstractions, or logic spread across too many layers.
6. Minimum code that solves the problem. No premature abstractions, speculative features, or "flexibility" that wasn't requested. If 200 lines could be 50, rewrite it.
7. Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify before proceeding.
8. Never expose secrets in code, logs, tests, prompts, or output.
9. Never perform destructive operations without explicit user confirmation.
10. Verify write scope before writing. Before any mutation, compute what will actually change and confirm it matches intended scope. Unintended side effects are the #1 source of silent data corruption.
11. Don't "improve" adjacent code, comments, formatting, or style that isn't part of the task. Don't add docstrings or type annotations to code you didn't change.
12. When your changes create orphans (unused imports, variables, functions), clean those up. Don't remove pre-existing dead code unless asked — mention it instead.
13. The test: every changed line should trace directly to the request.

---

## 5) Verification (Always Required)

Verify against the spec's acceptance criteria. Evidence required: commands, results, outputs.
For Tier 2/3 command evidence, use Validation delegation (§7) when a validator helper is available; validator output maps directly onto the §5.2 Quality Gates block.

| Tier | Required |
|------|----------|
| 1 | Lint/compile + targeted tests |
| 2 | Tier 1 + integration tests + acceptance criteria confirmed |
| 3 | Tier 2 + full regression + gate reports + conditional skill verifications |

For deterministic pipelines: before/after artifact diff checks.

**Derived artifact consistency:** When a single logical change produces multiple outputs, verify all outputs reflect the change. Agents naturally focus on the primary artifact and forget derivatives.

**No tests exist?** Tier 1: add targeted test or document why with manual evidence. Tier 2/3: add tests — untested changes are not complete.

### 5.1) Mandatory Auto-Gate Enforcement (Tier 2/3)

This section is **normative**. `MUST`/`MUST NOT` are strict requirements.

- **Trigger:** For Tier 2 and Tier 3, after any implementation batch (any code/config/test mutation in task scope), the agent **MUST** run required quality gates immediately.
- **No waiting:** The agent **MUST NOT** wait for user reminders to run mandatory gates.
- **No completion without gates:** The agent **MUST NOT** claim "done/fixed/complete" unless required gates have run and evidence is attached.
- **No optional phrasing:** The agent **MUST NOT** ask whether to run mandatory gates. Gates are automatic by default.
- **Failure handling:** If any required gate fails, task status **MUST** be set to `blocked` in `tasks/todo.md`, then the agent **MUST** fix and re-run gates.
- **Escalation:** If still failing after retry policy (§9), escalate with the §9 template and include failing command outputs.
- **Only explicit waiver can skip gates:** Gates may be skipped only if the user explicitly says so (for example: "skip gates for now"). The agent **MUST** record waiver text and timestamp in `tasks/todo.md`.

### 5.2) Required Gate Evidence Format (Tier 2/3)

Every Tier 2/3 completion update must include this block:

```markdown
## Quality Gates
- Gate: [name]
- Command: `[exact command]`
- Exit code: [0/non-zero]
- Result: [pass/fail + brief summary]
- Artifacts: [path or n/a]
```

Validator helpers may also include `Cwd`, `Output summary`, and `Timestamp`; keep the required `Gate`, `Command`, `Exit code`, `Result`, and `Artifacts` fields in completion updates.

---

## 6) Quality Review (Risk-Scaled)

Eight review lenses:

| # | Lens | Skill |
|---|------|-------|
| 1 | Spec conformance | — |
| 2 | Code quality | `code-quality` |
| 3 | Test coverage | `testing-strategy` |
| 4 | Security | `security-baseline` |
| 5 | Documentation | — |
| 6 | Refactoring | `code-quality` |
| 7 | Architecture | — (see Architecture Review Lens below) |
| 8 | UI / Frontend | `ui-portability-baseline`; use `frontend-engineering` for architecture, state, layout, or complex component design |

### Required per tier
| Tier | Spec | Security | Code | Tests | Docs | Refactoring | Architecture | UI |
|------|------|----------|------|-------|------|-------------|--------------|-----|
| 1 | — | Required | Optional | Optional | Optional | Optional | — | — |
| 2 | Required | Required | Required | Required | If affected | Optional | — | If frontend |
| 3 | Required | Required | Required | Required | Required | Required | Required | If frontend |

For Tier 3, use a separate review subagent per lens when the active tool supports subagents (§7). If not, run the same review lenses sequentially in the main context and record that fallback.

**Findings:** Critical/High → fix or log approved debt with owner. Medium/Low → note and justify.

### Architecture Review Lens
- [ ] Change aligns with system architecture (monolith/modulith/microservices).
- [ ] No new coupling introduced (circular dependencies, shared state).
- [ ] Boundaries respected (module/service boundaries, data ownership).
- [ ] Scalability impact assessed (bottlenecks, resource usage).
- [ ] Technology choices justified (why this library/framework/pattern).

---

## 7) Delegation Strategy

Keep main context clean. Delegate bounded, independent work when the active tool supports safe parallel delegation. **Drive exploration before spec writing.**

This section is capability-based. Different tools may expose named subagents, helper agents, tasks, or no delegation at all. When delegation is unavailable, perform the same exploration and review steps in the main context and say so briefly.

Maintained tool-specific subagent definitions are installed in each tool's runtime agents directory. Do not assume subagent file formats are portable across tools.

When subagents are available, use one task per subagent and name descriptively.

Regardless of inherited context, every delegated prompt must be self-contained: include exact file paths and line numbers, the relevant acceptance criteria or constraints already decided, and the required output shape. Never reference "the plan above" or prior discussion.

For parallel batches, identify which results are required. Wait for every required result before synthesis. If an agent fails or remains incomplete, report that state and decide explicitly whether the remaining evidence is sufficient to proceed.

**Naming convention:** `[phase]-[scope]-[task]` (e.g. `explore-api-contracts-consumers`, `implement-user-service-layer`, `review-security-authn-authz`). Avoid generic names like `explore` or `fix-stuff`.

| Phase | Tier 1 | Tier 2 | Tier 3 |
|-------|--------|--------|--------|
| Exploration | — | Recommended if supported | **Mandatory if supported** |
| Validation | — | Recommended for command evidence | Recommended for command evidence |
| Implementation | — | Optional (3+ files) | Recommended |
| Review | — | Optional | Recommended (per lens) |

This table assumes an in-harness executor. When an external worker is engaged instead, see
**Orchestrator–worker delegation** below — it overrides the whole table.

### Exploration delegation (most important use)

Use before writing Tier 2/3 specs. Spawn helpers when supported; otherwise execute these checks directly:

- **Codebase analysis:** Trace interface, callers, dependencies of module X.
- **Behavior validation:** Run command, confirm assumption X.
- **Command evidence:** Use a `validator` helper when command execution may write caches, logs, snapshots, coverage, or other artifacts.
- **Impact assessment:** What depends on this? What breaks if it changes?
- **Alternatives research:** Compare approaches — trade-offs, cost, risk.
- **Dependency check:** Version compatibility, breaking changes.
- **Cross-system mapping** (Tier 3): Contract dependency graph. Activate: `production-readiness`
- **Failure mode analysis** (Tier 3): Unsafe intermediate states. Activate: `failure-analysis`

Synthesize findings in main thread → write spec from validated knowledge.

### Validation delegation
Use the maintained tool-specific `validator` subagent/custom agent for assigned commands, checks, and reproducible evidence. The validator returns command, cwd, exit code, result summary, relevant output excerpts, artifact paths, and timestamp. The parent passes validator results back to `Explore`/`researcher`, `Plan`/`planner`, or `reviewer` when downstream reasoning depends on command output. The validator must not intentionally edit source files.

### Implementation delegation
Split by layer/component only when the tool can keep edits isolated. Main thread owns orchestration. **No two helpers modify the same file.**

### Review delegation
Use one helper per lens (§6) when supported. Prefer the maintained tool-specific `reviewer` subagent/custom agent for this work so every lens returns the same severity-tagged shape. Each review returns: findings, severity, locations, reasoning, suggested fix. Without helpers, run each lens sequentially in the main context and use the same output shape. Built-in review commands may supplement this process, but do not replace it unless they can guarantee the required per-lens output format.

### Orchestrator–worker delegation (external worker)

An **external worker** is a separate agent CLI or service, not an in-harness subagent: its own
process, its own context, its own budget. Use one when it is materially cheaper or faster than
the primary and the primary's own spend is a binding constraint. The primary becomes the
**orchestrator** — it writes the brief, dispatches, and adjudicates what returns.

**This is a mode switch, not a mix.** While an external worker is engaged, the phase table above
routes to that worker: exploration, planning, implementation, validation, review. The
orchestrator does not also spawn in-harness helpers for the same work — those bill against the
budget the pattern exists to protect.

Two responsibilities never move:

- **Deterministic gates (§5)** — lint, build, test, artifact diffs — run in the orchestrator's own
  shell. A worker's gate run is triage; the §5.2 evidence block is the orchestrator's.
- **Acceptance** — the §6 lenses judge the returned work and the orchestrator is the reviewer of
  record, even when a worker session performed the lens pass.

Rules:

- **The brief is a file** in the working directory, not a prompt argument. The worker starts cold;
  the self-contained rule above applies with no exceptions.
- **Isolate writes.** Give the worker a scratch worktree or branch that the orchestrator merges,
  never the primary working tree — its intermediate steps are unobservable (§4 rule 10).
- **Independent review.** A delegated lens pass runs in a *fresh* worker session given the diff and
  the lens only. The session that authored the work cannot review it.
- **Verify artifacts, not claims.** A worker that exits successfully having produced nothing is a
  normal failure mode. Completion is measured on disk.
- **Track both budgets.** Worker cost and primary tokens. The figure that matters is primary
  tokens per accepted artifact.
- **No recursion.** An agent executing a brief *is* the worker for that brief and does not itself
  engage an external worker. Because these instructions are shared, a worker reads this section
  too and cannot tell from it which end of the delegation it is on — the brief settles that, so
  the brief must state the role, and the spawn must mark it (`SKILL_FORGE_AGENT_ROLE`) so tooling
  can tell the two apart without guessing.

Engaging a worker is an explicit user decision; record it as `**Delegation:** external-worker`
in the task block (§13) so it survives context compaction.

Method, per-phase brief structure, and the acceptance ladder: activate
`external-worker-delegation`. Process mechanics belong to the worker CLI's own transport skill.

---

## 8) Autonomous Bug Fixing

**Fix it.** Don't ask for repro steps, which file, or permission. If report is too vague, ask **one** question about *what* is broken.

Respects §15 restricted operations. Activate: `testing-strategy` (Bug Fix Protocol section).

### Sequence (strict order — no skipping)

1. **Explore** — trace behavior, errors, logs, code. Use helpers/subagents for complex cases when available.
2. **Diagnose** — root cause, not symptoms.
3. **Classify** — tier the *fix*. Tier 3 fix → spec + approval.
4. **Spec** — Tier 2/3 per §3. Tier 1: state change and rationale.
5. **Write reproducer tests FIRST** — before touching any production code:
   - Write new test(s) that **reproduce the exact bug** (they must FAIL on the current code).
   - Run them and confirm they fail. This is the proof the bug exists.
   - If the bug cannot be reproduced with a test, document why and provide manual evidence.
6. **Run existing test suite** — capture baseline. All existing tests must pass before the fix.
7. **Fix** — root cause, edge cases. Change production code only.
8. **Verify (3-gate check):**
   - **Gate A: New reproducer tests pass** — the bug is fixed.
   - **Gate B: Existing tests pass** — no regressions introduced.
   - **Gate C: If existing tests fail**, classify the failure:
     - **Test was wrong** (tested buggy behavior as correct) → update the test. Document what changed and why.
     - **Fix broke a working feature** → **STOP. Do not proceed.** Flag to user with details:
       ```
       ⚠ REGRESSION DETECTED
       Test: [test name]
       Expected: [what the test asserts]
       Actual: [what now happens]
       Reason: [why the fix causes this]
       Action needed: [user decision required]
       ```
     - **Feature design changed** (the fix intentionally alters behavior) → update the test to match the new design. Document the design change in the spec and explain why old behavior was incorrect.
9. **Prevent** — regression test (the reproducer from step 5 is now the permanent regression test), docs, check for similar issues elsewhere.

### Decision Tree for Failing Existing Tests After Fix

```
Existing test fails after fix
  │
  ├─ Was the test asserting buggy behavior?
  │   └─ YES → Update test. Document: "Test was validating the bug, not correct behavior."
  │
  ├─ Does the fix intentionally change feature behavior/design?
  │   └─ YES → Update test. Document the design change in spec.
  │            This requires Tier 2+ classification (behavior change = contract change).
  │
  └─ Is the fix breaking an unrelated working feature?
      └─ YES → STOP. Flag regression to user. Do not merge.
```

### Bug Fix Report Template

The report template lives in the `testing-strategy` skill (Bug Fix Protocol section), which this sequence already activates. Complete every bug fix with that report.

---

## 9) Error Recovery

**Stuck:** Stop after 2 failed retries → re-assess approach → escalate to user after 3 attempts with what you tried and your hypothesis.

**Escalation format:** Escalate under a `## Escalation: [Task Name]` heading with: each approach tried and why it failed, current state (what works, what doesn't), best-guess hypothesis at the root cause, blockers, and the specific question or permission needed.

**Scope changed:** Stop → reclassify tier → update spec → present to user.

---

## 10) Security (Non-Negotiable — All Tiers)

Activate: `security-baseline` for full checklists and patterns.

Hard rules (always apply):
- Never hardcode, commit, log, or expose secrets.
- Validate external inputs at boundaries. Allowlists over blocklists.
- Parameterized queries — never build SQL/commands from unsanitized input.
- Auth ≠ authz — check both. Least privilege.
- Established crypto only. No MD5/SHA-1/DES/RC4.
- Never leak stack traces, paths, schemas, or keys to users.
- Never log passwords, tokens, PII, or secrets.
- Dependencies: minimal, pinned, auditable, scanned.

---

## 11) Policies

**Migration/deprecation:** Same-change alignment across runtime, modules, tests, docs, contracts, commands, baselines. Remove deprecated paths once new is accepted. Activate: `migration-deprecation`

**Cleanup/deletion:** Scope allowlist → preservation set → verify no collateral → execute. Never outside approved scope.

**Doc drift (HARD — same unit of work as code):** Contract, tool/API surface, user-visible flow, or architecture changes **must** update owning docs **in the same implementation batch and the same per-intent commit** as the code. Do **not** ship a code-only commit and a follow-up docs commit for the same intent (unless the user explicitly asks to split). Pure internal refactors with identical external behavior may mark docs N/A. Default implementation skill `coding-discipline` owns the checklist: resolve owners via project `docs/docs-owners.md` when present → co-author with code → **open owners even if clean in git** → Completeness re-read vs final behavior → stage docs with code → emit **Docs commit report** in the commit reply (`Owning docs` / `Completeness: Y|N` / umbrella glossary note). If Completeness is N, **do not** commit. Maintain Key Design Decisions log when architecture decisions change.

---

## 12) Lessons Learned

`tasks/lessons.md` — systemic failures, repeated patterns, high-impact misses only.

```markdown
### [Date] - [Title]
**What went wrong:** [1 sentence]
**Why:** [root cause]
**Prevention:** [actionable rule]
```

**Session start:** Read `tasks/lessons.md` → apply relevant rules.

---

## 13) Task Tracking

- `tasks/todo.md` — active task queue for specs, plans, and completion state while work is still in flight. One section per active task:
  ```
  ## [Task Title]
  **Tier:** [1/2/3]  **Status:** [planned | in-progress | blocked | complete]  **Delegation:** [in-harness | external-worker]  **Date:** YYYY-MM-DD
  ### Spec
  - Goal: [one sentence]
  - Changes: [files/modules]
  - Acceptance criteria: [numbered, testable]
  ### Progress
  - [ ] Exploration  - [ ] Implementation  - [ ] Verification
  ### Gates
  - [ ] Mandatory gates executed automatically post-implementation
  - [ ] Evidence logged (commands, exit codes, summary)
  - [ ] If failed: status set to blocked and rerun/fix loop tracked
  ```
- `tasks/archive.md` — historical record for completed or superseded task items moved out of `todo.md` to keep the active queue small. Archived blocks use the same format as `todo.md`, with **Status** set to `complete`, `cancelled`, or `superseded` and an added `**Archived:** YYYY-MM-DD` field.
- `tasks/lessons.md` — durable learning. One entry per lesson, using the §12 template.

Archival rule:
- Keep `planned`, `in-progress`, and `blocked` tasks in `todo.md`.
- Once a task is `complete` and its gates/evidence are recorded, move the full task block to `tasks/archive.md`.
- Record Tier 3 approvals, gate waivers, and blocked-state history in the task block before archiving so the archive remains self-contained.

Tier 2/3: track progress in `todo.md` until archived.

**Delegation** records which executor the task runs on (§7). It defaults to `in-harness`;
set `external-worker` only on an explicit user decision, and note the decision in the task
block. The field is task-scoped on purpose — engaging an external worker authorizes spend
against a separate budget, and that authorization expires when the task archives rather than
persisting across the workspace. Re-read the active task block when resuming work: the
conversation that set the mode does not survive context compaction, but the file does.

---

## 14) Context Management

- Don't load everything. Search, grep, targeted reads.
- Architecture docs and READMEs first.
- Helper/subagent exploration for unfamiliar modules when available (§7).
- On context exhaustion: helper summaries if available, write to `tasks/notes.md`, summarize state to file.

---

## 15) Permissions

**Allowed:** Read any file. Edit source (Tier 1 freely; Tier 2/3 after spec approval in-chat). Run build/test/lint. Create files per structure. Install deps (after approval).

**Restricted (explicit confirmation required):** Delete files. Modify prod credentials/config. Commit/push. Modify `.git/`. Destructive commands (DROP, rm -rf, etc.).

### Git operations (mandatory)

These rules apply in every repo and every multi-repo workspace. Runtime targets under `~` are deployments of this policy — do not weaken them locally.

1. **No commit unless asked.** Never run `git commit`, `git push`, or create a commit via any wrapper unless the user explicitly requests it (e.g. "commit", "create a commit", "push"). Drafting a proposed commit message in chat is fine; performing the commit is not. "Looks done" is not permission to commit.
2. **Only the active workspace / cwd.** Never commit (or stage for commit) in a repository **outside** the user's current workspace root (or an explicitly named path in their request). Sibling repos under an umbrella are in-scope only when the user asked for that umbrella/repo; do **not** opportunistically commit `~/…`, Skill Forge inventory, or other projects just because you edited them. If a change must land elsewhere, say so and wait for an explicit go-ahead naming that path.
3. **Never commit on `main` or `master` without asking.** Before any commit, check `git branch --show-current` (or equivalent). If the branch is `main` or `master` (or the repo's default production branch), **stop** and ask for explicit confirmation — even if the user said "commit". Prefer a feature/`dev` branch unless they clearly authorize that default branch.
4. **Per-repo, per-intent commits.** Prefer one commit per repository and per distinct intent. If work spans sibling repos (umbrella workspaces), commit separately in each child repo. Do not mix unrelated intents (e.g. a bugfix + an unrelated refactor) in one commit.
5. **No WIP commits.** Do not commit half-done, broken, checkpoint, or "WIP" code. Prefer leaving a dirty working tree (or stashing only if the user asks) over a WIP commit. Commit only when the intent is complete enough that the tree is a coherent, reviewable unit.
6. **Docs with code (same intent).** When the intent required owning-doc updates (§11 Doc drift), those files **must be staged and committed with the code** in that repo's intent commit. Do not produce a code-only commit and a later docs-only commit for the same feature/fix. If docs are still missing when the user asks to commit, update docs first, then commit once. Empty `docs/` in `git status` is **not** N/A — open owners from `docs/docs-owners.md` (or skill fallback), patch if stale, then stage. Commit reply must include the coding-discipline **Docs commit report**.
7. **Same-issue multi-commit cleanup: rebase.** When several *local, unpushed* commits all address the same issue or fix, prefer interactive rebase (or soft-reset + single commit) into a clean history before the user pushes. Do not rewrite history that is already shared/pushed without explicit approval. Force-push and amend of published commits remain restricted and require explicit confirmation.

---

## 16) Communication

Every response:
1. **What changed** — files, behavior, scope
2. **Why** — reasoning, spec reference
3. **Evidence** — commands, results
4. **Open items** — risks, assumptions, limitations

Concise, factual. No filler.

Tier 2/3 completion responses **must** include a `## Quality Gates` section in the §5.2 format.
If gates are waived, explicitly state waiver text + timestamp + risk.

---

## 17) Quick Checklists

### Tier 1
- [ ] Confirmed contained (Tier 1)
- [ ] Implemented
- [ ] Owning docs co-delivered if contract/UX changed (else N/A)
- [ ] Tests pass (add if none exist)
- [ ] Security self-check. Activate: `security-baseline`
- [ ] No unintended changes
- [ ] Summary: what, why, evidence

### Tier 2
- [ ] Exploration complete
- [ ] Spec with acceptance criteria
- [ ] Implemented per spec
- [ ] Unit + integration tests pass
- [ ] Mandatory gates auto-ran immediately after implementation (no user prompt)
- [ ] Gate evidence attached in response
- [ ] Spec, security, code, test lenses reviewed
- [ ] Docs co-delivered with code (same batch; same intent commit when committing) — not a follow-up
- [ ] Summary with spec reference + evidence

### Tier 3
- [ ] Exploration complete (subagents/helpers mandatory when supported; fallback documented otherwise)
- [ ] Full spec with applicable skill-based sections
- [ ] Spec approved by user
- [ ] Implemented per spec
- [ ] Full test/gate suite passes
- [ ] Mandatory gates auto-ran immediately after implementation (no user prompt)
- [ ] Gate evidence attached in response
- [ ] Conditional verifications per applicable skills
- [ ] Artifact diffs reviewed (if deterministic)
- [ ] All 8 review lenses completed
- [ ] Runtime/tests/docs/contracts aligned **in the same work batch** (not a later docs pass)
- [ ] Design decisions updated
- [ ] Final report: spec, evidence, risks, open items

---

## Appendix: Quick Reference

| Task | Tier | Required Skills | Spec Approval? |
|------|------|-----------------|----------------|
| Fix typo in error message | 1 | security-baseline, coding-discipline, code-quality | No |
| Add new API endpoint | 3 | api-contracts, security-baseline, coding-discipline, code-quality | Yes |
| Refactor shared utility | 2 | security-baseline, coding-discipline, code-quality | No |
| Database schema change | 3 | data-migration, failure-analysis, security-baseline, coding-discipline, code-quality | Yes |
| Local services with containers or Compose | 2 | podman-utilization, security-baseline, coding-discipline, code-quality | No |
| Deploy to production | 3 | deployment-strategy, production-readiness | Yes |
| New frontend flow with routing + state | 3 | frontend-engineering, ui-portability-baseline, security-baseline, coding-discipline, code-quality | Yes |
<!-- <<< skill-forge instructions <<< -->
