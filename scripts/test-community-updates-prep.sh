#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for scripts/check-community-updates-prep.sh (CFG-06), rewritten 2026-08-27 when
# the gate stopped freezing a dead literal and started comparing the CLIENT against the
# PRODUCER. Every case below is a defect that can actually happen to those two files, and
# each mutant is applied to a STAGED COPY that the positive control re-stages, so a mutation
# that failed to apply cannot be read as a mutant that survived.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-community-updates-prep.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/cfg06.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/cmd/olivares" "$TMP/tree/core/release" "$TMP/tree/design" \
    "$TMP/tree/scripts" "$TMP/tree/.github/workflows"
  cp "$ROOT/cmd/olivares/cmd_upgrade.go" "$TMP/tree/cmd/olivares/"
  cp "$ROOT/core/release/channelurl.go" "$TMP/tree/core/release/"
  cp "$ROOT/design/CFG-06-COMMUNITY-CHANNEL-PREP-2026-08-20.md" "$TMP/tree/design/"
  cp "$ROOT/.github/workflows/release.yml" "$TMP/tree/.github/workflows/"
  cp "$ROOT/scripts/release-attach-stable-pair.sh" "$TMP/tree/scripts/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-community-updates-prep.sh"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-community-updates-prep.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}
# mutate_or_die applies a sed and PROVES it changed the file. A mutant that silently fails
# to apply reports the gate as blind when the gate is fine.
mutate_or_die() {
  local file="$1" expr="$2" before after
  before="$(cksum <"$file")"
  sed -i "$expr" "$file"
  after="$(cksum <"$file")"
  [ "$before" != "$after" ] || {
    echo "BATTERY BROKEN: the mutant '$expr' did not change $file" >&2
    exit 1
  }
}

stage; run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "carrier + producer + doc is CLEAN"
else bad "the tree as shipped should be CLEAN ($(cat "$TMP/err"))"; fi

stage
mutate_or_die "$TMP/tree/cmd/olivares/cmd_upgrade.go" \
  's|defaultCommunityEndpoint  = "https://github.com/olivaresai/olivares"|defaultCommunityEndpoint  = "https://olivares.ai/updates"|'
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (carrier reverted to the 404 path) is killed"
else bad "a reverted carrier stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
mutate_or_die "$TMP/tree/core/release/channelurl.go" \
  's|return l\.assetURL(l\.channel + "-manifest\.json")|return l.assetURL(l.channel + "-update.json")|'
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (client renames the manifest asset) is killed"
else bad "a renamed client asset stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
mutate_or_die "$TMP/tree/.github/workflows/release.yml" \
  's|gh release upload "${RELEASE_TAG}" dist/stable-manifest\.json$|gh release upload "${RELEASE_TAG}" dist/stable-ota.json|'
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (PRODUCER renames the stable manifest asset) is killed"
else bad "a renamed producer asset stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
mutate_or_die "$TMP/tree/.github/workflows/release.yml" \
  's|gh release upload "${RELEASE_TAG}" dist/security-manifest\.json$|gh release upload "${RELEASE_TAG}" dist/sec.json|'
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (producer drops the security manifest asset) is killed"
else bad "a renamed security asset stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
mutate_or_die "$TMP/tree/scripts/release-attach-stable-pair.sh" \
  's|ota-dist/stable-manifest\.json ota-dist/stable-manifest\.json\.sig|ota-dist/stable-manifest.json|'
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (ceremony stops attaching the .sig) is killed"
else bad "a missing signature attachment stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
mutate_or_die "$TMP/tree/design/CFG-06-COMMUNITY-CHANNEL-PREP-2026-08-20.md" 's/NO publicado/ya publicado/g'
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (doc claims published) is killed"
else bad "doc claiming published stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# The THIRD answer, in both of the ways this gate can lose sight of a half.
stage
rm -f "$TMP/tree/core/release/channelurl.go"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing layout resolver is COULD NOT LOOK"
else bad "missing client rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/.github/workflows/release.yml"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing PRODUCER is COULD NOT LOOK, not CLEAN"
else bad "missing producer rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/scripts/release-attach-stable-pair.sh"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing ceremony is COULD NOT LOOK, not CLEAN"
else bad "missing ceremony rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

# NON-FIRING DIRECTION. A gate that refuses everything passes every "it refuses" case above
# and is worthless; re-staging clean must still be CLEAN after all that mutation.
stage; run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: the unmutated tree is still CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

# ⛔ THE MUTANT THE CONTRAST ASKED FOR: a COMMENTED-OUT copy of the expected line must not
# satisfy the gate. Every expectation is matched as a whole line for exactly this reason — a
# plain substring grep is happy with the corpse of the old line sitting above the new one.
stage
mutate_or_die "$TMP/tree/core/release/channelurl.go" \
  's|^\(\s*\)return l\.assetURL(l\.channel + "-manifest\.json")$|\1// return l.assetURL(l.channel + "-manifest.json")\n\1return l.assetURL(l.channel + "-update.json")|'
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (real line changed, old one left as a COMMENT) is killed"
else bad "a commented-out copy satisfied the gate: rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
mutate_or_die "$TMP/tree/.github/workflows/release.yml" \
  's|^\(\s*\)gh release upload "${RELEASE_TAG}" dist/stable-manifest\.json$|\1# gh release upload "${RELEASE_TAG}" dist/stable-manifest.json\n\1gh release upload "${RELEASE_TAG}" dist/stable-ota.json|'
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (producer line changed, old one left as a COMMENT) is killed"
else bad "a commented-out producer line satisfied the gate: rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# And the mention-vs-declaration direction: the OLD endpoint appearing in PROSE (it does —
# the constant's own doc comment explains why it moved) must not be read as the constant.
stage
grep -q 'https://olivares.ai/updates' "$TMP/tree/cmd/olivares/cmd_upgrade.go" || {
  echo "BATTERY BROKEN: the positive control assumes the old endpoint is still mentioned in prose" >&2
  exit 1
}
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: the old endpoint in a COMMENT is not the constant"
else bad "prose mentioning the old endpoint turned the gate red ($(cat "$TMP/err"))"; fi

echo "check-community-updates-prep selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
