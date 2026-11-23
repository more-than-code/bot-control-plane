#!/usr/bin/env bash
# Build the release bundle for one tag into dist/:
#   deploybot-server-<tag>-linux-amd64   server binary (API and MCP)
#   bot-console-<tag>.tar.gz             console build (bot-console-<tag>/build, package.json)
#   RELEASE.md                           the tag's CHANGELOG.md section
#   SHA256SUMS                           sums of the three files above
# Usage: scripts/release-bundle.sh vX.Y.Z
# Refuses a tag that is not the newest CHANGELOG.md entry or the console
# package.json version. Needs go, node 22+ and pnpm on PATH.
set -euo pipefail

tag="${1:-}"
if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "usage: $0 vX.Y.Z" >&2
  exit 2
fi
version="${tag#v}"
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

newest="$(sed -n 's/^## v\([0-9][0-9.]*\) — .*/\1/p' CHANGELOG.md | head -n1)"
if [[ "$newest" != "$version" ]]; then
  echo "tag $tag is not the newest CHANGELOG.md entry (v$newest)" >&2
  exit 1
fi
pkg="$(node -p 'require("./console/package.json").version')"
if [[ "$pkg" != "$version" ]]; then
  echo "tag $tag does not match console/package.json version $pkg" >&2
  exit 1
fi

dist="$root/dist"
rm -rf "$dist"
mkdir -p "$dist"

server="deploybot-server-$tag-linux-amd64"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags='-s -w' -o "$dist/$server" ./cmd/deploybot-server

(cd console && pnpm install --frozen-lockfile && pnpm build)
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/bot-console-$tag"
cp -R console/build "$stage/bot-console-$tag/build"
cp console/package.json "$stage/bot-console-$tag/package.json"
tar -C "$stage" -czf "$dist/bot-console-$tag.tar.gz" "bot-console-$tag"

# The tag's section: from its heading up to the next release heading.
awk -v head="## $tag — " '
  index($0, head) == 1 { on = 1; print; next }
  on && /^## v/ { exit }
  on { print }
' CHANGELOG.md > "$dist/RELEASE.md"

(cd "$dist" && sha256sum "$server" "bot-console-$tag.tar.gz" RELEASE.md > SHA256SUMS)
ls -l "$dist"
