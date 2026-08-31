#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-eco12-counsel-p0.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/eco12.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/scripts" "$TMP/tree/design/legal"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-eco12-counsel-p0.sh"
  cp "$ROOT/design/ECO-12-COUNSEL-P0-PACKAGE-2026-08-19.md" "$TMP/tree/design/"
  cp "$ROOT/design/legal/COUNSEL-LOTE-1-2026-07.md" "$TMP/tree/design/legal/"
  cp "$ROOT/design/PRICING-CANON.md" "$TMP/tree/design/"
  cp "$ROOT/LICENSING.md" "$TMP/tree/"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-eco12-counsel-p0.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return "$rc"
}

stage
if run; then ok "live package is CLEAN"
else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
sed -i 's/COUNSEL-P0/DECIDED/' "$TMP/tree/design/PRICING-CANON.md"
if run; then bad "COUNSEL-P0 closed in canon stayed CLEAN"
else ok "mutant (14d/7d decided without counsel) is killed"; fi

stage
sed -i 's/draft-for-counsel/published/' "$TMP/tree/design/PRICING-CANON.md"
if run; then bad "purchase_terms published stayed CLEAN"
else ok "mutant (terms published without counsel) is killed"; fi

stage
sed -i 's/pending a legal decision/decided by this lote/' "$TMP/tree/LICENSING.md"
if run; then bad "indemnity marked decided stayed CLEAN"
else ok "mutant (indemnity decided without founder) is killed"; fi

stage
sed -i 's/No enviado/Enviado/' "$TMP/tree/design/legal/COUNSEL-LOTE-1-2026-07.md"
if run; then bad "lote marked sent stayed CLEAN"
else ok "mutant (dossier sent without the owner) is killed"; fi

stage
sed -i 's/NO se envía/YA se envía/;s/No se envía/Ya se envía/' \
  "$TMP/tree/design/ECO-12-COUNSEL-P0-PACKAGE-2026-08-19.md"
if run; then bad "package claiming it sent stayed CLEAN"
else ok "mutant (package dictates the send) is killed"; fi

stage
if ! run; then bad "no-fire: live package should stay CLEAN ($(cat "$TMP/err"))"
else ok "no-fire: live presentable package stays CLEAN"; fi

stage
rm -f "$TMP/tree/design/ECO-12-COUNSEL-P0-PACKAGE-2026-08-19.md"
if run; then bad "missing package stayed CLEAN"
else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing package is COULD NOT LOOK"
  else bad "missing package should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-eco12-counsel-p0 selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
