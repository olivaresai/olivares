#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Regenerate every published console capture from a RELEASED Community image, in one command.
# Usage: scripts/docs-captures-release.sh <release tag> <image digest>
#        scripts/docs-captures-release.sh 0.1 sha256:fb9ec7475cfcf228919bcd53480bf0e2cb6413f70d05d7cce1ec0bbc6c2bb851
# The digest comes from the release record; the run refuses a tag that resolves to a different image.
# Needs curl, jq and tar. The binary comes out of the image's layers over the registry HTTP API, so
# no container runtime is required. The run is then scripts/docs-captures.sh with OLIVARES_BIN set and
# PUBLICAR=1: it boots the released binary on loopback, seeds the demo estate, drives Chromium through
# every view in both themes, copies the PNGs and manifest.json to docs-site/public/console, and records
# the image reference in the manifest. It finishes by checking that every capture a docs page references
# exists, and lists the ones this run did not regenerate.
# The estate it photographs is the demo dataset (scripts/seed-demo-dataset.py): four AI tools that answer
# recorded conversations through stand-ins (scripts/demo-tools), so no vendor account and no network are involved.
# ponytail: the released binary runs with your environment, in your worktree. The image is ours and digest-pinned;
# run this from a disposable worktree and a user without credentials. Upgrade trigger: before it runs any image
# the Olivares organization did not publish, put every binary call behind `env -i` and a sandbox.
# A capture whose visible text shows a path of this host fails the run; pass a TMPDIR outside /workspace and /home
# when a view needs one. The run boots an engine and a browser: on a shared host start it under that host's job
# wrapper so that two heavy runs do not overlap.
# DOCS_CAPTURE_ALLOW_PAID_CAPABILITIES=1 photographs the other views of a Community binary that serves the paid
# access graph or identity inventory (the harness refuses such a binary otherwise).
# scripts/docs-captures-held.txt lists the views every run leaves out (id, the open issue that owns the retake, why), so
# their published captures stay as they are: run as written above, the command never overwrites a held image.
# DOCS_CAPTURE_SKIP='id|id' leaves further views out of one run.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TAG="${1:-}"
EXPECT_DIGEST="${2:-}"
REPO=olivaresai/olivares
REGISTRY=https://registry-1.docker.io/v2
if ! [[ "$TAG" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] || ! [[ "$EXPECT_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "usage: $0 <release tag, for example 0.1> <image digest, sha256:...>" >&2
  exit 2
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
HDR="$WORK/auth.hdr"
# HTTP/1.1: over HTTP/2 the registry's CDN refuses the connection for some layers (curl 35 before the first byte).
CURL=(curl -q -fsSL --http1.1 --proto '=https' --proto-redir '=https' --tlsv1.2 --max-redirs 3 --max-time 900
  --max-filesize 700000000 --retry 8 --retry-delay 3 --retry-all-errors)

# An anonymous pull token, valid for about five minutes, so each request gets a fresh one. It travels in a
# header file, never in argv.
auth() {
  local token
  { set +x; } 2>/dev/null
  token="$("${CURL[@]}" "https://auth.docker.io/token?service=registry.docker.io&scope=repository:$REPO:pull" | jq -er .token)" || return 1
  (umask 077 && printf 'Authorization: Bearer %s\n' "$token" >"$HDR")
}
reg() { auth || return 1; "${CURL[@]}" -H "@$HDR" "$@"; }
# The CDN also cuts some layers mid-transfer, and one curl invocation does not get past the cut. Resume with a fresh
# token until the file has the size the manifest declares. A failed token request, an HTTP error or a size limit stops
# the run at once with the real cause; only a cut connection is retried (at most 40 times, one line per cut). The digest
# check that follows decides whether the bytes are right.
fetch_layer() {
  local digest="$1" want="$2" out="$3" try=0 rc
  [[ "$want" =~ ^[1-9][0-9]*$ ]] || { echo "layer $digest: the manifest declares no usable size: '$want'" >&2; return 1; }
  : >"$out"
  while [ "$(stat -c %s "$out")" -lt "$want" ]; do
    try=$((try + 1))
    [ "$try" -le 40 ] || { echo "layer $digest stopped at $(stat -c %s "$out") of $want bytes after 40 tries" >&2; return 1; }
    auth || { echo "layer $digest: the registry token request failed" >&2; return 1; }
    rc=0
    "${CURL[@]}" -H "@$HDR" -C - -o "$out" "$REGISTRY/$REPO/blobs/$digest" 2>"$WORK/curl.err" || rc=$?
    case "$rc" in
      0) ;;
      22 | 63) echo "layer $digest: curl exit $rc, not a cut connection: $(tail -n 1 "$WORK/curl.err")" >&2; return 1 ;;
      *) echo "layer ${digest:7:12}: connection cut at $(stat -c %s "$out") of $want bytes (curl $rc, try $try), resuming" >&2 ;;
    esac
  done
}

sha() { sha256sum "$1" | cut -d' ' -f1; }

ACCEPT_INDEX='Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json'
ACCEPT_MANIFEST='Accept: application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json'

reg -H "$ACCEPT_INDEX" -D "$WORK/index.hdr" -o "$WORK/index.json" "$REGISTRY/$REPO/manifests/$TAG"
IMAGE_DIGEST="$(tr -d '\r' <"$WORK/index.hdr" | awk -F': ' 'tolower($1) == "docker-content-digest" { print $2 }')"
[[ "$IMAGE_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "no image digest for $REPO:$TAG" >&2; exit 1; }
[ "sha256:$(sha "$WORK/index.json")" = "$IMAGE_DIGEST" ] || { echo "the image index does not hash to $IMAGE_DIGEST" >&2; exit 1; }
[ "$IMAGE_DIGEST" = "$EXPECT_DIGEST" ] || { echo "$REPO:$TAG is $IMAGE_DIGEST, expected $EXPECT_DIGEST" >&2; exit 1; }
echo "==> $REPO:$TAG = $IMAGE_DIGEST"

AMD64="$(jq -er '.manifests[] | select(.platform.os == "linux" and .platform.architecture == "amd64") | .digest' "$WORK/index.json")"
[[ "$AMD64" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "no linux/amd64 manifest in $IMAGE_DIGEST" >&2; exit 1; }
reg -H "$ACCEPT_MANIFEST" -o "$WORK/manifest.json" "$REGISTRY/$REPO/manifests/$AMD64"
[ "sha256:$(sha "$WORK/manifest.json")" = "$AMD64" ] || { echo "the amd64 manifest does not hash to $AMD64" >&2; exit 1; }

# The binary is one regular file in one layer. Scan from the last layer; each blob is checked against its digest,
# an unreadable layer stops the run, and a layer that deletes the binary (an OCI whiteout) ends the scan.
BIN=""
for layer in $(jq -r '.layers | reverse | .[].digest' "$WORK/manifest.json"); do
  [[ "$layer" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "bad layer digest in the manifest: $layer" >&2; exit 1; }
  blob="$WORK/layer.tgz"
  size="$(jq -er --arg d "$layer" '.layers[] | select(.digest == $d) | .size' "$WORK/manifest.json")"
  fetch_layer "$layer" "$size" "$blob"
  [ "sha256:$(sha "$blob")" = "$layer" ] || { echo "layer $layer failed its digest" >&2; exit 1; }
  tar -tzf "$blob" >"$WORK/layer.list" || { echo "layer $layer is not a readable gzip tar" >&2; exit 1; }
  if grep -qxF 'usr/local/bin/.wh.olivares' "$WORK/layer.list"; then
    echo "layer $layer deletes usr/local/bin/olivares" >&2
    exit 1
  fi
  if grep -qxF 'usr/local/bin/olivares' "$WORK/layer.list"; then
    member="$(tar -tvzf "$blob" usr/local/bin/olivares | head -n 1)"
    [[ "$member" == -* ]] || { echo "usr/local/bin/olivares in layer $layer is not a regular file: $member" >&2; exit 1; }
    mkdir -p "$WORK/bin"
    tar -xzf "$blob" -C "$WORK/bin" --no-same-owner --no-same-permissions usr/local/bin/olivares
    BIN="$WORK/bin/usr/local/bin/olivares"
    chmod 0700 "$BIN"
    break
  fi
  rm -f "$blob"
done
[ -f "$BIN" ] && [ ! -L "$BIN" ] || { echo "no usr/local/bin/olivares in the $TAG amd64 layers" >&2; exit 1; }
VERSION="$(cd "$WORK" && env -i PATH=/usr/bin:/bin HOME="$WORK" "$BIN" version)"
[[ "$VERSION" == "olivares $TAG "* ]] || { echo "the extracted binary is not release $TAG: $VERSION" >&2; exit 1; }
echo "==> $VERSION"

OLIVARES_BIN="$BIN" DOCS_CAPTURE_IMAGE="$REPO:$TAG@$IMAGE_DIGEST" PUBLICAR=1 bash "$ROOT/scripts/docs-captures.sh"
# The engine that served the captures must be the binary taken out of the image.
[ "$(jq -r .binary_sha256 "$ROOT/docs-site/public/console/manifest.json")" = "$(sha "$BIN")" ] ||
  { echo "the engine that served the captures is not the binary extracted from $REPO:$TAG" >&2; exit 1; }

# Every capture a current docs page names must now exist. The 2026-06 snapshot is frozen on purpose.
refs="$(grep -rhoE --include='*.md' --include='*.mdx' --exclude-dir=2026-06 '/console/[A-Za-z0-9._-]+\.png' \
  "$ROOT/docs-site/src/content/docs" | sed 's#^/console/##' | sort -u || true)"
[ -n "$refs" ] || { echo "no capture reference found in the docs pages" >&2; exit 1; }
missing=0
for png in $refs; do
  [ -f "$ROOT/docs-site/public/console/$png" ] || { echo "docs reference a missing capture: /console/$png" >&2; missing=1; }
done
# A file that exists may come from an earlier run. Say which captures this run did not take: the referenced ones
# first, then any other published capture the manifest does not list.
taken="$(jq -r '.take[].id' "$ROOT/docs-site/public/console/manifest.json" | sort -u)"
stale="$(comm -23 <(sed -E 's/-(light|dark)\.png$//' <<<"$refs" | sort -u) <(sed -n p <<<"$taken"))"
if [ -n "$stale" ]; then
  echo "Referenced by a docs page and NOT regenerated by this run (the published files are from an earlier run):" >&2
  sed 's/^/  /' <<<"$stale" >&2
fi
others="$(comm -23 <(cd "$ROOT/docs-site/public/console" && for f in *.png; do echo "${f%-light.png}" | sed -E 's/-dark\.png$//'; done | sort -u) <(sed -n p <<<"$taken") | grep -vxFf <(sed -E 's/-(light|dark)\.png$//' <<<"$refs" | sort -u) || true)"
if [ -n "$others" ]; then
  echo "Published and NOT regenerated by this run, referenced by no page:" >&2
  sed 's/^/  /' <<<"$others" >&2
fi
held="$(grep -vE '^[[:space:]]*(#|$)' "$ROOT/scripts/docs-captures-held.txt" || true)"
if [ -n "$held" ]; then
  echo "Held, left out of this run (scripts/docs-captures-held.txt; id, owning issue, why):" >&2
  sed 's/^/  /' <<<"$held" >&2
fi
exit "$missing"
