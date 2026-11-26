---
name: deploybot-api-change
description: Checklist for a Deploybot API change in bot-control-plane that adds or changes a /v1 route, a store method, a table or a migration (api/migrations, api/internal/api, api/internal/db, api/internal/store, api/internal/memstore). Use before writing the spec or code.
---

# Deploybot API change: route, store, migration

A new route, a schema change or a migration is Tier 3 in this Project: write `docs/spec-<name>.md` and wait for Joe's approval before code. Use this list for the spec and again before the PR.

## Contract and numbering

- Pick the next api-v1 version: list open and recently merged PRs in both repos, take the next free `v1.N`, and put it in the PR title. Renumber on your branch if another PR merges first.
- Pick the next migration number the same way: the highest `api/migrations/NNNN_*.sql` on `origin/dev` plus open PRs. Claim it in the PR title.
- `docs/contracts/*.md` are frozen copies. Do not edit them. Add one sentence for the new version to `docs/contract-status.md`.
- A user-visible change gets a plain-words bullet in `CHANGELOG.md`. It is embedded (`changelog.go`) and is what MCP `whats_new`, the MCP server instructions and the console show.

## Route

- Register it in `contractRoutes()` in `api/internal/api/server.go` and choose its `delegated` policy on purpose (an MCP session is `api_delegated`):
  - `delegatedDeny` (the zero value) refuses a write from MCP. Use it for privileged steps: secrets and files, host policy and access, org and membership writes, logins, domains, alert settings.
  - `delegatedAllow` for a write MCP may do directly, which is rare. A deploy-style change from MCP goes through a plan and `POST /v1/confirmations/{id}` instead.
  - `delegatedHandler` when the handler decides (it calls `refuseDelegated`, which audits the denial).
  - Every read route says `delegatedAllow` or `delegatedDenyRead`. `TestDelegatedRouteGuard` fails otherwise.
- Add the pattern to `delegatedDenyPatterns` or `delegatedAllowPatterns` in `api/internal/api/delegated_routes_test.go` with a test case.
- Authorize in the handler with `authz` (project capability or org role). Check that the caller can see the object before checking the role: someone who cannot see it gets `not_found`, someone who can see it but lacks the role gets `forbidden`.
- Write an audit row (`api/internal/audit`, an `Action...` constant) for every write and every refusal of a privileged write.
- Validate input at the boundary: closed key sets, bounds and UUIDs. Errors use `writeCode` and never leak SQL, paths or stack traces.

## Store and parity

- Add the method to the store interface in `api/internal/store`, then implement it in both `api/internal/db` (Postgres) and `api/internal/memstore`.
- Cover the behaviour with a parity test in `api/internal/db/*_parity_test.go` that runs against both stores.

## Migration

- Add `api/migrations/NNNN_<name>.sql` and its hand-run `NNNN_<name>.down.sql`. `--migrate` applies only the up file. The down file ends by deleting its `app.schema_migrations` row, or says how to.
- Comments in migration files contain no semicolon and no apostrophe, because `splitSQL` does not skip comments.
- Grants: the API runs as `deploybot_api`, and the test login owns every table, so a missing grant never fails an ordinary test. Grant explicitly what the API does: `SELECT … FOR UPDATE` needs UPDATE, and `ON CONFLICT DO UPDATE` needs UPDATE on the columns it sets. A newly written column on an older table needs its own column grant (0037 and 0040 fixed two misses).
- Add a test that runs the new store code as `deploybot_api` through `DEPLOYBOT_TEST_API_DATABASE_URL` (pattern: `TestReopenConfirmationAsAPIRole`, `TestValueRequestsAsAPIRole`).
- A migration that changes a table an older migration created goes into the down-and-forget helpers (`dropEdgeSchema`, `forgetEdgeMigrations`, `dropProblemsSchema`, `restoreEdgeMigration`).
- Never apply SQL to pi4, us1 or any shared database. Production migrations are deployed on Joe's yes.

## Gates before the PR

- `gofmt -l .` prints nothing, then `go build ./...`, `go vet ./...` and `go test -count=1` on the changed packages, as a non-root user.
- With the test database (`scripts/cloud-setup.sh`): `go test ./api/internal/db/ -run '<area>'`, then the migration up, down and up again.
- Report skipped tests as skipped. A database test that skipped is a gate that did not run.
