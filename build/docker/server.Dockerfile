# Deploybot server image: ghcr.io/more-than-code/deploybot-server
# (api-v1 v1.50 item 5, spec docs/spec-deploybot-on-containers.md section 3.1).
# One static binary on distroless nonroot. Modes: api, api --migrate, mcp,
# health <url>. Build from the repository root; see docs/containers.md.
#
# Base images are pinned by digest (multi-arch index). Refresh both lines
# together, on purpose, and record the change in the release notes.
ARG GO_IMAGE=docker.io/library/golang:1.27.1-bookworm@sha256:8d48e12ec56735e9358640898b9d9b9fcca110612ed8a5567438c0a1baa24e66
ARG BASE_IMAGE=gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

# The build stage runs on the builder's platform and cross-compiles.
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS=linux
ARG TARGETARCH
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=readonly
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The same flags as the release binaries (tasks/ops/release-runbook.md): the
# output depends only on the source and the toolchain.
RUN GOOS="${TARGETOS}" GOARCH="${TARGETARCH:-$(go env GOARCH)}" \
    go build -trimpath -buildvcs=false -ldflags "-s -w" -o /out/deploybot-server ./cmd/deploybot-server

FROM ${BASE_IMAGE}
COPY --from=build /out/deploybot-server /usr/local/bin/deploybot-server
USER nonroot:nonroot
# No baked HEALTHCHECK: each catalog entry (api, mcp) sets its own Docker
# healthcheck for its own port, so another process on a default port cannot
# mask a broken container (docs/containers.md).
ENTRYPOINT ["/usr/local/bin/deploybot-server"]
CMD ["api"]
