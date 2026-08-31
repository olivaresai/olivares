#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-eco-06-renewal-retries.sh — ECO-06. 168 h policy sourced, not applied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco-06-renewal-retries: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco-06-renewal-retries: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ECO06_JSON:-design/eco-06-renewal-retries.json}"
DOC="${OLIVARES_ECO06_DOC:-design/ECO-06-RENEWAL-RETRIES-HOLD-2026-08-19.md}"
CANON="${OLIVARES_ECO06_CANON:-design/PRICING-CANON.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$CANON" ] || cannot "missing $CANON"

grep -q 'NOT CONFIGURED' "$DOC" || fail "$DOC lost NOT CONFIGURED"
if grep -qiE 'retries applied|account retries set|168 h configured in the account' "$DOC"; then
	fail "$DOC claims an account write this lote does not have"
fi
grep -q 'renewal_retries_policy: within-published-168h-window' "$CANON" || \
	fail "canon lost renewal_retries_policy"

python3 - "$JSON" <<'PY' || fail "JSON failed the ECO-06 contract"
import json, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "eco-06-renewal-retries/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("configured") is not False:
    raise SystemExit("configured must stay false")
if data.get("account_looked") is not False:
    raise SystemExit("account_looked must stay false")
if data.get("window_hours") != 168:
    raise SystemExit("window_hours must stay 168")
if data.get("policy") != "within-published-168h-window":
    raise SystemExit("policy must stay within-published-168h-window")
if data.get("letter_to_provider") != "cancelled":
    raise SystemExit("letter_to_provider must stay cancelled")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
PY

say "check-eco-06-renewal-retries: CLEAN — 168 h sourced; not configured."
exit 0
