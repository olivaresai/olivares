#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-eco-07-3ds-renewal.sh — ECO-07. 3DS renewal not captured in Test Mode.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco-07-3ds-renewal: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco-07-3ds-renewal: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ECO07_JSON:-design/eco-07-3ds-renewal.json}"
DOC="${OLIVARES_ECO07_DOC:-design/ECO-07-3DS-RENEWAL-HOLD-2026-08-19.md}"
CANON="${OLIVARES_ECO07_CANON:-design/PRICING-CANON.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$CANON" ] || cannot "missing $CANON"

grep -q 'NOT CAPTURED' "$DOC" || fail "$DOC lost NOT CAPTURED"
if grep -qiE '3DS renewal captured|captured in Test Mode|live renewal captured' "$DOC"; then
	fail "$DOC claims a capture this lote does not have"
fi
grep -q 'three_ds: on' "$CANON" || fail "canon lost three_ds: on"

python3 - "$JSON" <<'PY' || fail "JSON failed the ECO-07 contract"
import json, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "eco-07-3ds-renewal/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("captured") is not False:
    raise SystemExit("captured must stay false")
if data.get("test_mode") is not True:
    raise SystemExit("test_mode must stay true in this container")
if data.get("measurable_in_test_mode") is not False:
    raise SystemExit("measurable_in_test_mode must stay false")
if data.get("three_ds_policy") != "on":
    raise SystemExit("three_ds_policy must stay on")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
PY

say "check-eco-07-3ds-renewal: CLEAN — not captured; not measurable in Test Mode."
exit 0
