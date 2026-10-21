#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Checks scripts/docs-captures-held.txt (or the file named as $1), the views the capture run leaves out so that
# their published image stays. A held view is not an exception that silences a gate: it names the open issue that
# owns its retake, and it is published in both themes or in neither (a view that never had a capture has no image
# to fall back on, and a half-published pair is the defect). A line that fails any of these lets a stale capture hide.
# The published set must also hold no two captures with the same bytes: the view whose id names a state it never
# reached (templates-readonly published a second copy of workspace-templates) passes every other check.
# manifest.json records the sha256 of each capture the last run took: each must equal its published file, and a held
# view must not be among a new run's takes. Legacy provenance is retained. Unnamed takes need not be
# published; every referenced or already published take still has its bytes checked. Zero-byte files fail too.
# The lint reads the issue field's format only; that the issue is open and its scope covers the cause is the reviewer's.
# `--selftest` runs the check over defective fixtures and fails unless each one is refused.
set -euo pipefail
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
SPEC="$ROOT/web/e2e/docs-captures.spec.ts"
PUBLIC="${DOCS_CAPTURE_PUBLIC:-$ROOT/docs-site/public/console}"
. "$ROOT/scripts/lib/capture-refs.sh"

check() {
  local file="$1" fail=0 n=0 ids=" " entry line raw id issue reason l d shas dups manifest takes mid mtheme msha f references modern=0
  [ -r "$file" ] || { echo "FAIL $file is not readable" >&2; return 1; }
  [ -d "$PUBLIC" ] || { echo "FAIL $PUBLIC is missing: every view would read as unpublished" >&2; return 1; }
  references="$(referenced_captures "$ROOT")" || return 1
  bad() { printf 'FAIL line %s: %s\n' "$line" "$1" >&2; fail=1; }
  while IFS= read -r entry; do
    n=$((n + 1))
    line="${entry%%:*}"
    raw="${entry#*:}"
    id="$(awk '{ print $1 }' <<<"$raw")"
    issue="$(awk '{ print $2 }' <<<"$raw")"
    reason="$(awk '{ $1 = $2 = ""; sub(/^[[:space:]]+/, ""); print }' <<<"$raw")"
    [[ "$id" =~ ^[A-Za-z0-9-]+$ ]] || { bad "the first field is a view id, got: $raw"; continue; }
    [[ "$issue" =~ ^#[0-9]+$ ]] || bad "$id: the second field is the open issue that owns the retake (#1234), got: ${issue:-nothing}"
    [ "${#reason}" -ge 12 ] || bad "$id: say why in a few words after the issue number"
    grep -qE "(^|[^_A-Za-z])id: '$id'," "$SPEC" || bad "$id: no such view in web/e2e/docs-captures.spec.ts"
    case "$ids" in *" $id "*) bad "$id: listed twice" ;; esac
    ids="$ids$id "
    [ -f "$PUBLIC/$id-light.png" ] && l=1 || l=0
    [ -f "$PUBLIC/$id-dark.png" ] && d=1 || d=0
    [ "$l" = "$d" ] || bad "$id: published in one theme only"
  done < <(grep -nvE '^[[:space:]]*(#|$)' "$file" || [ $? -eq 1 ])
  # A capture that cannot be read is a failure, not a capture without a twin.
  shas="$(cd "$PUBLIC" && find . -maxdepth 1 -name '*.png' -exec sha256sum {} +)" || {
    echo "FAIL could not hash every published capture in $PUBLIC" >&2
    return 1
  }
  dups="$(sort <<<"$shas" | uniq -w64 -D | awk '{ print substr($1, 1, 12), substr($0, 69) }')"
  if [ -n "$dups" ]; then
    printf 'FAIL published captures share their bytes (one of each group does not show the state its id names):\n%s\n' "$dups" >&2
    fail=1
  fi
  empty="$(cd "$PUBLIC" && find . -maxdepth 1 -name '*.png' -size 0 | sed 's#^\./##')"
  if [ -n "$empty" ]; then
    printf 'FAIL published captures of zero bytes:\n%s\n' "$empty" >&2
    fail=1
  fi
  manifest="$PUBLIC/manifest.json"
  jq -e 'has("skipped")' "$manifest" >/dev/null && modern=1
  if takes="$(jq -r '.take[] | "\(.id) \(.theme) \(.sha256)"' "$manifest")"; then
    while read -r mid mtheme msha; do
      [ -n "$mid" ] || continue
      f="$PUBLIC/$mid-$mtheme.png"
      if [ ! -f "$f" ]; then
        if grep -qxF "${f##*/}" <<<"$references"; then
          printf 'FAIL manifest.json takes %s %s and the referenced file is not published\n' "$mid" "$mtheme" >&2; fail=1
        fi
      elif [ "$(sha256sum "$f" | cut -d' ' -f1)" != "$msha" ]; then
        printf 'FAIL manifest.json records another sha256 than the published %s-%s.png\n' "$mid" "$mtheme" >&2; fail=1
      fi
      # Legacy manifests predate the held list. Keep their provenance without treating it as a new run.
      # Every new run records skipped views and must exclude held views from its takes.
      if [ "$modern" = 1 ]; then
        case "$ids" in *" $mid "*) printf 'FAIL %s is held and manifest.json still takes it\n' "$mid" >&2; fail=1 ;; esac
      fi
    done <<<"$takes"
  else
    printf 'FAIL %s is missing or has no take list\n' "$manifest" >&2
    fail=1
  fi
  [ "$fail" = 0 ] || return 1
  printf 'ok   %d held view(s) name an issue, exist in the spec, and are published in both themes or in neither; no two captures share bytes; manifest.json matches the files\n' "$n"
}

selftest() {
  local t out rc bad=0
  t="$(mktemp -d)"
  trap 'rm -rf "$t"' RETURN
  mkdir "$t/public"
  mkdir -p "$t/docs-site/src"
  printf 'console/health-light.png console/health-dark.png console/setup-light.png\n' >"$t/README.md"
  ROOT="$t"
  PUBLIC="$t/public"
  printf '{"skipped":[],"take":[]}\n' >"$t/public/manifest.json"
  expect() { # name, wanted rc, content
    printf '%b\n' "$3" >"$t/held.txt"
    rc=0; out="$(check "$t/held.txt" 2>&1)" || rc=$?
    if [ "$rc" = "$2" ]; then printf 'ok   %s\n' "$1"; else printf 'FAIL %s: rc=%s, wanted %s: %s\n' "$1" "$rc" "$2" "$out" >&2; bad=1; fi
  }
  expect 'a good line'                 0 'health #1821 shows only an empty state'
  expect 'no issue number'             1 'health shows only an empty state'
  expect 'reason too short'            1 'health #1821 empty'
  expect 'tab separated, no reason'    1 'health\t#1821\tx'
  expect 'unknown view'                1 'no-such-view #1821 shows only an empty state'
  expect 'listed twice'                1 'health #1821 shows only an empty state\nhealth #1821 shows only an empty state'
  expect 'bad id'                      1 'health! #1821 shows only an empty state'
  echo light >"$t/public/health-light.png"
  expect 'published in one theme only' 1 'health #1821 shows only an empty state'
  echo dark >"$t/public/health-dark.png"
  expect 'published in both themes'    0 'health #1821 shows only an empty state'
  lsha="$(sha256sum "$t/public/health-light.png" | cut -d' ' -f1)"
  printf '{"skipped":[],"take":[{"id":"health","theme":"light","sha256":"%s"}]}\n' "$lsha" >"$t/public/manifest.json"
  expect 'a manifest that matches'     0 'login #1821 shows only an empty state'
  expect 'a held view still taken'     1 'health #1821 shows only an empty state'
  printf '{"take":[{"id":"health","theme":"light","sha256":"%s"}]}' "$lsha" >"$t/public/manifest.json"
  expect 'legacy capture provenance is retained without claiming a retake' 0 'health #1821 shows only an empty state'
  printf '{"skipped":[],"take":[{"id":"health","theme":"light","sha256":"%064d"}]}\n' 0 >"$t/public/manifest.json"
  expect 'a manifest with another hash' 1 'login #1821 shows only an empty state'
  printf '{"skipped":[],"take":[{"id":"catalog","theme":"light","sha256":"%064d"}]}' 0 >"$t/public/manifest.json"
  expect 'an unnamed take stays in the manifest without publication' 0 'login #1821 shows only an empty state'
  printf '{"skipped":[],"take":[{"id":"setup","theme":"light","sha256":"%064d"}]}\n' 0 >"$t/public/manifest.json"
  expect 'a taken capture not published' 1 'login #1821 shows only an empty state'
  rm "$t/public/manifest.json"
  expect 'no manifest'                  1 'login #1821 shows only an empty state'
  printf '{"skipped":[],"take":[]}\n' >"$t/public/manifest.json"
  : >"$t/public/empty-light.png"
  expect 'an empty capture'            1 'login #1821 shows only an empty state'
  rm "$t/public/empty-light.png"
  cp "$t/public/health-light.png" "$t/public/other-light.png"
  expect 'two ids share their bytes'   1 'health #1821 shows only an empty state'
  rm "$t/public/other-light.png"
  ln -s "$t/no-such-file" "$t/public/dangling-light.png"
  expect 'an unreadable capture'       1 'health #1821 shows only an empty state'
  rm "$t/public/dangling-light.png"
  rm -rf "$t/public"
  expect 'missing public directory'    1 'health #1821 shows only an empty state'
  [ "$bad" = 0 ]
}

if [ "${1:-}" = "--selftest" ]; then
  selftest
else
  check "${1:-$ROOT/scripts/docs-captures-held.txt}"
fi
