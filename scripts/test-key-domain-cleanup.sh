#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Mutation battery for the EXIT trap in test-key-domain-separation.sh.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SUT="$ROOT/scripts/test-key-domain-separation.sh"
_tmp_base="${TMPDIR:-/workspace/.tmp-hub-backend}"
mkdir -p "$_tmp_base"
_tmp_base="$(cd "$_tmp_base" && pwd -P)"
export TMPDIR="$_tmp_base"
WORK="$(mktemp -d "$_tmp_base/key-domain-cleanup.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$WORK/bins"
  mkdir -p "$WORK/bins"
  : > "$WORK/bins/PLACEHOLDER"
  : > "$WORK/bins/.olivares-key-domain-cleanup-selftest"
}

is_restored() {
  [ "$(find "$WORK/bins" -type f -printf '%f\n' | LC_ALL=C sort)" = "PLACEHOLDER" ]
}

stage
bash "$SUT" --selftest-cleanup "$WORK/bins"
if is_restored; then
  ok "EXIT trap restores bins to PLACEHOLDER only"
else
  bad "cleanup probe left first-party build artifacts"
fi

sed 's/^trap cleanup EXIT$/# MUTANT: EXIT trap removed/' \
  "$SUT" > "$WORK/mutant.sh"
if ! grep -q 'MUTANT: EXIT trap removed' "$WORK/mutant.sh"; then
  bad "could not construct the missing-EXIT-trap mutant"
else
  stage
  bash "$WORK/mutant.sh" --selftest-cleanup "$WORK/bins"
  if is_restored; then
    bad "mutant without the EXIT trap stayed green"
  else
    ok "mutant without the EXIT trap is killed by the populated-bin postcondition"
  fi
fi

printf 'test-key-domain-cleanup: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
