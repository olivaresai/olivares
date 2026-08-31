#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ECO remainder: six sales_lanes stay closed; U_f / U_d stay UNKNOWN.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco-sales-lanes-closed: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco-sales-lanes-closed: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ECOLANE_JSON:-design/eco-sales-lanes-closed.json}"
DOC="${OLIVARES_ECOLANE_DOC:-design/ECO-SALES-LANES-CLOSED-2026-08-20.md}"
CANON="${OLIVARES_ECOLANE_CANON:-design/PRICING-CANON.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$CANON" ] || cannot "missing pricing canon"

grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'Lanes stay closed' "$DOC" || fail "$DOC lost lanes-closed"
grep -q 'Does not fill U_f' "$DOC" || fail "$DOC lost U_f pin"
if grep -qiE 'opened a sales lane|FIRMA A claimed|U_f filled' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi

grep -q 'refund_fee_adder: UNKNOWN' "$CANON" \
	|| fail "canon lost refund_fee_adder: UNKNOWN"
grep -q 'dispute_fee_adder: UNKNOWN' "$CANON" \
	|| fail "canon lost dispute_fee_adder: UNKNOWN"

python3 - "$JSON" "$CANON" <<'PY' || fail "lanes or JSON drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("u_f") != "UNKNOWN" or data.get("u_d") != "UNKNOWN":
    raise SystemExit("U_f/U_d must stay UNKNOWN")
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
want = [
    "self-hosted-monthly",
    "self-hosted-annual",
    "cloud-standard-monthly",
    "cloud-standard-annual",
    "cloud-scale-monthly",
    "cloud-scale-annual",
]
lanes = data.get("lanes") or {}
if list(lanes) != want:
    raise SystemExit("JSON lane set drifted")
for name in want:
    if lanes.get(name) != "closed":
        raise SystemExit("%s JSON state %r" % (name, lanes.get(name)))

text = open(sys.argv[2], encoding="utf-8").read()
idx = text.find("\nsales_lanes:")
if idx < 0:
    raise SystemExit("sales_lanes block missing")
block = text[idx + 1 :]
nxt = re.search(r"\n[a-zA-Z_][a-zA-Z0-9_]*:", block[1:])
if nxt:
    block = block[: nxt.start() + 1]
for name in want:
    pat = r"(?m)^  %s:\n    state: closed\b" % re.escape(name)
    if not re.search(pat, block):
        raise SystemExit("%s is not closed in the canon" % name)
    if re.search(r"(?m)^  %s:\n    state: (?!closed\b)" % re.escape(name), block):
        raise SystemExit("%s opened" % name)
PY

say "check-eco-sales-lanes-closed: CLEAN — six lanes closed; U_f/U_d UNKNOWN."
exit 0
