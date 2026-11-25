#!/usr/bin/env bash
# Build the release bundle for one tag into dist/ (or OUT_DIR), reproducibly
# (spec bot-agent docs/spec-release-workflow.md §4; built from PR #4):
#   deploybot-server-<tag>-linux-amd64   server binary (API and MCP)
#   deploybot-server-<tag>-linux-arm64   the same for arm64
#   bot-console-<tag>.tar.gz             console build (bot-console-<tag>/build, package.json)
#   RELEASE.md                           the tag's CHANGELOG.md section
#   SHA256SUMS                           sums of the files above
# Usage: scripts/release-bundle.sh vX.Y.Z [OUT_DIR]
# Refuses a tag that is not the newest CHANGELOG.md entry or the console
# package.json version, and a dirty work tree. Needs go, git, node 22+, pnpm,
# GNU tar and gzip on PATH.
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

if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
  echo "work tree has uncommitted changes" >&2
  exit 1
fi
export SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}"
export LC_ALL=C TZ=UTC

dist="${2:-$root/dist}"
rm -rf "$dist"
mkdir -p "$dist"

servers=()
for arch in amd64 arm64; do
  server="deploybot-server-$tag-linux-$arch"
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -trimpath -buildvcs=false -ldflags='-s -w' -o "$dist/$server" ./cmd/deploybot-server
  servers+=("$server")
done

(cd console && pnpm install --frozen-lockfile && pnpm build)
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/bot-console-$tag"
cp -R console/build "$stage/bot-console-$tag/build"
cp console/package.json "$stage/bot-console-$tag/package.json"
find "$stage" -exec touch -h -d "@$SOURCE_DATE_EPOCH" {} +
tar -C "$stage" --sort=name --format=gnu --mtime="@$SOURCE_DATE_EPOCH" \
  --owner=0 --group=0 --numeric-owner -cf - "bot-console-$tag" | gzip -9 -n > "$dist/bot-console-$tag.tar.gz"

# The tag's section: from its heading up to the next release heading.
awk -v head="## $tag — " '
  index($0, head) == 1 { on = 1; print; next }
  on && /^## v/ { exit }
  on { print }
' CHANGELOG.md > "$dist/RELEASE.md"

(cd "$dist" && sha256sum "${servers[@]}" "bot-console-$tag.tar.gz" RELEASE.md > SHA256SUMS)
ls -l "$dist"
