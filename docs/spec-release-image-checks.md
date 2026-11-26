# The release workflow checks its images the way the Mac build did

Status: approved by Joe 2026-11-26 ("Yes, open the workflow PR" on the
decision card "Add the four missing image checks to the release workflow?").
Tier 3 (a change to `.github/workflows/`). No api-v1 or agent-protocol change,
no migration, no bot-agent change.

## 1. Problem

Server releases are built, compared and pushed by `.github/workflows/release.yml`
(v0.2.50 was the first; production runs its digests). The Ops path on Joe's Mac
(Ops handoff 2026-11-26, §3 step 2) still built the images a second time with
podman to check five things. The workflow covers two of them (two builds equal;
digests posted) and not the rest:

| Ops check | Before this change |
|---|---|
| Two builds, equal image ids | covered (both platforms, `--no-cache`, `index.json` compared) |
| Server binary `cmp`-equal to the release binary | not checked: bundle and image are each compared with themselves, never with each other |
| `--help` exits 0 | not run |
| Runs as non-root | not asserted (only the Dockerfiles' `USER`) |
| Digests sent back | posted, but not asserted to be the digest that was compared |

Also not asserted: the console image keeps its `HEALTHCHECK`. us1's console entry
has no healthcheck of its own and relies on the image's (podman's OCI format
drops it; BuildKit keeps it, checked locally on BuildKit 0.35).

Goal (computerless plan): Ops no longer builds or pushes server images.

## 2. Change

1. `scripts/check-images.sh vX.Y.Z IMAGES_DIR DIST_DIR` (new). Reads
   `server.tar` and `console.tar`, multi-platform OCI archives as buildx writes
   them, with `tar` and `jq`. For linux/amd64 and linux/arm64 of each image it
   fails when:
   - there is not exactly one manifest for the platform;
   - the config's `User` is empty, `root`, `0`, `root:*` or `0:*`;
   - console: the config has no `Healthcheck` with a `CMD` test;
   - server: no layer holds `usr/local/bin/deploybot-server`, or the top-most one
     differs from `DIST_DIR/deploybot-server-vX.Y.Z-linux-<arch>`.
   It prints one `ok:` or `FAIL:` line per check and exits 1 on any failure,
   2 on bad usage.
2. Job `build`, new step "Check images" after "Images twice and compare": runs
   the script on `images/` and `dist/`, then loads the amd64 server image into
   Docker (`skopeo copy --override-arch amd64 … docker-daemon:…`) and runs the
   docs/containers.md smoke checks with `--network none`: `--help` exits 0,
   `health http://127.0.0.1:1/` exits 1, `health http://example.com/` exits 2.
   arm64 is covered by the binary comparison only (running it needs QEMU; the
   Mac build never ran arm64 either).
3. Job `publish`, after each `skopeo copy`: the digest file must equal
   `.manifests[0].digest` of the archive's `index.json`, or the job fails before
   the release is published.

A failing check stops the run before anything is tagged or pushed (items 1 and
2), or before the release is published (item 3; the image tag may then already
exist on ghcr, as for any publish failure today, and a rerun overwrites it).

## 3. Not changed

Build commands, Dockerfiles, base images, the bundle, the tag and release steps,
and who approves (the merge into `main`, spec D3 of bot-agent
`docs/spec-release-workflow.md`). The manual podman build in docs/containers.md
stays as the fallback for a broken CI run, on Joe's yes.

## 4. Tests

- `scripts/check_images_test.go` builds OCI archives in Go (nested and flat
  `index.json`) and runs the script: a good pair passes with 8 `ok:` lines; root
  user, empty user, `0:0`, missing HEALTHCHECK, a different binary, a missing
  binary and a missing platform each fail with their message; bad usage exits 2.
  Needs bash, GNU tar, gzip and jq; it runs in `go test ./...`.
- Manual, in the authoring session: the script on a real BuildKit 0.35
  multi-platform OCI archive with the v0.2.50 server binaries (pass, and fail
  after changing one byte of the arm64 bundle binary); the three smoke checks on
  that image; `skopeo copy --all --preserve-digests --digestfile` from that
  archive writes the `index.json` digest.
- The workflow itself runs only on a version commit in `main`; the next server
  release is its first run.

## 5. Rollback

Revert the PR. Nothing outside the workflow and the new script depends on it.

## 6. Release path after this change

1. Cloud: version commit, release PRs into `dev` and `main` on Joe's ask.
2. CI on the merge into `main`: release gates, bundle twice, images twice, the
   checks above, tag, push by tag with preserved digests, GitHub release with
   the digests.
3. Cloud: read the digests from the release, deploy by digest via the production
   MCP on Joe's yes (migrate, then api, mcp, console), outside the backup window.
4. Ops only for host steps: env files, way-back dumps, migration down scripts,
   the read-only migration check.
