#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ALC-01-S1 hub half: newManagedSCIM exists and is nil; boot does not call it.
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
BOOT="${OLIVARES_ALC01S_BOOT:-cmd/olivares/wire.go}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$NOENT" ] || cannot "missing noenterprise wire"
[ -f "$BOOT" ] || cannot "missing shared wire"

grep -q 'HOLD on the motor' "$DOC" || fail "$DOC lost HOLD on the motor"
grep -q 'Seam named' "$DOC" || fail "$DOC lost seam named"
if grep -qiE 'managed SCIM shipped|ALC-01 complete|invented /v1/managed' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
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

grep -E '^func newManagedSCIM\(\) any \{' "$NOENT" >/dev/null \
	|| fail "noenterprise wire lost newManagedSCIM"
# The body of the named seam must still be nil — not a client.
python3 - "$NOENT" <<'PY' || fail "newManagedSCIM is no longer the nil seam"
import re, sys
text = open(sys.argv[1], encoding="utf-8").read()
m = re.search(r"func newManagedSCIM\(\) any \{([^}]*)\}", text, re.S)
if not m:
    raise SystemExit("function body not found")
body = m.group(1)
if "return nil" not in body:
    raise SystemExit("body does not return nil")
if re.search(r"return [^n]", body):
    raise SystemExit("body returns something other than nil")
PY

if grep -E 'newManagedSCIM\(' "$BOOT" >/dev/null; then
	fail "shared wire calls newManagedSCIM — overlay has no matching symbol"
fi

say "check-alc-01-s1-named-nil: CLEAN — seam named nil; motor unbuilt; boot uncalled."
exit 0
