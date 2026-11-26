# The API learns the latest bot-agent release by itself

Status: rev 1, draft, waiting for Joe's approval. api-v1 **v1.62** (v1.61 is
claimed by PR #37, the secrets key in AWS KMS). No migration, no
agent-protocol change, no bot-agent change. Covers Defect Drainer
DEF-20261126-server-should-learn-the-latest-bot-agent-wzh9.

Joe's direction (project thread, 2026-11-26): "yes" to the coordinator's
suggestion that the server learns the latest bot-agent version automatically
instead of someone setting `BOT_API_LATEST_AGENT_VERSION` by hand at each
release.

## 1. Problem

Everything that knows "the newest bot-agent" reads one API setting,
`BOT_API_LATEST_AGENT_VERSION` (api-v1 v1.34 item 1,
`api/internal/config/config.go`, `Server.LatestAgentVersion`):

- `POST /v1/hosts/{id}/agent-upgrade` and MCP `upgrade_agent` accept only that
  version (`api/internal/api/agent_upgrade.go`, v1.48);
- every Host's `latest_agent_version` and `update_available`
  (`server.go` `withUpdateHint`), MCP `list_hosts`' hint;
- the bell's `host_update` items and `app.host_update_notices`
  (`notifications.go`, migration 0035);
- resolving an `agent_upgrade` problem when a host runs the latest or newer
  (`agentCurrent`).

Ops sets the value and restarts the API after every bot-agent release. When
bot-agent v0.2.19 was published on 2026-11-26 the value was still 0.2.17, so
the API refused every upgrade. Changing it needs root on the Deploybot host,
which the "computerless" plan rules out.

Since bot-agent `docs/spec-release-workflow.md` (agent-protocol v1.27), every
bot-agent release is published by the release workflow as a GitHub release of
`more-than-code/bot-agent` (`v<X.Y.Z>`, marked latest, immutable), with
`SHA256SUMS` and `SHA256SUMS.sig` (Ed25519 by the CI key in AWS KMS, or by
Joe's offline key for a bridge release).

## 2. Goal

Within 15 minutes of a bot-agent release going live on GitHub, with nobody
touching the Deploybot host, the API offers it: hosts show "update
available", the bell says so, and `upgrade_agent` accepts it. The API offers
only a release whose signature verifies with a key the hosts trust, so it
never offers a version the hosts' helper would refuse.

## 3. Decisions for Joe (recommended marked ★)

- **D1, where the version comes from.**
  ★ A: the API polls GitHub's latest bot-agent release (at start, then every
  15 minutes) and verifies its signature.
  B: the release workflow calls a new API route after publishing.
  C: both.
  B needs a new authenticated inbound route and a credential for it in the
  `release` environment, and a missed call (API down, deploy in progress)
  needs polling anyway. A has no secret and no new route.
- **D2, what `BOT_API_LATEST_AGENT_VERSION` becomes.**
  ★ A: a floor. The latest version is the higher of the setting and the
  verified GitHub release. Production keeps working without a host change
  (its 0.2.17 is simply outgrown), and an offline or air-gapped server still
  works from the setting alone.
  B: a pin. When set, polling is off and the setting is the latest (today's
  behaviour); production would need someone to unset it once, as root.
  C: remove it.
- **D3, holding a release back.** ★ Follow GitHub's "latest" both ways: to
  hold back a bad release, Joe marks the previous release as latest on
  github.com (works from the phone), or publishes a fix; within 15 minutes the
  API offers that one (still at least the floor). Plus
  `BOT_API_AGENT_RELEASE_CHECK=off`, which turns polling off so only the
  setting counts. The alternative, "the latest only ever rises until a
  restart", gives no phone-friendly way back.
- **D4, keep it in memory.** ★ The checked version lives in the API process;
  a restart checks again at once. Until that first check succeeds (GitHub
  down at start) the API uses the setting, so `host_update` notices for the
  newer version may be redated. A stored copy would need a migration for a
  small gain.

The rest of this spec assumes the ★ options.

## 4. Changes

### 4.1 The release check (`api/internal/agentrelease`, new package)

`Check(ctx) (version string, err error)`, one pass, no state:

1. `GET https://api.github.com/repos/more-than-code/bot-agent/releases/latest`
   (documented API: never a draft or prerelease), headers
   `Accept: application/vnd.github+json` and `User-Agent: deploybot`, no
   token. Body read up to 1 MiB; only `tag_name` is used. It must match
   `^v([0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4})$` (the form the helper accepts,
   `agentversion.ValidRelease`); else `ErrBadRelease`.
2. Download `SHA256SUMS` (at most 4 KiB) and `SHA256SUMS.sig` (at most 128
   bytes, `MaxSigFile` in bot-agent) from
   `https://github.com/more-than-code/bot-agent/releases/download/v<V>/`, the
   address the helper uses (bot-agent `internal/upgrade.BaseURL`).
3. The signature must verify, as plain Ed25519 over the exact `SHA256SUMS`
   bytes, under one of the public keys compiled into the package (a copy of
   bot-agent `internal/release.PublicKeys`: Joe's offline key and the CI key,
   same order, same comment). An empty list or the all-zero key refuses
   everything.
4. `SHA256SUMS` must have a line for `bot-agent-v<V>-linux-amd64` (64 hex
   characters, two spaces, the name). This binds the signed file to the tag,
   so a signed `SHA256SUMS` of an older release uploaded under a newer tag is
   refused.

Bounds: HTTPS only; the client follows redirects only to `github.com`,
`api.github.com`, `objects.githubusercontent.com` and
`release-assets.githubusercontent.com` (the helper's list plus the API
host), at most 5; each request times out after 10 s. Errors are fixed values
(`ErrUnavailable`, `ErrBadRelease`, `ErrBadSignature`); no error or log line
carries a response body. Tests point the two base URLs at a fake.

### 4.2 The latest version in the API

- `Server.LatestAgentVersion string` is replaced by `Server.LatestAgent`, a
  small type holding the floor (the setting) and the checked version in an
  `atomic.Pointer`. `Latest()` returns the higher of the two that parse as
  `X.Y.Z`, or `""` when neither does. Every reader listed in §1 calls
  `Latest()` once per request or sweep, so one request never sees two values.
- `RunAgentReleaseCheck(ctx)` (started from `api/run.go` next to
  `RunAgentUpgrades`): one check at start, then every 15 minutes with up to
  60 s of jitter. A successful check stores its version, even if it is lower
  than the last one (D3). A failed check keeps the last good version. Logs:
  one Info line when the checked version changes (`agent release check:
  latest bot-agent is X.Y.Z`), one Warn line when checks start failing (the
  error value only) and one Info line when they recover; nothing per
  successful unchanged check.
- `BOT_API_AGENT_RELEASE_CHECK`: `on` (default, also when empty) or `off`.
  Any other value stops the API at start with
  `BOT_API_AGENT_RELEASE_CHECK must be on or off`. `off` starts no checker,
  so `Latest()` is the setting, as today.
- `BOT_API_LATEST_AGENT_VERSION` keeps its form check and becomes the floor.

Nothing else changes: the upgrade route still accepts only `Latest()`, the
notices still key on the version, the helper still verifies the release
itself.

### 4.3 Docs

`api/README.md` (the v1.34 and v1.48 setting paragraphs, the env table),
`api/docs/design.md` (one decision entry), `docs/contract-status.md` (one
sentence), `CHANGELOG.md` (one line). bot-agent `docs/spec-release-workflow.md`
gains one sentence in its key-rotation paragraph: a change to `PublicKeys`
needs the same change in bot-control-plane `api/internal/agentrelease`, or
the API stops offering releases signed only by the new key (the floor still
works).

## 5. Contract diff (api-v1 v1.62)

| | Before (v1.34 item 1, v1.48) | After (v1.62) |
|---|---|---|
| `BOT_API_LATEST_AGENT_VERSION` | the latest version; empty hides the hint | the floor; empty means no floor |
| `BOT_API_AGENT_RELEASE_CHECK` | absent | `on` (default) or `off`; other values stop the API at start |
| Host `latest_agent_version` | the setting, or `null` | the higher of the setting and the verified GitHub release, or `null` when neither is known |
| `update_available`, bell `host_update`, `agent-upgrade` version rule, `agent_upgrade` problem resolve | against the setting | against the value above |
| Routes, fields, migrations, MCP tools, agent protocol | — | unchanged |

Consumers: the console and MCP read `latest_agent_version` and
`update_available` as before; the hosts' helper is unchanged and still checks
every bundle itself.

## 6. Security

- The API never runs, installs or forwards anything it downloads; it reads one
  version string. The worst a forged GitHub answer can do is make the API
  offer a version, and the helper refuses any bundle not signed by a trusted
  key. Verifying on the server means a release the helper would refuse is not
  offered either (an unsigned draft, a wrong key, a replayed `SHA256SUMS`).
- Who can make the API offer a version: someone who can publish a release in
  `more-than-code/bot-agent` **and** sign it with a trusted key, the same bar
  as getting a host to install it. Upgrades still need an org owner or admin
  to ask (console, or MCP with `confirm`) and `agent_upgrade = true` in the
  host's `runner.toml`.
- No credential: unauthenticated calls, 4 an hour plus restarts, well inside
  GitHub's 60 an hour per IP.
- Downgrade by D3 only lowers what is offered; the helper never installs a
  version that is not newer than the installed one.

## 7. Alternatives considered

- **Workflow notifies the API (D1 B).** Faster (seconds), but adds an inbound
  route, a credential in GitHub, and still needs polling for missed calls.
- **The agent reports the latest release it sees.** Hosts would each call
  GitHub and the API would have to trust what agents say; no gain over §4.1.
- **Watch `github.com/.../releases/latest` redirects instead of the API.**
  Avoids the API rate limit but relies on web page behaviour; the documented
  API is enough at this rate.
- **Store the checked version (D4 alternative).** Survives a restart while
  GitHub is down, at the cost of a migration; not worth it now.

## 8. Rollback

Set `BOT_API_AGENT_RELEASE_CHECK=off` (and the setting to the wanted version)
and restart: exactly today's behaviour. A code revert needs nothing else (no
migration, no stored state).

## 9. Acceptance criteria and tests

1. With the fake GitHub serving a signed release `v0.2.19` (test key added to
   the package's key list in tests only) and the setting `0.2.17`, the host
   answers carry `latest_agent_version: "0.2.19"` after the first check, and
   `POST …/agent-upgrade {"version":"0.2.19"}` is accepted (memstore).
2. `Check` refuses, each with its error and without changing the stored
   version: a bad signature; a signature by an untrusted key; a `SHA256SUMS`
   without the `bot-agent-v<V>-linux-amd64` line (replay); a tag that is not
   `vX.Y.Z`; a body over the limit; a redirect to another host; a non-200; a
   timeout.
3. Floor: setting `0.2.20` with a verified `0.2.19` gives `0.2.20`; neither
   known gives `null` and the upgrade route's existing `bad_request`.
4. Downgrade (D3): a later check that verifies `0.2.18` replaces `0.2.19`.
5. A failed check after a good one keeps the good version; the Warn line is
   written once until a check succeeds.
6. `BOT_API_AGENT_RELEASE_CHECK=off` starts no checker; `maybe` stops the
   API at start with the fixed message.
7. The compiled key list equals bot-agent `internal/release.PublicKeys` (a
   test pins both base64 strings; the bot-agent change in §4.3 points at it).
8. No log line or error carries a response body (a test with a body holding
   a marker string checks the log output).
9. Existing tests that set `LatestAgentVersion` pass with the floor.
10. Gates: `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...`
    (no database code changes, so no database gate); `-race` on
    `api/internal/agentrelease` and `api/internal/api`.
