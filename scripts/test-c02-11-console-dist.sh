#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c02-11-console-dist.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c02-11-console-dist.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0211.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c02-11-console-dist.sh"
	cp "$ROOT/design/c02-11-console-dist-hold.json" "$TMP/tree/design/"
	cp "$ROOT/design/C02-11-CONSOLE-DIST-HOLD-2026-08-20.md" "$TMP/tree/design/"
}

run() {
	local rc=0
	# No overlay tree in the fixture: JSON/doc half only.
	OLIVARES_ROOT="$TMP/tree" OLIVARES_ENT_DIR="" \
		bash "$TMP/tree/scripts/check-c02-11-console-dist.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live HOLD is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-11-console-dist-hold.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
data = json.loads(p.read_text())
data["executed"] = True
p.write_text(json.dumps(data))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: executed=true is FAIL"
else
	bad "executed=true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-11-console-dist-hold.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
data = json.loads(p.read_text())
data["land_key_before_producer"] = False
p.write_text(json.dumps(data))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: denying the half-stitch is FAIL"
else
	bad "land_key_before_producer false should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-11-console-dist-hold.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
data = json.loads(p.read_text())
data["one_web_build_on_overlay_main"] = False
p.write_text(json.dumps(data))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: overlay main no longer one web build is FAIL"
else
	bad "one_web_build false should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's/HOLD/console split shipped/' \
	"$TMP/tree/design/C02-11-CONSOLE-DIST-HOLD-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims split shipped is FAIL"
else
	bad "claimed split should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/C02-11-CONSOLE-DIST-HOLD-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing HOLD doc is COULD NOT LOOK"
else
	bad "missing doc should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
# Overlay fixture: two web builds must fail when ENT_DIR is set.
mkdir -p "$TMP/ent"
printf '%s\n' 'before:' \
	'    - bash -c "pnpm --dir web run build"' \
	'    - bash -c "pnpm --dir web run build"' \
	>"$TMP/ent/.goreleaser.yaml"
OLIVARES_ROOT="$TMP/tree" OLIVARES_ENT_DIR="$TMP/ent" \
	bash "$TMP/tree/scripts/check-c02-11-console-dist.sh" \
	>"$TMP/out" 2>"$TMP/err" || true
if grep -q 'web builds' "$TMP/err"; then
	ok "firing: two overlay web builds is FAIL"
else
	bad "two web builds should FAIL ($(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "check-c02-11-console-dist selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
