#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# CFG-01 remasure: hostname RESOLVES. The 2026-08-20 record is read as history;
# production runs the 2026-09-17 order (an internal design note (not shipped)).
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
STATE="${OLIVARES_CFG01D_STATE:-design/PRODUCTION-STATE-2026-09-24.json}"
OFFERS="${OLIVARES_CFG01D_OFFERS:-commercial/license-worker/config/production/dodo-checkout-offers.json}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$WRA" ] || cannot "missing wrangler config"
[ -f "$STATE" ] || cannot "missing $STATE"
[ -f "$OFFERS" ] || cannot "missing $OFFERS"

grep -q 'NOT PROVISIONED' "$DOC" || fail "$DOC lost NOT PROVISIONED"
grep -q 'RESOLVES' "$DOC" || fail "$DOC lost RESOLVES"
if grep -qiE 'currently NXDOMAIN|is NXDOMAIN measured today|opened production|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a close or a stale NXDOMAIN as current"
fi

# The 2026-08-20 record, read as history: each field stays as it was measured.
python3 - "$JSON" <<'PY' || fail "the 2026-08-20 record failed the DNS remasure"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "cfg-01-dns-resolves/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("licenses_dns_nxdomain") is not False:
    raise SystemExit("licenses_dns_nxdomain no longer reads the measured false")
if data.get("production_provisioned") is not False:
    raise SystemExit("production_provisioned no longer reads the measured false")
if data.get("fulfillment_production") is not False:
    raise SystemExit("fulfillment_production no longer reads the measured false")
if data.get("section9_condition") != "not-met":
    raise SystemExit("section9_condition no longer reads the measured not-met")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s no longer reads the measured UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

python3 - check-cfg-01-dns-resolves "$WRA" "$STATE" "$OFFERS" <<'PY' || exit $?
# THE CURRENT PRODUCTION CONTRACT, the same block in every gate that reads production state
# (an internal design note (not shipped)): the 2026-09-17 order's sale switch and version, the
# four Business configurations by id in every sold place, and no Cloud id in any of them.
import json, sys

gate, wf, state_path, offers_path = sys.argv[1:5]
found = []

def cannot(msg):
    print(f"{gate}: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

def strip_jsonc(s):
    out, i, n, in_str, esc = [], 0, len(s), False, False
    while i < n:
        c = s[i]
        if in_str:
            out.append(c)
            if esc:
                esc = False
            elif c == "\\":
                esc = True
            elif c == '"':
                in_str = False
            i += 1
            continue
        if c == '"':
            in_str = True
            out.append(c)
            i += 1
            continue
        if c == "/" and i + 1 < n and s[i + 1] == "/":
            while i < n and s[i] != "\n":
                i += 1
            continue
        out.append(c)
        i += 1
    return "".join(out)

def load(path, jsonc=False):
    try:
        text = open(path, encoding="utf-8").read()
        return json.loads(strip_jsonc(text) if jsonc else text)
    except Exception as e:
        cannot(f"{path} is not readable JSON: {e}")

state = load(state_path)
configs = state.get("configurations") or {}
cloud_ids = {i for fam in (state.get("not_sold") or {}).values() for i in (fam.get("ids") or [])}
if state.get("schema") != "production-state/v1" or len(configs) != 4 or not cloud_ids:
    cannot(f"{state_path} does not name four configurations and the not-sold Cloud ids")

prod = (((load(wf, jsonc=True).get("env") or {}).get("production") or {}).get("vars") or {})
for key, value in (("FULFILLMENT_ENABLED", "true"), ("ENTERPRISE_VERSION", "26.9.0")):
    if prod.get(key) != value:
        found.append(f"production {key} is {prod.get(key)!r}, want {value!r} (the 2026-09-17 order)")

def var(name):
    raw = prod.get(name)
    if not raw:
        found.append(f"production {name} is missing")
        return {}
    try:
        return json.loads(raw)
    except Exception as e:
        cannot(f"production {name} is not JSON: {e}")

catalog = var("DODO_CATALOG")
product_map = var("PRODUCT_MAP").get("products") or {}
offer_sets = [(offers_path, load(offers_path).get("offers") or {})]
if "DODO_CHECKOUT" in prod:
    offer_sets.append(("production DODO_CHECKOUT", var("DODO_CHECKOUT").get("offers") or {}))

places = {f"DODO_CATALOG {k}": set(catalog.get(k) or {})
          for k in ("products", "addons", "cadence_months", "bundles", "set_codes")}
places["PRODUCT_MAP"] = set(product_map)

for name, conf in sorted(configs.items()):
    for cadence in ("monthly", "annual"):
        pid = conf.get(cadence)
        wanted = ["DODO_CATALOG products", "DODO_CATALOG cadence_months", "PRODUCT_MAP"]
        if conf.get("kind") == "bundle":
            wanted.append("DODO_CATALOG bundles")
        for where in wanted:
            if pid not in places[where]:
                found.append(f"configuration {name} {cadence} {pid} is missing from production {where}")
        if pid in product_map and (product_map[pid] or {}).get("edition") != "Business":
            found.append(f"configuration {name} {cadence} {pid} is not edition Business in production PRODUCT_MAP")
        for where, offers in offer_sets:
            if ((offers.get(name) or {}).get(cadence) or {}).get("product_id") != pid:
                found.append(f"configuration {name} {cadence} {pid} is missing from {where}")

catalogue = "production sells no Cloud, whatever the flags"
for pid in catalog.get("cloud_products") or []:
    found.append(f"CFG-12 catalogue: production DODO_CATALOG cloud_products lists {pid} — {catalogue}")
for where, ids in places.items():
    for pid in sorted(ids & cloud_ids):
        found.append(f"CFG-12 catalogue: Cloud id {pid} is in production {where} — {catalogue}")
for pid, entry in sorted(product_map.items()):
    entry = entry or {}
    if pid not in cloud_ids and (entry.get("edition") == "Cloud" or str(entry.get("plan", "")).startswith("cloud-")):
        found.append(f"CFG-12 catalogue: production PRODUCT_MAP maps {pid} to Cloud {entry.get('plan')!r} — {catalogue}")
for where, offers in offer_sets:
    for key, cadences in sorted(offers.items()):
        ids = {(c or {}).get("product_id") for c in (cadences or {}).values() if isinstance(c, dict)}
        if key in (state.get("not_sold") or {}) or key.startswith("cloud") or ids & cloud_ids:
            found.append(f"CFG-12 catalogue: {where} offers {key!r} — {catalogue}")

for line in found:
    print(f"{gate}: FAIL — {line}", file=sys.stderr)
sys.exit(1 if found else 0)
PY

say "check-cfg-01-dns-resolves: CLEAN — hostname RESOLVES as measured 2026-08-20; production runs the 2026-09-17 order: four Business configurations, no Cloud."
exit 0
