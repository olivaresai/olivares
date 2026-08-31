#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-eco-16-exit-restore.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-eco-16-exit-restore.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/eco16.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" "$TMP/tree/cloud"
	cp "$CHECK" "$TMP/tree/scripts/check-eco-16-exit-restore.sh"
	chmod +x "$TMP/tree/scripts/check-eco-16-exit-restore.sh"
	cat >"$TMP/tree/design/eco-16-exit-restore.json" <<'EOF'
{
  "lote": "ECO-16",
  "restored": false,
  "format": "olivares.cloud-export.v1",
  "restore_target": "self_hosted.business",
  "read_export_window_days": 30,
  "destructive_auto_delete": false,
  "format_in_cloud_go": false,
  "u_f": "UNKNOWN",
  "u_d": "UNKNOWN"
}
EOF
	cat >"$TMP/tree/design/ECO-16-EXIT-RESTORE-2026-08-19.md" <<'EOF'
NOT RESTORED. Format sourced. No cloud/ exporter.
EOF
	cat >"$TMP/tree/design/PRICING-CANON.md" <<'EOF'
    format: olivares.cloud-export.v1
    restore_target: self_hosted.business
    read_export_window_days: 30
    destructive_auto_delete: false
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-eco-16-exit-restore.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: not restored, format sourced is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-16-exit-restore.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["restored"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: restored true is FAIL"
else
	bad "restored true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-16-exit-restore.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["u_f"] = "0.15"
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: filling U_f is FAIL"
else
	bad "U_f fill should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'restore succeeded' >>"$TMP/tree/design/ECO-16-EXIT-RESTORE-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming restore is FAIL"
else
	bad "restore claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/PRICING-CANON.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing canon is LOOK (2)"
else
	bad "missing canon should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo
echo "test-eco-16-exit-restore: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
