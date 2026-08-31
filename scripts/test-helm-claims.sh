#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for check-helm-claims.sh. The pin takes a rendered manifest; it is
# not a zero-arg CI CHECK (usage without a file is COULD NOT LOOK). Mutants
# compile: they are YAML documents the awk either accepts or refuses.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-helm-claims.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/helm-claims.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

write_clean() {
  cat >"$1" <<'YAML'
---
kind: PersistentVolumeClaim
metadata:
  name: data-olivares-0
---
kind: Pod
metadata:
  name: engine
spec:
  volumes:
    - persistentVolumeClaim:
        claimName: data-olivares-0
YAML
}

write_dangling() {
  cat >"$1" <<'YAML'
---
kind: Pod
metadata:
  name: engine
spec:
  volumes:
    - persistentVolumeClaim:
        claimName: data-olivares-0
YAML
}

write_sts() {
  cat >"$1" <<'YAML'
---
kind: StatefulSet
metadata:
  name: olivares
spec:
  replicas: 1
  volumeClaimTemplates:
    - metadata:
        name: data
---
kind: Pod
metadata:
  name: engine
spec:
  volumes:
    - persistentVolumeClaim:
        claimName: data-olivares-0
YAML
}

run() {
  local rc=0
  bash "$CHECK" "$1" >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return "$rc"
}

write_clean "$TMP/clean.yaml"
if run "$TMP/clean.yaml"; then ok "live matching PVC is CLEAN"
else bad "live matching PVC should be CLEAN ($(cat "$TMP/err"))"; fi

write_dangling "$TMP/dangling.yaml"
if run "$TMP/dangling.yaml"; then bad "dangling claimName stayed CLEAN"
else
  rc=$(cat "$TMP/rc")
  if [ "$rc" = 1 ]; then ok "mutant (dangling claimName) is killed"
  else bad "dangling claimName rc=$rc want 1 ($(cat "$TMP/err"))"; fi
fi

write_sts "$TMP/sts.yaml"
if run "$TMP/sts.yaml"; then ok "StatefulSet volumeClaimTemplate produces the claim"
else bad "STS template should be CLEAN ($(cat "$TMP/err"))"; fi

rc=0
bash "$CHECK" "$TMP/missing.yaml" >/dev/null 2>"$TMP/err" || rc=$?
if [ "$rc" = 2 ]; then ok "missing manifest is COULD NOT LOOK"
else bad "missing manifest rc=$rc want 2 ($(cat "$TMP/err"))"; fi

rc=0
bash "$CHECK" >/dev/null 2>"$TMP/err" || rc=$?
if [ "$rc" = 2 ]; then ok "no-args is COULD NOT LOOK"
else bad "no-args rc=$rc want 2 ($(cat "$TMP/err"))"; fi

write_clean "$TMP/clean2.yaml"
if run "$TMP/clean2.yaml"; then ok "no-fire: live matching PVC stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-helm-claims selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
