#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-module-bridge.sh — the slug↔package map is the VOCABULARIO table
# as data. Three answers: 0 clean · 1 drift · 2 could not look.

set -euo pipefail

say() { printf '%s\n' "$*"; }
fail() { say "check-module-bridge: FAIL — $*" >&2; exit 1; }
cannot() { say "check-module-bridge: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

VOC="$ROOT/design/VOCABULARIO-MODULOS-2026-08-08.md"
JSON="$ROOT/commercial/module-slug-package.json"
[ -r "$VOC" ] || cannot "cannot read $VOC"
[ -r "$JSON" ] || cannot "cannot read $JSON"

command -v python3 >/dev/null || cannot "no python3"

python3 - "$VOC" "$JSON" <<'PY'
import json, re, sys

voc_path, json_path = sys.argv[1], sys.argv[2]
try:
    text = open(voc_path, encoding="utf-8").read()
    data = json.load(open(json_path, encoding="utf-8"))
except OSError as e:
    print(f"unreadable: {e}", file=sys.stderr)
    sys.exit(2)

# Rows of the measured table: | `slug` | `enterprise/...` |
row = re.compile(
    r"^\|\s*`([^`]+)`\s*\|\s*`([^`]+)`[^|]*\|",
    re.M,
)
# Only the section that names sold ids (skip other tables).
start = text.find("| id vendido |")
if start < 0:
    print("VOCABULARIO has no 'id vendido' table", file=sys.stderr)
    sys.exit(2)
chunk = text[start:]
end = chunk.find("\n> ###")
if end > 0:
    chunk = chunk[:end]
voc = {}
for m in row.finditer(chunk):
    slug, pkg = m.group(1), m.group(2)
    if slug == "id vendido":
        continue
    voc[slug] = pkg.split()[0]  # drop trailing (`groupmap.go`)

entries = data.get("entries")
if not isinstance(entries, list) or not entries:
    print("JSON entries missing", file=sys.stderr)
    sys.exit(1)
js = {}
for e in entries:
    s, p = e.get("slug"), e.get("package")
    if not s or not p:
        print(f"incomplete entry {e!r}", file=sys.stderr)
        sys.exit(1)
    if s in js:
        print(f"duplicate slug {s}", file=sys.stderr)
        sys.exit(1)
    js[s] = p

drift = []
for s in sorted(set(voc) | set(js)):
    if s not in voc:
        drift.append(f"JSON has {s} but VOCABULARIO does not")
    elif s not in js:
        drift.append(f"VOCABULARIO has {s} but JSON does not")
    elif voc[s] != js[s]:
        drift.append(f"{s}: JSON {js[s]!r} != VOCABULARIO {voc[s]!r}")

# Non-bijective packages must stay named, not "fixed" into 1:1.
from collections import Counter
pkgs = Counter(js.values())
shared = {p: n for p, n in pkgs.items() if n > 1}
if "enterprise/federation" not in shared or "enterprise/wormretention" not in shared:
    print("expected federation and wormretention to serve two slugs each", file=sys.stderr)
    sys.exit(1)

if drift:
    for d in drift:
        print(d, file=sys.stderr)
    sys.exit(1)
print(f"ok {len(js)} slugs, {len(pkgs)} packages, shared={sorted(shared)}")
sys.exit(0)
PY
rc=$?
if [ "$rc" -eq 2 ]; then
  cannot "parser could not read the table or the JSON"
fi
if [ "$rc" -ne 0 ]; then
  fail "slug↔package map drifted from VOCABULARIO"
fi
say "check-module-bridge: CLEAN — JSON matches the measured VOCABULARIO table."
exit 0
