#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# CFG-01 remasure: hostname RESOLVES; production stays unprovisioned.
# Does not live-query DNS. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-cfg-01-dns-resolves: FAIL — $*" >&2; exit 1; }
cannot() { say "check-cfg-01-dns-resolves: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_CFG01D_JSON:-design/cfg-01-dns-resolves.json}"
DOC="${OLIVARES_CFG01D_DOC:-design/CFG-01-DNS-RESOLVES-2026-08-20.md}"
WRA="${OLIVARES_CFG01D_WRA:-commercial/license-worker/wrangler.jsonc}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$WRA" ] || cannot "missing wrangler config"

grep -q 'NOT PROVISIONED' "$DOC" || fail "$DOC lost NOT PROVISIONED"
grep -q 'RESOLVES' "$DOC" || fail "$DOC lost RESOLVES"
if grep -qiE 'currently NXDOMAIN|is NXDOMAIN measured today|opened production|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a close or a stale NXDOMAIN as current"
fi

python3 - "$JSON" "$WRA" <<'PY' || fail "JSON/wrangler failed the DNS remasure"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
wra = open(sys.argv[2], encoding="utf-8").read()
if data.get("schema") != "cfg-01-dns-resolves/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("licenses_dns_nxdomain") is not False:
    raise SystemExit("licenses_dns_nxdomain must stay false")
if data.get("production_provisioned") is not False:
    raise SystemExit("production_provisioned must stay false")
if data.get("fulfillment_production") is not False:
    raise SystemExit("fulfillment_production must stay false")
if data.get("section9_condition") != "not-met":
    raise SystemExit("section9_condition must stay not-met")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
idx = wra.find('"production": {')
if idx < 0:
    raise SystemExit("no production env block")
window = wra[idx:idx + 4000]
if '"FULFILLMENT_ENABLED": "true"' in window:
    raise SystemExit("production FULFILLMENT_ENABLED must stay false")
if '"FULFILLMENT_ENABLED": "false"' not in window:
    raise SystemExit("production block lost FULFILLMENT_ENABLED false")
PY

say "check-cfg-01-dns-resolves: CLEAN — hostname RESOLVES; production unprovisioned."
exit 0
