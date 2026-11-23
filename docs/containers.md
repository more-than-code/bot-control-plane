# Container images

api-v1 v1.50 items 5 and 9 (umbrella spec `docs/spec-deploybot-on-containers.md` §3.1). Two images, built from a release tag and deployed only by digest:

| Image | Dockerfile | Contents |
|-------|------------|----------|
| `ghcr.io/more-than-code/deploybot-server` | `build/docker/server.Dockerfile` | `deploybot-server` (static, `CGO_ENABLED=0 -trimpath -ldflags "-s -w"`, the release flags) on `gcr.io/distroless/static-debian12:nonroot`. Entrypoint `deploybot-server`, default command `api`; also `api --migrate`, `mcp`, `health <url>`. **No baked HEALTHCHECK**: each entry sets its own (below). |
| `ghcr.io/more-than-code/deploybot-console` | `build/docker/console.Dockerfile` | The SvelteKit `build` folder and `package.json` (the console has no runtime dependencies) on `node:24-bookworm-slim`, user `node`. `CMD ["env", "NODE_OPTIONS=--max-old-space-size=160", "node", "build"]`: the heap cap is in the image because `NODE_OPTIONS` is on the API's env denylist. `HOST=127.0.0.1`, `PORT=5180` by default (host networking binds loopback like today's unit). Healthcheck: a `node -e` fetch of `/signin` on `$PORT` that must answer 2xx within 3 s. |

Both base images are pinned by the digest of their multi-arch index, in an `ARG` at the top of each Dockerfile (the server's Go build stage too). pnpm comes through corepack, pinned by version and the npm registry's sha512 (`PNPM_SPEC`). Change a pin on purpose, in its own commit, and note it in the release.

## Catalog entries (host networking)

Each entry sets its own Docker healthcheck on its own port, so another process listening on a default port cannot make a broken container look healthy. Examples, with the containers on ports that differ from the systemd units during a cutover:

- **api** (`kind` service, `network` `"host"`, `command` `["api"]`, `ports` `[{"host":8190,"container":8190}]`, `BOT_API_LISTEN=127.0.0.1:8190` in its host env file):
  `"healthcheck": {"test": ["/usr/local/bin/deploybot-server", "health", "http://127.0.0.1:8190/health"]}`
- **mcp** (`command` `["mcp"]`, port 8191, `BOT_MCP_LISTEN=127.0.0.1:8191`):
  `"healthcheck": {"test": ["/usr/local/bin/deploybot-server", "health", "http://127.0.0.1:8191/health"]}`
- **migrate** (`kind` job, `command` `["api", "--migrate"]`): no healthcheck.
- **console**: leave `command` empty (the image's `CMD` keeps the heap cap) and set `PORT=5280` in its host env file; the image's healthcheck follows `PORT`. That check starts a `node` process every 30 s (about 40 MB for a moment, inside the container's memory limit); leave headroom for it in `memory_mb`, or replace it with an entry healthcheck if the budget is tight.

`ports[].host` in host mode is only the loopback port the agent's own health wait probes (agent-protocol v1.21 item 3).

## Secrets

Configuration comes from the environment at run time: the same variables as the units. The secrets (`BOT_SECRETS_KEY`, `BOT_API_MCP_CLIENT_SECRET`, `BOT_API_GITHUB_CLIENT_SECRET`, `BOT_MCP_CLIENT_SECRET`) can be env values or `*_FILE` paths to mounted 0600 files, never both (`api/README.md`, Deploybot on containers).

- Env values are visible to root on the host (`docker inspect`, `/proc/<pid>/environ`). Put them only in the host-owned env file (`runner.toml` `host_env_files`, root 0600, agent-protocol v1.21 item 4).
- Never put them in a catalog entry's `env` or in an env set: that would send them through Deploybot itself.
- Nothing secret is built into an image; `.dockerignore` keeps `.env`, `*.env`, `*.pem`, `*.key`, secret-named files (source files excepted) and task notes out of the build context.

## Build

From the repository root at the release tag, with a clean tree. The build context is the repository root.

```sh
git checkout vX.Y.Z
export SOURCE_DATE_EPOCH="$(git log -1 --format=%ct)"

# Podman (used on Joe's Mac). --format docker keeps the console's
# HEALTHCHECK, which the OCI image format drops.
podman build --format docker --platform linux/amd64 \
  --source-date-epoch "$SOURCE_DATE_EPOCH" --rewrite-timestamp \
  -f build/docker/server.Dockerfile -t deploybot-server:vX.Y.Z .
podman build --format docker --platform linux/amd64 \
  --source-date-epoch "$SOURCE_DATE_EPOCH" --rewrite-timestamp \
  -f build/docker/console.Dockerfile -t deploybot-console:vX.Y.Z .

# Docker with BuildKit (buildx 0.13 or later)
docker buildx build --platform linux/amd64 \
  --build-arg SOURCE_DATE_EPOCH \
  --output type=docker,rewrite-timestamp=true \
  -f build/docker/server.Dockerfile -t deploybot-server:vX.Y.Z .
```

`SOURCE_DATE_EPOCH` (the tag's commit time) sets the image creation time and, with the timestamp rewrite, the layer file times; in the console build it also fixes SvelteKit's version name (`console/svelte.config.js`). So the same tag, base digests and builder give the same image id. Check it the way the runbook checks the binaries: build twice (`--no-cache`) and compare `podman image inspect --format '{{.Id}}'`. The server binary inside is byte-for-byte the release binary for the same platform. Both build stages run on the builder's platform (the Go binary is cross-compiled; the console output is plain JS).

Targets: `linux/amd64` for us1; `linux/arm64` builds the same way for the Pis.

## Publish and deploy

Pushing is an ops step on Joe's yes (release runbook), never part of a build. Push by tag, then record the pushed digest (`podman push --digestfile`) in the release notes; catalog entries reference `ghcr.io/more-than-code/deploybot-server@sha256:…`, never a tag. The images are public, so hosts need no registry login.

## Smoke checks

```sh
podman run --rm deploybot-server:vX.Y.Z --help                    # usage, exit 0
podman run --rm deploybot-server:vX.Y.Z health http://127.0.0.1:1/ # exit 1 (nothing listening)
podman run --rm deploybot-server:vX.Y.Z health http://example.com/ # exit 2 (not loopback)
podman image inspect --format '{{json .HealthCheck}} {{.User}}' deploybot-console:vX.Y.Z
```
