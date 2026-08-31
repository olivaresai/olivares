#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ALC-01-S4: inbound SCIM must not consult a commercial grant.
# Reads core/; does not write it. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-alc-01-s4-inbound-ungated: FAIL — $*" >&2; exit 1; }
cannot() { say "check-alc-01-s4-inbound-ungated: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ALC01S4_JSON:-design/alc-01-s4-inbound-ungated.json}"
DOC="${OLIVARES_ALC01S4_DOC:-design/ALC-01-S4-INBOUND-UNGATED-2026-08-20.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"

grep -q 'Inbound stays open' "$DOC" || fail "$DOC lost inbound-open"
grep -q 'HOLD on the motor' "$DOC" || fail "$DOC lost HOLD on the motor"
if grep -qiE 'inbound SCIM gated|managed SCIM shipped|ALC-01 complete' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("inbound_gated_by_grant") is not False:
    raise SystemExit("inbound_gated_by_grant must stay false")
if data.get("motor_implemented") is not False:
    raise SystemExit("motor_implemented must stay false")
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

FILES='
core/api/handlers_scim.go
core/api/handlers_scim_groups.go
core/api/handlers_scim_events.go
core/auth/scim.go
core/auth/scim_groups.go
core/auth/scim_events.go
'
for f in $FILES; do
	[ -f "$f" ] || cannot "missing $f"
	if grep -E 'addongate|addonGate|ErrNotEntitled|EntitlementFunc' "$f" >/dev/null; then
		fail "inbound SCIM file consults a commercial grant: $f"
	fi
done

say "check-alc-01-s4-inbound-ungated: CLEAN — inbound SCIM ungated by grant; motor unbuilt."
exit 0
