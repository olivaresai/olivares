#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C05 remasure: both Cloud pdt_ map to plans; invite still exists;
# production fulfilment stays off. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-both-pdt-remeasure: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-both-pdt-remeasure: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C05R_JSON:-design/c05-both-pdt-remeasure.json}"
DOC="${OLIVARES_C05R_DOC:-design/C05-BOTH-PDT-REMEDIDO-2026-08-20.md}"
MAP="${OLIVARES_C05R_MAP:-cloud/control-plane/internal/billing/dodo-cloud-product-map.json}"
MGR="${OLIVARES_C05R_MGR:-cloud/control-plane/internal/tenant/manager.go}"
WF="${OLIVARES_C05R_WF:-commercial/license-worker/wrangler.jsonc}"
COMPOSE="${OLIVARES_C05R_COMPOSE:-cloud/staging/docker-compose.yml}"

SKU_M="pdt_0NlE7N9AZ9CV7wNAemXAO"
SKU_Y="pdt_0NlE7ZtwL8GfOeYefL7M8"

for f in "$JSON" "$DOC" "$MAP" "$MGR" "$WF" "$COMPOSE"; do
	[ -f "$f" ] || cannot "missing $f"
done

grep -q 'Production fulfilment stays off' "$DOC" || fail "$DOC lost production-off"
grep -q "$SKU_M" "$DOC" || fail "$DOC lost monthly SKU"
grep -q "$SKU_Y" "$DOC" || fail "$DOC lost yearly SKU"
if grep -qiE 'production fulfilment on|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a go-live this lote does not have"
fi

grep -q 'func (m \*Manager) inviteFirstOwner' "$MGR" \
	|| fail "inviteFirstOwner missing"

grep -q 'COMMERCE_PROVIDER:.*dodo' "$COMPOSE" \
	|| fail "staging compose is not dodo"
grep -q "$SKU_M" "$COMPOSE" || fail "staging compose lost monthly SKU"
grep -q "$SKU_Y" "$COMPOSE" || fail "staging compose lost yearly SKU"

python3 - "$JSON" "$MAP" "$WF" "$SKU_M" "$SKU_Y" <<'PY' || fail "SKU map or production catalogue drifted"
import json, re, sys

def fail(msg):
    print("check-c05-both-pdt-remeasure: FAIL — %s" % msg, file=sys.stderr)
    raise SystemExit(1)

def cannot(msg):
    print("check-c05-both-pdt-remeasure: COULD NOT LOOK — %s" % msg, file=sys.stderr)
    raise SystemExit(2)

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

meta, mmap, wf, sku_m, sku_y = sys.argv[1:6]
data = json.load(open(meta, encoding="utf-8"))
if data.get("sku_m") != sku_m or data.get("sku_y") != sku_y:
    fail("JSON SKUs drifted")
if data.get("plan_m") != "cloud-standard-m" or data.get("plan_y") != "cloud-standard-y":
    fail("JSON plans drifted")
if data.get("production_fulfillment") is not False:
    fail("production_fulfillment must stay false")
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        fail("%s is not a 40-hex object id" % key)

products = (json.load(open(mmap, encoding="utf-8")).get("products") or {})
if (products.get(sku_m) or {}).get("tier") != "cloud-standard-m":
    fail("product map monthly tier")
if (products.get(sku_y) or {}).get("tier") != "cloud-standard-y":
    fail("product map yearly tier")

try:
    wr = json.loads(strip_jsonc(open(wf, encoding="utf-8").read()))
except Exception as e:
    cannot("wrangler is not JSON after comment strip: %s" % e)
prod = ((wr.get("env") or {}).get("production") or {}).get("vars") or {}
if prod.get("FULFILLMENT_ENABLED") != "false":
    fail("production FULFILLMENT_ENABLED is %r" % prod.get("FULFILLMENT_ENABLED"))
catalog = json.loads(prod.get("DODO_CATALOG") or "{}")
cloud = catalog.get("cloud_products") or []
if sku_m not in cloud or sku_y not in cloud:
    fail("production cloud_products lost a Cloud SKU")
sets = catalog.get("set_codes") or {}
if sku_m in sets or sku_y in sets:
    fail("Cloud SKUs must not have OTA set_codes")
PY

say "check-c05-both-pdt-remeasure: CLEAN — both pdt_ mapped; invite present; production off."
exit 0
