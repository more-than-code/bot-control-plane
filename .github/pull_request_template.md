## What and why

<!-- One or two sentences. Link the Defect Drainer item, if any. -->

**Tier:** 1 / 2 / 3. For Tier 3: spec `docs/spec-….md`, approved by Joe on YYYY-MM-DD ("quote").
**Contract:** none / api-v1 v1.N / agent-protocol v1.N (next free number checked against open and merged PRs in both repos).
**Migration:** none / `00NN_….sql` (helpers that forget older migrations updated: yes / not needed).

## Gates

| Gate | Command | Result |
|---|---|---|
| Format | `gofmt -l .` | |
| Build, vet | `go build ./... && go vet ./...` | |
| Go tests | `go test ./...` | |
| Console | `pnpm check && pnpm test` (in `console/`) | |
| Database | `go test ./api/internal/db/ -run '…'` with a test DB, migration up/down/up | run / **not run: no test DB** |

## Review

<!-- Who reviewed it: a fresh session per lens, or left for Main Dev. A /code-review in the authoring session is a self-check. -->

## Not done or not checked

<!-- Anything skipped, cancelled tests, follow-ups. -->
