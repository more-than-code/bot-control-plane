# Deploybot console image: ghcr.io/more-than-code/deploybot-console
# (api-v1 v1.50 item 5, spec docs/spec-deploybot-on-containers.md section 3.1).
# The SvelteKit node build and package.json on node 24 slim. Build from the
# repository root; see docs/containers.md.
#
# The base image is pinned by digest (multi-arch index); both stages use it.
ARG NODE_IMAGE=docker.io/library/node:24-bookworm-slim@sha256:dc26ae92c9fb15cf6b1d63e20f0944b2efe0f115b20e00be48fb23fceb061c54

# The build stage runs on the builder's platform; its output is plain JS.
FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS build
# pnpm through corepack, pinned by version and the npm registry's sha512.
ARG PNPM_SPEC=pnpm@12.5.1+sha512.e3f305bc784a2bc89f5ad3b6138889470fae8d2af5f36b61216ec91c2c3d64089775f9de38aac331044ea40f245cb0d5666392dfdf65824e1907ef6a2c62de5f
# Set by `--source-date-epoch` (podman) or `--build-arg SOURCE_DATE_EPOCH`
# (docker); it fixes SvelteKit's version name (svelte.config.js).
ARG SOURCE_DATE_EPOCH
ENV CI=true
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0
RUN corepack enable pnpm && corepack install --global "${PNPM_SPEC}"
# The repository layout is kept: the console reads ../CHANGELOG.md at build time.
WORKDIR /src/console
COPY console/package.json console/pnpm-lock.yaml console/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY CHANGELOG.md /src/CHANGELOG.md
COPY console/ ./
RUN pnpm run build

FROM ${NODE_IMAGE}
ENV NODE_ENV=production HOST=127.0.0.1 PORT=5180
WORKDIR /app
COPY --from=build /src/console/package.json ./package.json
COPY --from=build /src/console/build ./build
USER node
# GET /signin answers 200 when BOT_API_URL is valid, 503 when not.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["node", "-e", "fetch('http://127.0.0.1:'+(process.env.PORT||'5180')+'/signin',{redirect:'manual',signal:AbortSignal.timeout(3000)}).then(r=>process.exit(r.status>=200&&r.status<300?0:1),()=>process.exit(1))"]
# The heap cap lives here, not in the entry's env (NODE_OPTIONS is on the
# API's env denylist).
CMD ["env", "NODE_OPTIONS=--max-old-space-size=160", "node", "build"]
