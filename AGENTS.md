# bot-control-plane

The Deploybot server side: the API (`api/`), the MCP gateway (`mcp/`) and the web console (`console/`). One Go module (`github.com/more-than-code/bot-control-plane`) builds the server binary `cmd/deploybot-server` (`deploybot-server api [--migrate]` or `deploybot-server mcp`; called as `bot-api-server` or `bot-mcp-server`, it runs that mode) and the user CLI `cmd/deploybot`. The host agent is the separate repo bot-agent.

Tenancy (api-v1 v1.24): an organization owns hosts and contains projects. Org owners and admins manage hosts, join tokens and per-project host access (`metrics` or `deploy`) and act as admin on every project in the org. Projects keep their own members and roles; a project member need not be in the org. The agent sees only project slugs. Detail: `api/docs/design.md` D38.

The public site (deploybot.dev) is the static app `site/` in bot-control-plane (`site/README.md`), deployed to Cloudflare Pages by ops with `site/scripts/deploy.sh`; until the cutover the console still serves the same pages.

## Rules for bot-control-plane

- The contracts in bot-control-plane's `docs/contracts/` (`api-v1.md`, `oauth-v1.md`, `agent-protocol-v1.md`) are frozen copies of the umbrella source; report problems instead of editing them. They hold only implemented versions. Which version is implemented where: `docs/contract-status.md`.
- Do not print, log, or commit `.env` or any secret. Log only a short redacted error line.
- Commit and push only on your own branch and open pull requests into `dev`; never push to, merge into or rewrite `main`. Do not add a remote.
- Do not apply SQL, install or reload Telegraf, restart systemd units, or change pi4 from an agent session.

## Gates (bot-control-plane)

Change gates, for each pull request in bot-control-plane; paste each command and its result into the pull request body (`.github/pull_request_template.md`):

- `gofmt -l .` prints nothing, then `go build ./...`, `go vet ./...` and `go test ./...`.
- A console change: `pnpm check` and `pnpm test` in `console/`.
- A database change (a migration, `api/internal/db`, `api/internal/store`, memstore parity): also `go test ./api/internal/db/ -run '<the changed area>'` against a test database, and the migration's up, down and up again. Without `DEPLOYBOT_TEST_DATABASE_URL` those tests skip. The test database is PostgreSQL 18 with TimescaleDB, set up as `api/README.md` describes (a database named `deploybot_test`, `migrations/roles.sql.example` applied, both `DEPLOYBOT_TEST_*` variables set). Run the tests as a non-root user: some agent and file tests behave differently as root.
- A new table, query or written column needs an explicit `deploybot_api` grant and a test that runs the store code as that role (`TestValueRequestsAsAPIRole`); a migration that changes an older table joins the down-and-forget test helpers. Detail: skill `deploybot-api-change`.
- A new api-v1 version: add one sentence for it to `docs/contract-status.md`. Main Dev copies the contract section into the umbrella source and the frozen copies at release.

Release gates, run by the release workflow (`.github/workflows/release.yml`, which also tags, builds and publishes when a version commit merges into `main`; bot-agent `docs/spec-release-workflow.md`) and by Main Dev when releasing by hand: the full Go suite with a test database (0 fail, 0 skip), the db package twice more, the linux build twice and `cmp`-equal, and console install, check, test and build. A change gate failure blocks the commit; a release gate failure blocks the tag and the push. A failure caused by the environment (a shared test database, machine load) is rerun on its own and recorded as such. Only one branch uses a test database at a time.

Sessions never approve a pending deployment and never change environments, secrets, rulesets or repository settings. A change to `.github/workflows/` or `scripts/release-bundle.sh` is Tier 3 and goes through a reviewed PR.

Database access: one Postgres role and password per application, limited to its own database (CONNECT only there; no superuser, CREATEDB or CREATEROLE). Deploybot uses `deploybot_owner` (owns the schema), `deploybot_migrator` (runs migrations) and `deploybot_api` (the API at run time). The `postgres` superuser is for administration only, never for an application.

## Go: API and MCP (bot-control-plane)

From the bot-control-plane root: `go build ./... && go vet ./... && go test ./...`; binaries: `go build -o bin/ ./cmd/...`.

- API (`api/`): package `api` exposes `Run`, started by `deploybot-server api`. Design: `api/docs/design.md`. Do not connect to pi4 or any shared database except through the `DATABASE_URL` you are given. Do not create roles or databases. Database tests run only when `DEPLOYBOT_TEST_DATABASE_URL` is set; without it they skip.
- MCP (`mcp/`): package `mcp` exposes `Run`, started by `deploybot-server mcp`; `deploybot login|logout` is `mcp/cli`, built from `cmd/deploybot`. Design: `mcp/docs/design.md`. No database. One client secret that only allows exchanging user tokens; never act without a user token. Calls to the API `/v1` use only the delegated token from that exchange.
- Privileged steps (secret values and files, host policy and access, logins, domains, alert settings) are the person's: the API refuses them to delegated (MCP) sessions, and MCP returns a `console_url` deep link (`mcp/internal/tools/links.go`).
- A new route chooses its `delegated` policy in `contractRoutes` (`api/internal/api/server.go`) and joins the lists in `delegated_routes_test.go`. The zero value `delegatedDeny` refuses writes; reads say `delegatedAllow` or `delegatedDenyRead`.
- `CHANGELOG.md` is embedded (`changelog.go`) and is what MCP `whats_new`, the MCP server instructions and the console show: add a plain-words bullet for each user-visible change.
- Skills in `.agents/skills/`: `deploybot-api-change` (route, store, migration) and `deploybot-mcp-tool`.
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
