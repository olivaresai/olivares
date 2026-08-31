#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C03-06: EvaluateOverride stays ungated; durableLicensed still unscoped.
# Overlay via OLIVARES_ENT_DIR. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-06-needs-decision: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-06-needs-decision: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0306_JSON:-design/c03-06-needs-decision.json}"
DOC="${OLIVARES_C0306_DOC:-design/C03-06-NEEDS-DECISION-2026-08-20.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"

grep -q 'EvaluateOverride is NO-GATE' "$DOC" || fail "$DOC lost NO-GATE"
grep -q 'HOLD on narrowing' "$DOC" || fail "$DOC lost HOLD on narrowing"
grep -q 'not to' "$DOC" || fail "$DOC lost the wrong-pack refusal"
if grep -qiE 'durableLicensed now scoped|EvaluateOverride gated|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a motor this lote does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("evaluate_override_gated") is not False:
    raise SystemExit("evaluate_override_gated must stay false")
if data.get("durable_addon_scoped") is not False:
    raise SystemExit("durable_addon_scoped must stay false")
if data.get("narrow_to_identity_scale") is not False:
    raise SystemExit("narrow_to_identity_scale must stay false")
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

ENT="${OLIVARES_ENT_DIR:-}"
[ -n "$ENT" ] || cannot "OLIVARES_ENT_DIR unset"
[ -d "$ENT" ] || cannot "OLIVARES_ENT_DIR is not a directory"

LH="$ENT/enterprise/rtbf/legalhold.go"
DB="$ENT/cmd-overlay/olivares/durablebus_enterprise.go"
[ -f "$LH" ] || cannot "missing legal-hold source"
[ -f "$DB" ] || cannot "missing durable-bus source"

if grep -E 'addongate|addonGate|EntitlementFunc|ErrNotEntitled' "$LH" >/dev/null; then
	fail "legal-hold override evaluation consults a commercial grant"
fi
grep -q 'func (h \*LegalHoldOverride) EvaluateOverride' "$LH" \
	|| fail "EvaluateOverride missing"

python3 - "$DB" <<'PY' || fail "durableLicensed is no longer the unscoped term check"
import re, sys
text = open(sys.argv[1], encoding="utf-8").read()
start = text.find("func durableLicensed(")
if start < 0:
    raise SystemExit("durableLicensed not found")
# Body: from the first '{' after the signature to the matching '}'.
brace = text.find("{", start)
if brace < 0:
    raise SystemExit("durableLicensed has no body")
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
    raise SystemExit("durableLicensed body unclosed")
body = text[brace : end + 1]
if "StatusExpired" not in body:
    raise SystemExit("durableLicensed lost the term check")
for tok in ("Features", "grants", "addonGate", "Authorize", "EntitlementFunc"):
    if tok in body:
        raise SystemExit("durableLicensed consults %s" % tok)
PY

say "check-c03-06-needs-decision: CLEAN — EvaluateOverride ungated; durableLicensed unscoped."
exit 0
