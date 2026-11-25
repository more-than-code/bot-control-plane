# A refused deploy keeps its plan under the API role (migration 0040)

Status: rev 1, approved by Joe on 2026-11-25 ("ok"). Migration 0040. No api-v1 or
agent-protocol change: this makes api-v1 v1.55 item 2 (spec-refused-deploy.md)
work as already specified. Covers DD items
DEF-20261123-refused-deploy-never-keeps-its-plan-miss-3vy7 and, through it,
DEF-20261123-a-refused-deploy-confirm-uses-up-the-pla-fsm5.

## Problem

`ReopenConfirmation` (`api/internal/db/catalog.go`) runs

```sql
UPDATE app.catalog_confirmations
SET used_at = NULL, expires_at = GREATEST(expires_at, $4) ...
```

`deploybot_api` holds only `UPDATE (used_at)` on that table (0007, lines
131-132); no later migration adds `expires_at`. Postgres checks column
privileges when it plans the statement, so every reopen fails with SQLSTATE
42501. `reopenPlan` (`api/internal/api/catalog_image.go`) logs the error and
returns "", so a host-refused deploy or rollback still uses up its plan and
the answer never says it was kept. It fails closed: a functional bug, not a
security one.

Reproduced on the cloud test database (PostgreSQL 18, migrated to 0039) as
`deploybot_api`:

- `UPDATE app.catalog_confirmations SET used_at = NULL, expires_at = GREATEST(expires_at, now()) WHERE token_hash = '\x00' RETURNING expires_at`
  answers `permission denied for table catalog_confirmations`;
- the same statement without the `expires_at` assignment succeeds.

The existing test (`catalog_integration_test.go:165-173`) logs in as the
owner test role and never hits the grant.

## Change

1. `api/migrations/0040_catalog_confirmations_expires_grant.sql`:
   `GRANT UPDATE (expires_at) ON TABLE app.catalog_confirmations TO deploybot_api;`
   Comment style as 0037 (no semicolon or apostrophe in comments).
2. `0040_catalog_confirmations_expires_grant.down.sql` (hand-run, not applied
   by `--migrate`): `REVOKE UPDATE (expires_at) ON TABLE app.catalog_confirmations FROM deploybot_api;`
   plus the `DELETE FROM app.schema_migrations` line, as 0037's down does.
   After it, reopens fail again (the v1.55 behaviour degrades to "plan used
   up", as today).
3. `api/internal/db/catalog_confirmations_api_role_test.go`:
   `TestReopenConfirmationAsAPIRole`, shaped like
   `TestHostUpdateNoticesAsAPIRole`. As `deploybot_api` it creates an image
   deploy confirmation, marks it used, reopens it, and checks `used_at` is
   NULL, `expires_at` moved to `until`, and that a second
   `MarkConfirmationUsed` succeeds. It fails on current dev with 42501.
4. `api/README.md`: one sentence beside the v1.55 reopen text (or the
   catalog confirmations paragraph) saying `deploybot_api` holds
   `UPDATE (used_at)` from 0007 and `UPDATE (expires_at)` from 0040, and why,
   as the 0037 sentence does for host_update_notices.

Not changed: the store interface, memstore, the handler, MCP, the console,
`service_confirmations` and `catalog_write_confirmations` (they never reopen,
so `UPDATE (used_at)` stays enough there).

## Contracts preserved

No public interface changes. api-v1 v1.55 item 2's text already describes
the behaviour this enables; no contract-status line is needed. The grant is
column-limited: the API still cannot change `token_hash`, `user_id`, the plan
or any other column.

## Rollout and rollback

- `bot-api-server --migrate` applies 0040 with the next release; it is a
  grant only, instant, and safe to run while the old build serves (the old
  build issues the same UPDATE and starts succeeding).
- If it was applied by hand on a server first, 0040 is a no-op there.
- Rollback: the down script above, or leave the grant (it is harmless to an
  older build).

## Down-and-forget helpers

0040 changes privileges on 0007's table. No test helper drops or recreates
`app.catalog_confirmations` (`dropEdgeSchema`, `forgetEdgeMigrations`,
`dropProblemsSchema` and `restoreEdgeMigration` touch other tables), so none
needs to forget 0040. The implementation PR rechecks this.

## Acceptance criteria

- As `deploybot_api`, `ReopenConfirmation` succeeds and the plan can be
  confirmed again (the new test).
- The new test fails without 0040 (shown in the PR).
- Migration up, down, up again on the test database.
- Gates: `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...`
  as a non-root user with `DEPLOYBOT_TEST_DATABASE_URL` and
  `DEPLOYBOT_TEST_API_DATABASE_URL` set (0 skipped in `api/internal/db`),
  and `go test ./api/internal/db/ -run 'Confirmation|Migrat'`.
