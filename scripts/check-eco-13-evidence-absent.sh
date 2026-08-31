#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ECO-13 remainder: the seven close-tests named by the HOLD are absent.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco-13-evidence-absent: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco-13-evidence-absent: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ECO13A_JSON:-design/eco-13-scale-gates.json}"
HOLD="${OLIVARES_ECO13A_HOLD:-design/HOLD-SCALE-GATES-2026-08-19.md}"
DOC="${OLIVARES_ECO13A_DOC:-design/ECO-13-EVIDENCE-ABSENT-2026-08-20.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$HOLD" ] || cannot "missing $HOLD"
[ -f "$DOC" ] || cannot "missing $DOC"

grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'NO IMPLEMENTADO' "$DOC" || fail "$DOC lost NO IMPLEMENTADO"
if grep -qiE 'implemented the seven|opened cloud-scale|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an opening this lote does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags no longer say the evidence is absent"
import json, re, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("implemented") is not False:
    raise SystemExit("implemented must be false")
if data.get("sales_lane_opened") is not False:
    raise SystemExit("sales_lane_opened must be false")
if data.get("u_f") != "UNKNOWN" or data.get("u_d") != "UNKNOWN":
    raise SystemExit("U_f/U_d must stay UNKNOWN")
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
want_false = {
    "SCALE-REPORTING": "race_test",
    "SCALE-PQCPOSTURE": "e2e",
    "SCALE-ONBOARDING": "e2e",
    "SCALE-COMPUTER-USE-GATE": "drift_fixtures",
    "SCALE-RENDER-INSPECTOR": "calibrated_corpus",
    "SCALE-CREDENTIAL-MINTER": "tenant_isolation_test",
    "SCALE-LOGIN-ENFORCEMENT": "cross_tenant_login_test",
}
rows = {r.get("id"): r for r in (data.get("gates") or [])}
for gid, flag in want_false.items():
    row = rows.get(gid) or {}
    if row.get("status") != "HOLD":
        raise SystemExit("%s status %s" % (gid, row.get("status")))
    if row.get(flag) is not False:
        raise SystemExit("%s.%s must stay false" % (gid, flag))
PY

# Names from the HOLD close table. Match func Test… in Go only — the
# HOLD doc cites them in backticks.
TESTS='
TestReportingCrossTenantRace
TestPQCPostureE2E
TestOnboardingE2E
TestComputerUseTaxonomyDrift
TestRenderInspectorCorpus
TestCredentialTenantIsolation
TestLoginEnforcementCrossTenant
'

scan_tree() {
	local tree="$1"
	local found=""
	local t
	for t in $TESTS; do
		if grep -R --include='*.go' -l -E "^func ${t}\\(" "$tree" >/dev/null 2>&1; then
			found="$found $t"
		fi
	done
	printf '%s' "$found"
}

hits="$(scan_tree "$ROOT")"
if [ -n "$hits" ]; then
	fail "close-test present while HOLD says absent:$hits"
fi

ENT="${OLIVARES_ENT_DIR:-}"
if [ -n "$ENT" ]; then
	[ -d "$ENT" ] || cannot "OLIVARES_ENT_DIR is not a directory"
	hits="$(scan_tree "$ENT")"
	if [ -n "$hits" ]; then
		fail "overlay close-test present while HOLD says absent:$hits"
	fi
fi

say "check-eco-13-evidence-absent: CLEAN — seven close-tests absent; flags false; lane closed."
exit 0
