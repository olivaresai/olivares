#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-alc-01-s2-managed-contract.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-alc-01-s2-managed-contract.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/alc01s2.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/core/api" "$TMP/tree/core/auth" \
		"$TMP/tree/cmd/olivares"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-alc-01-s2-managed-contract.sh"
	cp "$ROOT/design/alc-01-s2-managed-contract.json" "$TMP/tree/design/"
	cp "$ROOT/design/ALC-01-S2-MANAGED-CONTRACT-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/core/api/handlers_scim.go" "$TMP/tree/core/api/"
	cp "$ROOT/core/api/handlers_scim_groups.go" "$TMP/tree/core/api/"
	cp "$ROOT/core/api/server.go" "$TMP/tree/core/api/"
	cp "$ROOT/core/auth/scim.go" "$TMP/tree/core/auth/"
	cp "$ROOT/core/auth/federation_login.go" "$TMP/tree/core/auth/"
	cp "$ROOT/cmd/olivares/wire_noenterprise.go" "$TMP/tree/cmd/olivares/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-alc-01-s2-managed-contract.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live managed-SCIM contract is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/core/api/handlers_scim.go" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
p.write_text(p.read_text().replace(
    "func (s *Server) scimCreateUser",
    "func (s *Server) scimMintUser",
    1,
))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: inbound create renamed is FAIL"
else
	bad "renamed create should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
printf '\n_ = r.Header.Get("Idempotency-Key")\n' >>"$TMP/tree/core/api/handlers_scim.go"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: inbound Idempotency-Key is FAIL"
else
	bad "inbound header should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/alc-01-s2-managed-contract.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["motor_implemented"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: motor_implemented true is FAIL"
else
	bad "motor flag should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/alc-01-s2-managed-contract.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["verbs"] = ["create", "update"]
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: verbs dropped deprovision is FAIL"
else
	bad "short verbs should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'managed SCIM shipped' >>"$TMP/tree/design/ALC-01-S2-MANAGED-CONTRACT-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims motor shipped is FAIL"
else
	bad "shipped claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/ALC-01-S2-MANAGED-CONTRACT-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing contract doc is COULD NOT LOOK"
else
	bad "missing doc should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "check-alc-01-s2-managed-contract selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
