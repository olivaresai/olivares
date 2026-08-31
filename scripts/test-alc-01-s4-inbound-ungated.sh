#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-alc-01-s4-inbound-ungated.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-alc-01-s4-inbound-ungated.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/alc01s4.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/core/api" "$TMP/tree/core/auth"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-alc-01-s4-inbound-ungated.sh"
	cp "$ROOT/design/alc-01-s4-inbound-ungated.json" "$TMP/tree/design/"
	cp "$ROOT/design/ALC-01-S4-INBOUND-UNGATED-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/core/api/handlers_scim.go" "$TMP/tree/core/api/"
	cp "$ROOT/core/api/handlers_scim_groups.go" "$TMP/tree/core/api/"
	cp "$ROOT/core/api/handlers_scim_events.go" "$TMP/tree/core/api/"
	cp "$ROOT/core/auth/scim.go" "$TMP/tree/core/auth/"
	cp "$ROOT/core/auth/scim_groups.go" "$TMP/tree/core/auth/"
	cp "$ROOT/core/auth/scim_events.go" "$TMP/tree/core/auth/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-alc-01-s4-inbound-ungated.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live inbound-ungated pin is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'addonGate.Authorize(ctx, "wire")' >>"$TMP/tree/core/api/handlers_scim.go"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: planted grant consult is FAIL"
else
	bad "grant consult should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/alc-01-s4-inbound-ungated.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["inbound_gated_by_grant"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: inbound_gated_by_grant true is FAIL"
else
	bad "gated flag should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'inbound SCIM gated' >>"$TMP/tree/design/ALC-01-S4-INBOUND-UNGATED-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims inbound gated is FAIL"
else
	bad "gated claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/ALC-01-S4-INBOUND-UNGATED-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing inbound-ungated doc is COULD NOT LOOK"
else
	bad "missing doc should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/core/api/handlers_scim.go"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing inbound handler is COULD NOT LOOK"
else
	bad "missing handler should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "check-alc-01-s4-inbound-ungated selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
