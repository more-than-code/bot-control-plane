# bot-control-plane

The Deploybot server side: the API (`api/`), the MCP gateway (`mcp/`) and the web console (`console/`). One Go module (`github.com/more-than-code/bot-control-plane`) builds the server binary `cmd/deploybot-server` (`deploybot-server api [--migrate]` or `deploybot-server mcp`; called as `bot-api-server` or `bot-mcp-server`, it runs that mode) and the user CLI `cmd/deploybot`. The host agent is the separate repo bot-agent.

Tenancy (api-v1 v1.24): an organization owns hosts and contains projects. Org owners and admins manage hosts, join tokens and per-project host access (`metrics` or `deploy`) and act as admin on every project in the org. Projects keep their own members and roles; a project member need not be in the org. The agent sees only project slugs. Detail: `api/docs/design.md` D38.

The public site (deploybot.dev) is the static app `site/` in bot-control-plane (`site/README.md`), deployed to Cloudflare Pages by ops with `site/scripts/deploy.sh`; until the cutover the console still serves the same pages.

## Rules for bot-control-plane

- The contracts in bot-control-plane's `docs/contracts/` (`api-v1.md`, `oauth-v1.md`, `agent-protocol-v1.md`) are frozen copies of the umbrella source; report problems instead of editing them. They hold only implemented versions. Which version is implemented where: `docs/contract-status.md`.
- Do not print, log, or commit `.env` or any secret. Log only a short redacted error line.
- Do not commit unless the user asks. Do not push or add a remote.
- Do not apply SQL, install or reload Telegraf, restart systemd units, or change pi4 from an agent session.

## Gates (bot-control-plane)

Change gates, for each pull request in bot-control-plane; paste each command and its result into the pull request body (`.github/pull_request_template.md`):

- `gofmt -l .` prints nothing, then `go build ./...`, `go vet ./...` and `go test ./...`.
- A console change: `pnpm check` and `pnpm test` in `console/`.
- A database change (a migration, `api/internal/db`, `api/internal/store`, memstore parity): also `go test ./api/internal/db/ -run '<the changed area>'` against a test database, and the migration's up, down and up again. Without `DEPLOYBOT_TEST_DATABASE_URL` those tests skip. The test database is PostgreSQL 18 with TimescaleDB, set up as `api/README.md` describes (a database named `deploybot_test`, `migrations/roles.sql.example` applied, both `DEPLOYBOT_TEST_*` variables set). Run the tests as a non-root user: some agent and file tests behave differently as root.
- A new table or a new query on a table needs a test that runs the store code as `deploybot_api` (see `TestValueRequestsAsAPIRole`), because the test login owns every table and never hits a missing grant: `SELECT … FOR UPDATE` needs UPDATE, `ON CONFLICT DO UPDATE` needs UPDATE on the set columns (0035 shipped without it on 2026-11-24).
- A new migration that changes a table an older migration created: add it to what the down-and-forget test helpers forget (`dropEdgeSchema`, `forgetEdgeMigrations`, `dropProblemsSchema`, `restoreEdgeMigration`). Otherwise `Migrate` rebuilds the table with the old definition while yours stays recorded, and later tests fail (0036 did this to 0023's check on 2026-11-24).
- A new api-v1 version: add one sentence for it to `docs/contract-status.md`. Main Dev copies the contract section into the umbrella source and the frozen copies at release.

Release gates, run by Main Dev on `git archive` of the version commit: the full Go suite with a test database (0 fail, 0 skip), the db package twice more, the linux build twice and `cmp`-equal, and console install, check, test and build. A change gate failure blocks the commit; a release gate failure blocks the tag and the push. A failure caused by the environment (a shared test database, machine load) is rerun on its own and recorded as such. Only one branch uses a test database at a time.

Database access: one Postgres role and password per application, limited to its own database (CONNECT only there; no superuser, CREATEDB or CREATEROLE). Deploybot uses `deploybot_owner` (owns the schema), `deploybot_migrator` (runs migrations) and `deploybot_api` (the API at run time). The `postgres` superuser is for administration only, never for an application.

## Go: API and MCP (bot-control-plane)

From the bot-control-plane root: `go build ./... && go vet ./... && go test ./...`; binaries: `go build -o bin/ ./cmd/...`.

- API (`api/`): package `api` exposes `Run`, started by `deploybot-server api`. Design: `api/docs/design.md`. Do not connect to pi4 or any shared database except through the `DATABASE_URL` you are given. Do not create roles or databases. Database tests run only when `DEPLOYBOT_TEST_DATABASE_URL` is set; without it they skip.
- MCP (`mcp/`): package `mcp` exposes `Run`, started by `deploybot-server mcp`; `deploybot login|logout` is `mcp/cli`, built from `cmd/deploybot`. Design: `mcp/docs/design.md`. No database. One client secret that only allows exchanging user tokens; never act without a user token. Calls to the API `/v1` use only the delegated token from that exchange.
- Organizations are read-only over MCP (`list_orgs`; `list_projects` shows each project's org; `list_hosts` takes `project_id` or `org_id`). Do not add an MCP tool for org, membership, join-token or host-access writes: the API refuses delegated sessions on those routes.

## Console (`console/` in bot-control-plane)

SvelteKit 2 dashboard (not 3), a BFF for the API; adapter `@sveltejs/adapter-node`, config in `svelte.config.js`, imports via `$lib`, private env via `$env/dynamic/private`. `BOT_API_URL` is required; there is no Postgres login. Contract: `docs/contracts/api-v1.md`. Design, code map, conventions and shared components: `console/docs/design.md` (section "Code map and conventions").

Node 22 or newer (`console/package.json` engines); on a machine whose system Node is older, put a newer user-local Node first on `PATH` and do not change the system Node:

```bash
export PATH="$HOME/.local/opt/node/bin:$PATH"
cd console && pnpm install && pnpm check && pnpm test && pnpm build
PORT=5180 HOST=0.0.0.0 node build
```

`pnpm dev` and `pnpm preview` bind `0.0.0.0:5180` with `strictPort`. Do not leave a dev server running; a temporary one uses a port other than 5180 and 5176 and is stopped.

Hard rules (detail in the design doc):

- The browser never calls the API; server loads and form actions do. JavaScript is required; URL state and server form actions stay; no `?confirm=` or `?catalog.edit=` path.
- Svelte 5 runes, Tailwind CSS v4, shadcn-svelte, bits-ui, `@lucide/svelte`. Do not edit `src/lib/components/ui/**` and do not add a dependency for a one-off widget.
- Colours are tokens in `src/app.css` only: no colour literal or inline colour style in a `.svelte` file outside `ui/**`, and no `forceMount` or `portalProps` outside it.
- Route pages use the shared components; `tests/components-reuse.test.ts` enforces it.
- `node --test` loads TypeScript directly: a value import from a module under test ends in `.ts`.
