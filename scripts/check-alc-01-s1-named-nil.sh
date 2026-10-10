#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ALC-01-S1 hub half: the managedSCIM edition port exists and is nil; boot does not call it.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-alc-01-s1-named-nil: FAIL — $*" >&2; exit 1; }
cannot() { say "check-alc-01-s1-named-nil: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ALC01S_JSON:-design/alc-01-s1-named-nil.json}"
DOC="${OLIVARES_ALC01S_DOC:-design/ALC-01-S1-NAMED-NIL-2026-08-20.md}"
NOENT="${OLIVARES_ALC01S_NOENT:-cmd/olivares/wire_noenterprise.go}"
PORTS="${OLIVARES_ALC01S_PORTS:-cmd/olivares/edition_ports.go}"
BOOT="${OLIVARES_ALC01S_BOOT:-cmd/olivares/wire.go}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$NOENT" ] || cannot "missing noenterprise wire"
[ -f "$PORTS" ] || cannot "missing edition ports"
[ -f "$BOOT" ] || cannot "missing shared wire"

grep -q 'HOLD on the motor' "$DOC" || fail "$DOC lost HOLD on the motor"
grep -q 'Seam named' "$DOC" || fail "$DOC lost seam named"
if grep -qiE 'managed SCIM shipped|ALC-01 complete|invented /v1/managed' "$DOC"; then
	fail "$DOC claims a close this batch does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("hub_seam_named") is not True:
    raise SystemExit("hub_seam_named must be true")
if data.get("motor_implemented") is not False:
    raise SystemExit("motor_implemented must stay false")
if data.get("api_path_invented") is not False:
    raise SystemExit("api_path_invented must stay false")
if data.get("wired_into_boot") is not False:
    raise SystemExit("wired_into_boot must stay false")
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

# The seam is the managedSCIM edition port. Community leaves it nil, and the shared
# wire never calls it: the overlay has no constructor to fill it with.
grep -Eq '^[[:space:]]managedSCIM editionPort\[any\]$' "$PORTS" \
	|| fail "edition ports lost managedSCIM"
grep -q '^func editionPortsForBuild()' "$NOENT" \
	|| fail "the noenterprise wire no longer fills the Community edition; the nil check below would prove nothing"
if grep -q 'managedSCIM' "$NOENT"; then
	fail "Community fills managedSCIM — the seam is no longer nil"
fi

if grep -l 'thisEdition\.managedSCIM' "$(dirname "$BOOT")"/*.go | grep -qv '_test\.go$'; then
	fail "shared wire calls the managedSCIM port — overlay has no matching constructor"
fi

say "check-alc-01-s1-named-nil: CLEAN — seam named nil; motor unbuilt; boot uncalled."
exit 0
