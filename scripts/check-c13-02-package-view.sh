#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C13-02 remainder: reverse package→slugs view matches the source.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-02-package-view: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-02-package-view: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C1302V_JSON:-design/c13-02-package-view.json}"
DOC="${OLIVARES_C1302V_DOC:-design/C13-02-PACKAGE-VIEW-2026-08-20.md}"
SRC="${OLIVARES_C1302V_SRC:-commercial/module-slug-package.json}"
VIEW="${OLIVARES_C1302V_VIEW:-commercial/module-package-slugs.json}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$SRC" ] || cannot "missing source map"
[ -f "$VIEW" ] || cannot "missing reverse view"

grep -q 'Not bijective' "$DOC" || fail "$DOC lost not-bijective"
grep -q 'Pack slugs' "$DOC" || fail "$DOC lost pack-slugs-off"
if grep -qiE 'FIRMA A claimed|view is bijective|pack slug landed' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi

python3 - "$JSON" "$SRC" "$VIEW" <<'PY' || fail "view drifted from source"
import json, re, sys

banned = {"biz", "biz+airs", "biz+ids", "biz+reg", "biz+cp"}

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c13-02-package-view/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("bijective") is not False:
    raise SystemExit("bijective must stay false")
if data.get("pack_slugs_present") is not False:
    raise SystemExit("pack_slugs_present must stay false")
if data.get("source_entries") != 27:
    raise SystemExit("source_entries must stay 27")
if data.get("packages") != 25:
    raise SystemExit("packages must stay 25")
shared = list(data.get("shared_packages") or [])
if shared != ["enterprise/federation", "enterprise/wormretention"]:
    raise SystemExit("shared_packages drifted: %r" % shared)
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

src = json.load(open(sys.argv[2], encoding="utf-8"))
view = json.load(open(sys.argv[3], encoding="utf-8"))
entries = src.get("entries") or []
if len(entries) != 27:
    raise SystemExit("source entries %d, want 27" % len(entries))

want = {}
for row in entries:
    slug = row.get("slug") or ""
    pkg = row.get("package") or ""
    if slug in banned:
        raise SystemExit("source grew a pack slug %s" % slug)
    want.setdefault(pkg, []).append(slug)
for pkg in want:
    want[pkg] = sorted(set(want[pkg]))
got = view.get("packages") or {}
if set(got) != set(want):
    raise SystemExit("package set drifted")
for pkg, slugs in want.items():
    if list(got.get(pkg) or []) != slugs:
        raise SystemExit("package %s slugs drifted" % pkg)
if len(got) != 25:
    raise SystemExit("view package count %d, want 25" % len(got))
for slugs in got.values():
    for slug in slugs:
        if slug in banned:
            raise SystemExit("view grew a pack slug %s" % slug)
# not bijective: at least one package with two slugs
if not any(len(v) > 1 for v in got.values()):
    raise SystemExit("view became bijective")
if len(got.get("enterprise/federation") or []) != 2:
    raise SystemExit("federation lost its two slugs")
if len(got.get("enterprise/wormretention") or []) != 2:
    raise SystemExit("wormretention lost its two slugs")
PY

say "check-c13-02-package-view: CLEAN — reverse view matches source; not bijective; packs off."
exit 0
