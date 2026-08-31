#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# CFG-12 unique leftover unique vs #940: pin wrangler CLOUD_FORWARD_ENABLED
# "false" in all three blocks while production still lists two Cloud pdt_.
# Does not copy catalog.ts / dodo-catalog.test.ts / preflight DATOS.
# Three answers: 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-cfg-12-forward-off: FAIL — $*" >&2; exit 1; }
cannot() { say "check-cfg-12-forward-off: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

DOC="${OLIVARES_CFG12_DOC:-design/CFG-12-FORWARD-OFF-PIN-2026-08-20.md}"
WF="${OLIVARES_CFG12_WRA:-commercial/license-worker/wrangler.jsonc}"
CAT="${OLIVARES_CFG12_CAT:-commercial/license-worker/src/dodo/catalog.ts}"

[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$WF" ] || cannot "missing $WF"
[ -r "$CAT" ] || cannot "missing $CAT"
command -v python3 >/dev/null || cannot "no python3"

grep -q 'CLOUD_FORWARD_ENABLED stays false' "$DOC" \
  || fail "prepare doc lost CLOUD_FORWARD_ENABLED stays false"
grep -q 'does not copy' "$DOC" \
  || fail "prepare doc no longer says it does not copy catalog.ts"
grep -q 'Unique leftover unique vs `#940`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #940"

grep -q 'export function cloudSaleForwardBlock' "$CAT" \
  || fail "catalog.ts lost cloudSaleForwardBlock (on main; this lote does not rewrite it)"

python3 - "$WF" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-cfg-12-forward-off: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-cfg-12-forward-off: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

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

raw = open(sys.argv[1], encoding="utf-8").read()
try:
    doc = json.loads(strip_jsonc(raw))
except Exception as e:
    cannot(f"wrangler.jsonc is not JSON after comment strip: {e}")

want_ids = [
    "pdt_0NlE7N9AZ9CV7wNAemXAO",
    "pdt_0NlE7ZtwL8GfOeYefL7M8",
]

def vars_of(block, who):
    v = (block or {}).get("vars")
    if not isinstance(v, dict) or not v:
        fail(f"{who} vars missing")
    return v

base = vars_of(doc, "base")
envs = doc.get("env") or {}
sand = vars_of(envs.get("sandbox"), "env.sandbox")
prod = vars_of(envs.get("production"), "env.production")

blocks = (("base", base), ("sandbox", sand), ("production", prod))
for who, v in blocks:
    got = v.get("CLOUD_FORWARD_ENABLED")
    if got != "false":
        fail(f"{who} CLOUD_FORWARD_ENABLED is {got!r}, want 'false'")

if prod.get("FULFILLMENT_ENABLED") != "false":
    fail(
        "production FULFILLMENT_ENABLED is %r with Cloud SKUs listed "
        "and forward off — CFG-12 HOLD (do not flip production sale)"
        % (prod.get("FULFILLMENT_ENABLED"),)
    )

raw_cat = prod.get("DODO_CATALOG") or ""
try:
    cat = json.loads(raw_cat)
except Exception as e:
    cannot(f"production DODO_CATALOG is not JSON: {e}")

cloud = cat.get("cloud_products")
if cloud != want_ids:
    fail(f"production cloud_products is {cloud!r}, want {want_ids!r}")

print(
    "check-cfg-12-forward-off: CLEAN — 3/3 CLOUD_FORWARD_ENABLED false; "
    "production sale OFF; two Cloud pdt_ still listed."
)
PY
