#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C03-32: Dodo adn_ ↔ fused-canon feature ↔ offer_id. Eight rows, no fifth.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-32-identifier-map: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-32-identifier-map: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0332_JSON:-design/c03-32-identifier-map.json}"
DOC="${OLIVARES_C0332_DOC:-design/C03-32-IDENTIFIER-MAP-2026-08-20.md}"
WF="${OLIVARES_C0332_WF:-commercial/license-worker/wrangler.jsonc}"
CLI="${OLIVARES_C0332_CLI:-cmd/olivares/cmd_license.go}"
SETS="${OLIVARES_C0332_SETS:-commercial/license-worker/src/download/sets.ts}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$WF" ] || cannot "missing wrangler config"
[ -f "$CLI" ] || cannot "missing license CLI"
[ -f "$SETS" ] || cannot "missing set codes"

grep -q 'Grants not populated' "$DOC" || fail "$DOC lost grants-not-populated"
grep -q 'Map written' "$DOC" || fail "$DOC lost map written"
if grep -qiE 'grants populated|fifth add-on accepted|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi

python3 - "$JSON" "$WF" "$CLI" "$SETS" <<'PY' || fail "identifier map disagrees with the live sources"
import json, re, sys

def fail(msg):
    print("check-c03-32-identifier-map: FAIL — %s" % msg, file=sys.stderr)
    raise SystemExit(1)

def cannot(msg):
    print("check-c03-32-identifier-map: COULD NOT LOOK — %s" % msg, file=sys.stderr)
    raise SystemExit(2)

def strip_jsonc(s):
    out = []
    i, n = 0, len(s)
    in_str = esc = False
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

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("grants_populated") is not False:
    fail("grants_populated must stay false")
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        fail("%s is not a 40-hex object id" % key)

entries = data.get("entries") or []
if len(entries) != 8:
    fail("want 8 add-on rows, got %d" % len(entries))

want_features = {"regulated", "ai-runtime-security", "compliance-packs", "identity-scale"}
want_codes = {"reg", "airs", "cp", "ids"}
seen_dodo = set()
seen_plan = set()
features = set()
for row in entries:
    dodo = row.get("dodo_id") or ""
    offer = row.get("offer_id") or ""
    feat = row.get("feature_id") or ""
    code = row.get("set_code") or ""
    plan = row.get("plan") or ""
    cad = row.get("cadence") or ""
    if not dodo.startswith("adn_"):
        fail("dodo_id %r is not an add-on id" % dodo)
    if dodo in seen_dodo:
        fail("duplicate dodo_id %s" % dodo)
    seen_dodo.add(dodo)
    if plan in seen_plan:
        fail("duplicate plan %s" % plan)
    seen_plan.add(plan)
    if feat not in want_features:
        fail("feature_id %r is not one of the four fused-canon ids" % feat)
    features.add(feat)
    if code not in want_codes:
        fail("set_code %r is not a known add-on code" % code)
    if not offer.startswith("self_hosted.business.addons."):
        fail("offer_id %r is not a canon add-on" % offer)
    if not offer.endswith(feat.replace("ai-runtime-security", "ai-runtime-security")):
        pass
    if offer.split(".")[-1] != feat:
        fail("offer_id %r does not end with feature_id %r" % (offer, feat))
    if cad == "month" and not plan.endswith("-m"):
        fail("monthly row plan %r" % plan)
    if cad == "year" and not plan.endswith("-y"):
        fail("yearly row plan %r" % plan)
    if cad not in ("month", "year"):
        fail("cadence %r" % cad)

if features != want_features:
    fail("feature set %s" % sorted(features))

cli = open(sys.argv[3], encoding="utf-8").read()
m = re.search(r"fusedCanonAddonIDs = \[\]string\{([^}]+)\}", cli)
if not m:
    cannot("fusedCanonAddonIDs not found")
ids = set(re.findall(r'"([^"]+)"', m.group(1)))
if ids != want_features:
    fail("CLI fused-canon ids %s" % sorted(ids))

sets = open(sys.argv[4], encoding="utf-8").read()
if '["airs", "cp", "ids", "reg"]' not in sets and "['airs', 'cp', 'ids', 'reg']" not in sets:
    # tolerate either quote style in ADDON_CODES
    codes = re.search(r"ADDON_CODES = \[([^\]]+)\]", sets)
    if not codes:
        cannot("ADDON_CODES not found")
    got = set(re.findall(r'"([^"]+)"', codes.group(1)))
    if got != want_codes:
        fail("ADDON_CODES %s" % sorted(got))

raw = open(sys.argv[2], encoding="utf-8").read()
try:
    wr = json.loads(strip_jsonc(raw))
except Exception as e:
    cannot("wrangler config is not JSON after comment strip: %s" % e)
prod = ((wr.get("env") or {}).get("production") or {}).get("vars") or {}
pmap_raw = prod.get("PRODUCT_MAP") or ""
try:
    pmap = json.loads(pmap_raw)
except Exception as e:
    cannot("PRODUCT_MAP is not JSON: %s" % e)
products = (pmap.get("products") or {})
for row in entries:
    dodo = row["dodo_id"]
    rec = products.get(dodo) or {}
    if rec.get("plan") != row["plan"]:
        fail("PRODUCT_MAP %s plan %r want %r" % (dodo, rec.get("plan"), row["plan"]))
    if rec.get("edition") != "Business":
        fail("PRODUCT_MAP %s edition %r" % (dodo, rec.get("edition")))

adn_in_map = {k for k in products if k.startswith("adn_")}
if adn_in_map != seen_dodo:
    fail("PRODUCT_MAP adn_ set != map (%d vs %d)" % (len(adn_in_map), len(seen_dodo)))
PY

say "check-c03-32-identifier-map: CLEAN — eight rows join adn_, feature, offer; grants unwritten."
exit 0
