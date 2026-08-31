#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C03-06 unique leftover unique vs overlay-gated
# check-c03-06-needs-decision.sh: hub-safe HOLD so lint:addon-sets
# does not LOOK 2 without OLIVARES_ENT_DIR.
# Three answers: 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-06-needs-decision-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-06-needs-decision-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0306P_JSON:-design/c03-06-needs-decision-prep-2026-08-20.json}"
DOC="${OLIVARES_C0306P_DOC:-design/C03-06-NEEDS-DECISION-PREP-2026-08-20.md}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c03-06-needs-decision.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs overlay-gated C03-06 check"
grep -q 'HOLD on narrowing' "$DOC" || fail "prepare doc lost HOLD on narrowing"
grep -q 'EvaluateOverride is NO-GATE' "$DOC" \
  || fail "prepare doc lost NO-GATE"
grep -q 'not to identity-scale' "$DOC" \
  || fail "prepare doc lost the wrong-pack refusal"
if grep -qiE 'durableLicensed now scoped|EvaluateOverride gated|FIRMA A claimed' "$DOC"; then
  fail "prepare doc claims a motor this lote does not have"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c03-06-needs-decision-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c03-06-needs-decision-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"JSON is not readable: {e}")

if data.get("schema") != "c03-06-needs-decision-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("evaluate_override_gated") is not False:
    fail("evaluate_override_gated must stay false")
if data.get("durable_addon_scoped") is not False:
    fail("durable_addon_scoped must stay false")
if data.get("narrow_to_identity_scale") is not False:
    fail("narrow_to_identity_scale must stay false")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
sha = data.get("overlay_main_sha") or ""
if len(sha) != 40 or any(c not in "0123456789abcdef" for c in sha):
    fail("overlay_main_sha is not 40-hex")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)
print("json-ok")
PY

say "check-c03-06-needs-decision-prep: CLEAN — needs-decision HOLD; hub-safe; overlay not remasured."
exit 0
