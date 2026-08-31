#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-cfg-10-sandbox-redeploy.sh — CFG-10. Sandbox was attempted and
# did not write. Production stays off. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-cfg-10-sandbox-redeploy: FAIL — $*" >&2; exit 1; }
cannot() { say "check-cfg-10-sandbox-redeploy: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_CFG10_JSON:-design/cfg-10-sandbox-redeploy.json}"
DOC="${OLIVARES_CFG10_DOC:-design/CFG-10-SANDBOX-REDEPLOY-2026-08-19.md}"
WRA="${OLIVARES_CFG10_WRA:-commercial/license-worker/wrangler.jsonc}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$WRA" ] || cannot "missing $WRA"

grep -q 'NOT DEPLOYED' "$DOC" || fail "$DOC lost NOT DEPLOYED"
if grep -qiE 'live deploy succeeded|opened production' "$DOC"; then
	fail "$DOC claims a result this lote does not have"
fi

python3 - "$JSON" "$WRA" <<'PY' || fail "JSON/wrangler failed the CFG-10 contract"
import json, re, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
wra = open(sys.argv[2], encoding="utf-8").read()
if data.get("deployed") is not False:
    raise SystemExit("deployed must be false")
if data.get("production_targeted") is not False:
    raise SystemExit("production_targeted must be false")
if data.get("buy_to_bytes") != "cannot-look":
    raise SystemExit("buy_to_bytes must stay cannot-look")
if data.get("live_deploy") != "auth-10000":
    raise SystemExit("live_deploy must name the measured 10000")
# Sandbox block must be true; production block must be false.
# Env blocks REPLACE the top-level vars; grep the quoted assignments
# next to the env names rather than any occurrence.
if '"FULFILLMENT_ENABLED": "false"' not in wra:
    raise SystemExit("production/base FULFILLMENT_ENABLED false missing")
# sandbox vars sit under "sandbox": and set true (measured in this file).
if not re.search(r'"sandbox"\s*:\s*\{[^}]*"FULFILLMENT_ENABLED": "true"', wra, re.S):
    # the sandbox block is large (catalog JSON). Search a tighter window.
    idx = wra.find('"sandbox"')
    if idx < 0:
        raise SystemExit("no sandbox env block")
    window = wra[idx:idx+2500]
    if '"FULFILLMENT_ENABLED": "true"' not in window:
        raise SystemExit("sandbox FULFILLMENT_ENABLED is not true")
if data.get("fulfillment_sandbox") is not True:
    raise SystemExit("JSON fulfillment_sandbox must be true")
if data.get("fulfillment_production") is not False:
    raise SystemExit("JSON fulfillment_production must be false")
PY

say "check-cfg-10-sandbox-redeploy: CLEAN — attempted, not deployed, production off."
exit 0
