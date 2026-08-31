#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ECO-11: RESERVE-FUNDED package is presentable. Row/amount/custody
# stay unsigned. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco-11-reserve-funded: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco-11-reserve-funded: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

PKG="${OLIVARES_ECO11_PKG:-design/ECO-11-RESERVE-FUNDED-2026-08-20.md}"
JSON="${OLIVARES_ECO11_JSON:-design/eco-11-reserve-funded.json}"
AB="${OLIVARES_ECO11_AB:-design/preparado/AB-01-RESERVA-HOJA-DE-DECISION-2026-08-01.md}"
CANON="${OLIVARES_ECO11_CANON:-design/PRICING-CANON.md}"

[ -f "$PKG" ] || cannot "missing $PKG"
[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$AB" ] || cannot "missing AB-01 sheet"
[ -f "$CANON" ] || cannot "missing pricing canon"

grep -q 'Se presenta' "$PKG" || fail "package no longer says it presents"
grep -Eq 'No se decide' "$PKG" || fail "package no longer says it does not decide"
grep -q 'AB-01' "$PKG" || fail "package lost the AB-01 signature vehicle"
grep -q 'does not sign, fund' "$PKG" || fail "package no longer refuses to dictate"
if grep -qiE 'I adopt S-B|selected_rate_row: S-B|FIRMA A claimed' "$PKG"; then
	fail "package claims a close this lote does not have"
fi

grep -q '\[ \] ADOPTO S-B' "$AB" || fail "AB-01 lost the S-B signature box"
grep -q '\[ \] ELIJO cuenta a la vista' "$AB" \
	|| fail "AB-01 lost the custody signature box"
grep -q '\[ \] MANTENGO checkout Cloud cerrado' "$AB" \
	|| fail "AB-01 lost the closed-checkout signature box"

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "eco-11-reserve-funded/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("presented") is not True:
    raise SystemExit("presented must stay true")
if data.get("decided") is not False:
    raise SystemExit("decided must stay false")
for k in ("selected_rate_row", "funded_amount", "u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
if data.get("recommended_rate_row") != "S-B":
    raise SystemExit("recommended_rate_row must stay S-B as the model candidate")
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

still_open() {
	local key="$1" what="$2"
	local lines
	lines="$(grep -E "^[[:space:]]*${key}:" "$CANON" || true)"
	[ -n "$lines" ] || fail "$what ($key) disappeared from the canon"
	if grep -qv 'UNKNOWN' <<<"$lines"; then
		fail "$what ($key) is no longer UNKNOWN"
	fi
}
still_open selected_rate_row "AF-02 row"
still_open refund_fee_adder "U_f"
still_open dispute_fee_adder "U_d"
still_open funded_amount "funded_amount"
grep -qE '^[[:space:]]*recommended_rate_row:[[:space:]]+S-B' "$CANON" \
	|| fail "recommended_rate_row is no longer S-B (model candidate, not a signature)"
grep -qE '^[[:space:]]*checkout_when_unknown_used_as_zero:[[:space:]]+forbidden' "$CANON" \
	|| fail "using UNKNOWN as zero is no longer forbidden"
grep -q 'RESERVE-FUNDED:.*founder-approved-row-amount-and-custody' "$CANON" \
	|| fail "RESERVE-FUNDED no longer requires signed row, amount and custody"

if grep -Eiq 'funded_amount:[[:space:]]*\$?[0-9]' "$PKG"; then
	fail "package states a funded_amount number"
fi

say "check-eco-11-reserve-funded: CLEAN — package presentable; boxes open; row/U_f/U_d/amount UNKNOWN."
exit 0
