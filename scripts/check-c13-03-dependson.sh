#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C13-03: overlay-main promoteActivation still ignores DependsOn.
# Overlay via OLIVARES_ENT_DIR. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-03-dependson: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-03-dependson: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C1303_JSON:-design/c13-03-dependson.json}"
DOC="${OLIVARES_C1303_DOC:-design/C13-03-DEPENDSON-HOLD-2026-08-20.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"

grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'DependsOn not honoured' "$DOC" || fail "$DOC lost the honour pin"
if grep -qiE 'DependsOn honoured on overlay main|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("depends_on_honoured") is not False:
    raise SystemExit("depends_on_honoured must stay false")
if data.get("declared_on_legalhold_reconcile") is not True:
    raise SystemExit("declared_on_legalhold_reconcile must stay true")
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

ENT="${OLIVARES_ENT_DIR:-}"
[ -n "$ENT" ] || cannot "OLIVARES_ENT_DIR unset"
[ -d "$ENT" ] || cannot "OLIVARES_ENT_DIR is not a directory"
PROMOTE="$ENT/cmd-overlay/olivares/activation_apply_enterprise.go"
CAT="$ENT/enterprise/activation/catalog.go"
[ -f "$PROMOTE" ] || cannot "missing promoteActivation source"
[ -f "$CAT" ] || cannot "missing activation catalog"

grep -q 'DependsOn: "audit-worm-archive"' "$CAT" \
	|| fail "catalog lost legalhold-reconcile DependsOn declaration"

python3 - "$PROMOTE" <<'PY' || fail "promoteActivation is no longer the unhonoured flip"
import sys
text = open(sys.argv[1], encoding="utf-8").read()
start = text.find("func promoteActivation(")
if start < 0:
    raise SystemExit("promoteActivation not found")
brace = text.find("{", start)
if brace < 0:
    raise SystemExit("promoteActivation has no body")
depth = 0
end = None
for i, ch in enumerate(text[brace:], brace):
    if ch == "{":
        depth += 1
    elif ch == "}":
        depth -= 1
        if depth == 0:
            end = i
            break
if end is None:
    raise SystemExit("promoteActivation body unclosed")
body = text[brace : end + 1]
if "DependsOn" in body:
    raise SystemExit("promoteActivation consults DependsOn")
if "State = ActivationActive" not in body:
    raise SystemExit("promoteActivation lost the active flip")
PY

say "check-c13-03-dependson: CLEAN — DependsOn declared; promote still ignores it."
exit 0
