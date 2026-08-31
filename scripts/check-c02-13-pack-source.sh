#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c02-13-pack-source.sh — C02-13 slice 1. Pack codes live in ONE
# JSON; sets.ts and addon-sets.sh must derive, not diverge.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-13-pack-source: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-13-pack-source: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0213_JSON:-commercial/pack-composition.json}"
SETS="${OLIVARES_C0213_SETS:-commercial/license-worker/src/download/sets.ts}"
ADDON="${OLIVARES_C0213_ADDON:-scripts/addon-sets.sh}"
C13="${OLIVARES_C0213_C13:-commercial/module-slug-package.json}"
DOC="${OLIVARES_C0213_DOC:-design/C02-13-PACK-SOURCE-2026-08-19.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$SETS" ] || cannot "missing $SETS"
[ -f "$ADDON" ] || cannot "missing $ADDON"
[ -f "$C13" ] || cannot "missing C13-02 map $C13"
[ -f "$DOC" ] || cannot "missing $DOC"

python3 - "$JSON" "$SETS" "$ADDON" "$C13" <<'PY' || fail "pack-composition drifted from sets.ts or addon-sets.sh"
import itertools, json, re, sys

comp = json.load(open(sys.argv[1], encoding="utf-8"))
sets = open(sys.argv[2], encoding="utf-8").read()
addon = open(sys.argv[3], encoding="utf-8").read()
c13 = json.load(open(sys.argv[4], encoding="utf-8"))

base = comp.get("base") or {}
ent = comp.get("enterprise") or {}
addons = comp.get("addons") or []
if base.get("code") != "biz" or ent.get("code") != "ent":
    raise SystemExit("base/ent codes must stay biz/ent")
if base.get("product_id") != "self_hosted.business":
    raise SystemExit("base product_id drifted")
if ent.get("product_id") != "self_hosted.enterprise":
    raise SystemExit("enterprise product_id drifted")
codes = [a.get("code") for a in addons]
if sorted(codes) != ["airs", "cp", "ids", "reg"]:
    raise SystemExit("addon codes %s, want airs/cp/ids/reg" % codes)
# Derive slugs: biz, biz+sorted-subsets, ent. Same rule as sets.ts setSlug.
want_slugs = ["biz"]
for r in range(1, len(codes) + 1):
    for combo in itertools.combinations(sorted(codes), r):
        want_slugs.append("biz+" + "+".join(combo))
want_slugs.append("ent")
want_slugs = sorted(want_slugs)
if len(want_slugs) != 17:
    raise SystemExit("derived %d slugs, want 17" % len(want_slugs))

# sets.ts ADDON_CODES and ALLOWED_SET_SLUGS.
m = re.search(r"export const ADDON_CODES = \[([^\]]+)\]", sets)
if not m:
    raise SystemExit("sets.ts has no ADDON_CODES")
got_codes = re.findall(r'"([a-z]+)"', m.group(1))
if sorted(got_codes) != sorted(codes):
    raise SystemExit("sets.ts ADDON_CODES %s != composition %s" % (got_codes, codes))
listed = re.findall(r'^\s+"(biz[^"]*|ent)",?$', sets, re.M)
if sorted(listed) != want_slugs:
    raise SystemExit("ALLOWED_SET_SLUGS %s != derived %s" % (sorted(listed), want_slugs))
for a in addons:
    needle = '["%s", "%s"]' % (a["product_id"], a["code"])
    if needle not in sets:
        raise SystemExit("sets.ts missing PRODUCT_TO_SET_CODE %s" % needle)

# addon-sets.sh CODES dict: "regulated": "reg"
for a in addons:
    pat = '"%s": "%s"' % (a["addon"], a["code"])
    if pat not in addon:
        raise SystemExit("addon-sets.sh missing CODES %s" % pat)

entries = c13.get("entries") or []
if len(entries) < 20:
    raise SystemExit("C13-02 map too short (%d)" % len(entries))
PY

say "check-c02-13-pack-source: CLEAN — 17 slugs derived; sets.ts and addon-sets.sh match."
exit 0
