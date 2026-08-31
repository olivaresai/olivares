#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c03-05-roster-accumulate.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c03-05-roster-accumulate.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0305.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" "$TMP/tree/modules/governance"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c03-05-roster-accumulate.sh"
	cp "$ROOT/design/c03-05-roster-accumulate.json" "$TMP/tree/design/"
	cp "$ROOT/design/C03-05-ROSTER-ACCUMULATE-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/modules/governance/roster.go" "$TMP/tree/modules/governance/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-c03-05-roster-accumulate.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live accumulate pin is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/modules/governance/roster.go" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
t = p.read_text()
old = """\t\tgraph, err := b.Provider.Snapshot(ctx)
\t\tif err != nil {
\t\t\terrs = append(errs, err)
\t\t\tcontinue
\t\t}"""
new = """\t\tgraph, err := b.Provider.Snapshot(ctx)
\t\tif err != nil {
\t\t\treturn err
\t\t}"""
if old not in t:
    raise SystemExit("live Snapshot block not found")
p.write_text(t.replace(old, new, 1))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: abort on Snapshot is FAIL"
else
	bad "abort should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c03-05-roster-accumulate.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["snapshot_gated"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: snapshot_gated true is FAIL"
else
	bad "gated flag should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'Snapshot gated' >>"$TMP/tree/design/C03-05-ROSTER-ACCUMULATE-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims Snapshot gated is FAIL"
else
	bad "gated claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/C03-05-ROSTER-ACCUMULATE-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing accumulate doc is COULD NOT LOOK"
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

echo "check-c03-05-roster-accumulate selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
