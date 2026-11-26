#!/usr/bin/env bash
# Check the release images before they are pushed (docs/spec-release-image-checks.md):
# the checks the manual image build made on Joe's Mac, now on the CI archives.
#   - each image's config sets a user that is not root, for every platform;
#   - the console image keeps its HEALTHCHECK;
#   - the server binary in each platform's image is byte for byte the bundle
#     binary deploybot-server-<tag>-linux-<arch> in DIST_DIR.
# IMAGES_DIR holds server.tar and console.tar, multi-platform OCI archives as
# `docker buildx build --output type=oci` writes them. Running the server image
# (--help and the health smoke checks) is a workflow step, since it needs Docker.
# Usage: scripts/check-images.sh vX.Y.Z IMAGES_DIR DIST_DIR
# Needs bash, GNU tar, gzip and jq.
set -euo pipefail

tag="${1:-}"
images="${2:-}"
dist="${3:-}"
if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || [[ -z "$images" || -z "$dist" ]]; then
  echo "usage: $0 vX.Y.Z IMAGES_DIR DIST_DIR" >&2
  exit 2
fi
arches=(amd64 arm64)
server_path=usr/local/bin/deploybot-server
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
failed=0
fail() { echo "FAIL: $*" >&2; failed=1; }

# blob ARCHIVE DIGEST: one blob of an OCI archive, to stdout.
blob() {
  [[ "$2" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "bad digest: $2" >&2; return 1; }
  tar -xOf "$1" "blobs/sha256/${2#sha256:}"
}

# index ARCHIVE: the image index of the archive (index.json may point to it).
index() {
  local top first
  top="$(tar -xOf "$1" index.json)"
  first="$(jq -r '.manifests[0].mediaType' <<<"$top")"
  if [[ "$(jq '.manifests | length' <<<"$top")" == 1 && "$first" == application/vnd.oci.image.index.v1+json ]]; then
    blob "$1" "$(jq -r '.manifests[0].digest' <<<"$top")"
  else
    printf '%s\n' "$top"
  fi
}

# layer_file ARCHIVE MANIFEST PATH OUT: extract PATH from the top-most layer
# that has it. Returns 1 when no layer has it.
layer_file() {
  local digest mt dec
  while read -r digest mt; do
    case "$mt" in
      *+gzip|application/vnd.docker.image.rootfs.diff.tar.gzip) dec=(gzip -dc) ;;
      *.tar|application/vnd.oci.image.layer.v1.tar) dec=(cat) ;;
      *) echo "unknown layer type $mt" >&2; return 1 ;;
    esac
    if blob "$1" "$digest" | "${dec[@]}" | tar -xOf - "$3" >"$4" 2>/dev/null; then
      return 0
    fi
  done < <(jq -r '.layers | reverse | .[] | "\(.digest) \(.mediaType)"' <<<"$2")
  return 1
}

for img in server console; do
  ar="$images/$img.tar"
  [[ -f "$ar" ]] || { fail "$ar is missing"; continue; }
  idx="$(index "$ar")"
  for arch in "${arches[@]}"; do
    what="$img linux/$arch"
    mdigest="$(jq -r --arg a "$arch" '[.manifests[] | select(.platform.os == "linux" and .platform.architecture == $a)] | if length == 1 then .[0].digest else "" end' <<<"$idx")"
    [[ -n "$mdigest" ]] || { fail "$what: not exactly one manifest"; continue; }
    manifest="$(blob "$ar" "$mdigest")"
    cfg="$(blob "$ar" "$(jq -r '.config.digest' <<<"$manifest")")"

    user="$(jq -r '.config.User // ""' <<<"$cfg")"
    case "$user" in
      ""|root|0|root:*|0:*) fail "$what: runs as root (User \"$user\")" ;;
      *) echo "ok: $what runs as $user" ;;
    esac

    if [[ "$img" == console ]]; then
      if [[ "$(jq -r '.config.Healthcheck.Test[0] // ""' <<<"$cfg")" == CMD ]]; then
        echo "ok: $what has its HEALTHCHECK"
      else
        fail "$what: HEALTHCHECK is missing"
      fi
    fi

    if [[ "$img" == server ]]; then
      want="$dist/deploybot-server-$tag-linux-$arch"
      got="$work/server-$arch"
      if [[ ! -f "$want" ]]; then
        fail "$want is missing"
      elif ! layer_file "$ar" "$manifest" "$server_path" "$got"; then
        fail "$what: no layer has /$server_path"
      elif cmp -s "$got" "$want"; then
        echo "ok: $what binary is $(basename "$want")"
      else
        fail "$what: binary differs from $(basename "$want")"
      fi
    fi
  done
done

exit "$failed"
