#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# export-update-bundle.sh — produce a self-contained, signed AIR-GAP update bundle
# for `olivares upgrade --bundle`. The bundle is a tarball of the signed
# per-channel manifest, its Ed25519 signature, and the platform archives it points
# at — verifiable 100% OFFLINE against the embedded (or --pubkey) OTA key, with
# NO network, cosign, or Rekor. It is the DDIL/air-gap sibling of verify-release.sh
# and reuses the SAME manifest producer as the online pipeline (`olivares release
# manifest`), so an air-gapped install verifies byte-identically to an online one.
#
# Usage:
#   export-update-bundle.sh --dir <release-dir> --channel <c> --version <v> \
#       --sign-key <dedicated-ota-priv-b64-file> --out <bundle.tar.gz> \
#       [--set <slug>] [--list-files] [--olivares <bin>] [--min-version <v>] \
#       [--advisory <id>]... [--security] [--rollout <n>] [--expires-in <dur>] \
#       [--no-crosscheck]
#
# The <release-dir> holds the goreleaser archives (olivares_<v>_<os>_<arch>.tar.gz).
# C02-17: enterprise air-gap is ONE bundle PER SET. Pass --set <slug> from
# ALLOWED_SET_SLUGS; archives are taken from <dir>/<slug>/ or
# <dir>/enterprise/<version>/<slug>/, never from a mixed tree. Omitting --set
# on a tree that already has set prefixes is a finding (would pack the wrong
# bytes, or mix two SKUs under the same basename). A FLAT community dir
# without set prefixes stays valid without --set — that path is AGPL core.
# Verify a produced bundle offline with:
#   olivares upgrade --bundle <bundle.tar.gz> --pubkey <ota.pub> --check
#
# ANTI-FREEZE: the manifest producer carries a freshness bound BY DEFAULT (2160h).
# This script used to forward --expires-in only when the caller passed it, so the
# common invocation shipped a signed air-gap bundle with `expires: null` — one a
# mirror (or a stale USB stick doing the rounds of a DDIL site) can replay forever.
# Pass --expires-in to choose the window; you cannot accidentally omit it.
#
# CROSS-CHECK: after generating, the manifest is bound to the release dir's
# checksums.txt and its policy is bounds-checked, the same gate the online ceremony
# runs. Use --no-crosscheck ONLY for a hand-assembled dir with no checksums.txt.
set -euo pipefail

DIR="" CHANNEL="stable" VERSION="" SIGN_KEY="" OUT="" OLIVARES="" MINVER=""
SET="" LIST_FILES=0
SECURITY=0 ROLLOUT="" EXPIRES_IN="" CROSSCHECK=1 ; ADVISORIES=()

die() { echo "error: $*" >&2; exit 1; }
cannot() { echo "export-update-bundle: COULD NOT LOOK: $*" >&2; exit 2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --dir) DIR="$2"; shift 2;;
    --channel) CHANNEL="$2"; shift 2;;
    --version) VERSION="$2"; shift 2;;
    --sign-key) SIGN_KEY="$2"; shift 2;;
    --out) OUT="$2"; shift 2;;
    --set) SET="$2"; shift 2;;
    --list-files) LIST_FILES=1; shift;;
    --olivares) OLIVARES="$2"; shift 2;;
    --min-version) MINVER="$2"; shift 2;;
    --advisory) ADVISORIES+=("$2"); shift 2;;
    --security) SECURITY=1; shift;;
    --rollout) ROLLOUT="$2"; shift 2;;
    --expires-in) EXPIRES_IN="$2"; shift 2;;
    --no-crosscheck) CROSSCHECK=0; shift;;
    -h|--help) grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0;;
    *) die "unknown flag: $1";;
  esac
done

[ -n "$DIR" ] || die "--dir is required"
[ -n "$VERSION" ] || die "--version is required"
[ -d "$DIR" ] || die "release dir $DIR does not exist"
# Git tags carry the leading "v"; GoReleaser archive names and manifest.version do not.
VERSION="${VERSION#v}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SETS_TS="$ROOT/commercial/license-worker/src/download/sets.ts"
load_slugs() {
  [ -f "$SETS_TS" ] || return 1
  sed -n '/^export const ALLOWED_SET_SLUGS/,/^]);/p' "$SETS_TS" |
    grep -oE '^[[:space:]]+"[a-z+]+",?$' | grep -oE '"[a-z+]+"' | tr -d '"'
}

SLUGS="$(load_slugs || true)"
if [ -n "$SET" ]; then
  [ -n "$SLUGS" ] || cannot "cannot read ALLOWED_SET_SLUGS from $SETS_TS; refusing --set without the allowlist"
  _ok=0
  for _s in $SLUGS; do [ "$_s" = "$SET" ] && _ok=1; done
  [ "$_ok" = 1 ] || die "--set '$SET' is not in ALLOWED_SET_SLUGS (sets.ts). A slug the download gate does not serve must not be bundled."
  unset _ok _s
fi

has_set_layout() {
  [ -n "$SLUGS" ] || return 1
  local slug
  for slug in $SLUGS; do
    # nullglob-free: a literal glob means no match
    for f in "$DIR/$slug"/olivares_*.tar.gz; do
      [ -f "$f" ] && return 0
    done
    for f in "$DIR/enterprise/"*/"$slug"/olivares_*.tar.gz; do
      [ -f "$f" ] && return 0
    done
  done
  return 1
}

ARCHDIR="$DIR"
if [ -n "$SET" ]; then
  if [ -d "$DIR/$SET" ]; then
    ARCHDIR="$DIR/$SET"
  elif [ -d "$DIR/enterprise/$VERSION/$SET" ]; then
    ARCHDIR="$DIR/enterprise/$VERSION/$SET"
  elif [ "$(basename "$DIR")" = "$SET" ]; then
    ARCHDIR="$DIR"
  else
    die "no archive dir for --set '$SET' under $DIR/$SET or $DIR/enterprise/$VERSION/$SET"
  fi
elif has_set_layout; then
  die "release dir $DIR has commercial set prefixes (C02-17). Pass --set <slug> so the air-gap bundle is one SKU, not a mixed tree under the same basename."
fi

if [ "$LIST_FILES" != 1 ]; then
  [ -n "$SIGN_KEY" ] || die "--sign-key is required (dedicated OTA Ed25519 private key, base64)"
  [ -n "$OUT" ] || die "--out is required"
  [ -f "$SIGN_KEY" ] || die "sign-key file $SIGN_KEY does not exist"
fi

# Archives of THIS tree only (C02-17: ARCHDIR is the set dir when --set is set).
FILES=(manifest.json manifest.json.sig)
for f in "$ARCHDIR"/olivares_"$VERSION"_*.tar.gz; do
  [ -f "$f" ] || continue
  base="$(basename "$f")"
  case "$base" in *_fips_*) continue;; esac
  FILES+=("$base")
done
[ "${#FILES[@]}" -gt 2 ] || die "no platform archives olivares_${VERSION}_*.tar.gz found in $ARCHDIR"

if [ "$LIST_FILES" = 1 ]; then
  echo "SET=${SET:-}"
  echo "ARCHDIR=$ARCHDIR"
  printf '%s\n' "${FILES[@]}"
  exit 0
fi

# Resolve the olivares binary (build a local one from the worktree if not given).
if [ -z "$OLIVARES" ]; then
  OLIVARES="$(mktemp -d)/olivares"
  echo "==> building olivares (manifest producer) ..."
  ( cd "$ROOT/cmd/olivares" && GOWORK="${GOWORK:-}" go build -o "$OLIVARES" . )
fi
command -v "$OLIVARES" >/dev/null 2>&1 || [ -x "$OLIVARES" ] || die "olivares binary not runnable: $OLIVARES"

# Generate + sign the manifest IN the archive dir (relative names match the tar).
GEN_ARGS=(release manifest --dir "$ARCHDIR" --channel "$CHANNEL" --version "$VERSION"
  --out "$ARCHDIR/manifest.json" --sign-key "@$SIGN_KEY")
[ -n "$MINVER" ] && GEN_ARGS+=(--min-version "$MINVER")
[ "$SECURITY" -eq 1 ] && GEN_ARGS+=(--security)
[ -n "$ROLLOUT" ] && GEN_ARGS+=(--rollout "$ROLLOUT")
[ -n "$EXPIRES_IN" ] && GEN_ARGS+=(--expires-in "$EXPIRES_IN")
for a in "${ADVISORIES[@]:-}"; do [ -n "$a" ] && GEN_ARGS+=(--advisory "$a"); done
echo "==> generating signed manifest for $CHANNEL/$VERSION${SET:+ set=$SET} ..."
"$OLIVARES" "${GEN_ARGS[@]}"

# Same gate as the online ceremony: bind every digest to checksums.txt.
if [ "$CROSSCHECK" -eq 1 ]; then
  [ -f "$ARCHDIR/checksums.txt" ] || die "no $ARCHDIR/checksums.txt to cross-check the manifest against.
  The air-gap bundle would carry a manifest nothing but this script vouches for.
  Point --dir at the goreleaser output, or pass --no-crosscheck if you accept that."
  echo "==> cross-checking the manifest against $ARCHDIR/checksums.txt ..."
  "$OLIVARES" release verify-manifest --manifest "$ARCHDIR/manifest.json" \
    --checksums "$ARCHDIR/checksums.txt" --dir "$ARCHDIR" \
    --expect-channel "$CHANNEL" --expect-version "$VERSION"
else
  echo "==> WARNING: --no-crosscheck: the manifest is signed but bound to nothing but this script."
fi

echo "==> writing bundle $OUT (${#FILES[@]} entries) from $ARCHDIR ..."
tar -czf "$OUT" -C "$ARCHDIR" "${FILES[@]}"

echo "==> DONE. Air-gap install (100% offline):"
echo "     olivares upgrade --bundle $OUT --pubkey <ota.pub> --check"
echo "     olivares upgrade --bundle $OUT --pubkey <ota.pub> --yes"
