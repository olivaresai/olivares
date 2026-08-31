#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c13-03-dependson.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c13-03-dependson.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c1303.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree" "$TMP/ent"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/ent/cmd-overlay/olivares" "$TMP/ent/enterprise/activation"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c13-03-dependson.sh"
	cp "$ROOT/design/c13-03-dependson.json" "$TMP/tree/design/"
	cp "$ROOT/design/C13-03-DEPENDSON-HOLD-2026-08-20.md" "$TMP/tree/design/"
	cat >"$TMP/ent/enterprise/activation/catalog.go" <<'EOF'
package activation

var Catalog = []struct {
	Key       string
	DependsOn string
}{
	{Key: "legalhold-reconcile", DependsOn: "audit-worm-archive"},
}
EOF
	cat >"$TMP/ent/cmd-overlay/olivares/activation_apply_enterprise.go" <<'EOF'
package olivares

func promoteActivation(a *Activation) error {
	a.State = ActivationActive
	return nil
}
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" OLIVARES_ENT_DIR="$TMP/ent" \
		bash "$TMP/tree/scripts/check-c13-03-dependson.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live DependsOn HOLD is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/ent/cmd-overlay/olivares/activation_apply_enterprise.go" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
t = p.read_text()
idx = t.find("func promoteActivation(")
if idx < 0:
    raise SystemExit("promoteActivation not found")
brace = t.find("{", idx)
if brace < 0:
    raise SystemExit("no body")
insert = '\n\tif a.DependsOn != "" {\n\t\treturn nil, fmt.Errorf("blocked")\n\t}'
p.write_text(t[: brace + 1] + insert + t[brace + 1 :])
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: DependsOn consult in promote is FAIL"
else
	bad "DependsOn consult should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c13-03-dependson.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["depends_on_honoured"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: depends_on_honoured true is FAIL"
else
	bad "honoured flag should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'DependsOn honoured on overlay main' >>"$TMP/tree/design/C13-03-DEPENDSON-HOLD-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims honoured is FAIL"
else
	bad "honoured claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/C13-03-DEPENDSON-HOLD-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing HOLD doc is COULD NOT LOOK"
else
	bad "missing doc should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
OLIVARES_ROOT="$TMP/tree" OLIVARES_ENT_DIR="" \
	bash "$TMP/tree/scripts/check-c13-03-dependson.sh" \
	>"$TMP/out" 2>"$TMP/err" || true
if grep -q 'COULD NOT LOOK' "$TMP/err"; then
	ok "unset overlay dir is COULD NOT LOOK"
else
	bad "unset overlay dir should LOOK 2 ($(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "check-c13-03-dependson selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
