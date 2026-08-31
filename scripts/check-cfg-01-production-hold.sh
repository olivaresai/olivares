#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-cfg-01-production-hold.sh — CFG-01. Production stays unprovisioned.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-cfg-01-production-hold: FAIL — $*" >&2; exit 1; }
cannot() { say "check-cfg-01-production-hold: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_CFG01_JSON:-design/cfg-01-production-hold.json}"
DOC="${OLIVARES_CFG01_DOC:-design/CFG-01-PRODUCTION-HOLD-2026-08-19.md}"
WRA="${OLIVARES_CFG01_WRA:-commercial/license-worker/wrangler.jsonc}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$WRA" ] || cannot "missing $WRA"

grep -q 'NOT PROVISIONED' "$DOC" || fail "$DOC lost NOT PROVISIONED"
if grep -qiE 'production deployed|opened production|wrangler deploy --env production' "$DOC"; then
	fail "$DOC claims a production write this lote does not have"
fi

python3 - "$JSON" "$WRA" <<'PY' || fail "JSON/wrangler failed the CFG-01 contract"
import json, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
wra = open(sys.argv[2], encoding="utf-8").read()

if data.get("schema") != "cfg-01-production-hold/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("production_provisioned") is not False:
    raise SystemExit("production_provisioned must stay false")
if data.get("production_targeted") is not False:
    raise SystemExit("production_targeted must stay false")
if data.get("sandbox_verified") is not False:
    raise SystemExit("sandbox_verified must stay false")
if data.get("sandbox_contrasted") is not False:
    raise SystemExit("sandbox_contrasted must stay false")
if data.get("sandbox_complete") is not False:
    raise SystemExit("sandbox_complete must stay false")
if data.get("fulfillment_production") is not False:
    raise SystemExit("fulfillment_production must stay false")
if data.get("section9_condition") != "not-met":
    raise SystemExit("section9_condition must stay not-met")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
if '"FULFILLMENT_ENABLED": "false"' not in wra:
    raise SystemExit("production/base FULFILLMENT_ENABLED false missing")
# The env object, not a comment that quotes the word.
idx = wra.find('"production": {')
if idx < 0:
    raise SystemExit("no production env block")
window = wra[idx:idx + 4000]
if '"FULFILLMENT_ENABLED": "true"' in window:
    raise SystemExit("production FULFILLMENT_ENABLED must stay false")
if '"FULFILLMENT_ENABLED": "false"' not in window:
    raise SystemExit("production block lost FULFILLMENT_ENABLED false")
PY

say "check-cfg-01-production-hold: CLEAN — production unprovisioned; §9 not met."
exit 0
