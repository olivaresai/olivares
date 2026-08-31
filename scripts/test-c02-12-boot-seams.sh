#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c02-12-boot-seams.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c02-12-boot-seams.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0212.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" "$TMP/tree/cmd/olivares"
	cp "$CHECK" "$TMP/tree/scripts/check-c02-12-boot-seams.sh"
	chmod +x "$TMP/tree/scripts/check-c02-12-boot-seams.sh"
	cp "$ROOT/design/c02-12-boot-seams.json" "$TMP/tree/design/"
	cat >"$TMP/tree/design/C02-12-BOOT-SEAMS-2026-08-19.md" <<'EOF'
NOT CLOSED. newDurableBus still aborts boot on community.
EOF
	cat >"$TMP/tree/design/BACKLOG-COMPLETITUD-2026-08-16.md" <<'EOF'
| C02-12 | Auditar los 44 seams de wire_noenterprise.go
EOF
	cat >"$TMP/tree/design/ARTEFACTOS-POR-PACK-2026-08-08.md" <<'EOF'
preserved_on_every_lapse. ningún seam retirado por tag puede cambiar el CONTRATO DE ARRANQUE.
EOF
	# 46 constructors, one fmt.Errorf, CAEP error tuple + nil,nil.
	{
		echo 'package main'
		i=1
		while [ "$i" -le 44 ]; do
			printf 'func seam%02d() {}\n' "$i"
			i=$((i + 1))
		done
		cat <<'GO'
func newDurableBus() (any, error) {
	return nil, fmt.Errorf("community durable bus")
}
func newCAEPTransmitter() (caepTransmitter, error) {
	return nil, nil
}
GO
	} >"$TMP/tree/cmd/olivares/wire_noenterprise.go"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c02-12-boot-seams.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: pinned open audit is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-12-boot-seams.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["invariant_closed"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: invariant_closed true is FAIL"
else
	bad "invariant_closed true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-12-boot-seams.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["constructors"] = 44
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: claiming 44 constructors is FAIL"
else
	bad "constructors 44 should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-12-boot-seams.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["boot_aborting"] = []
d["boot_aborting_count"] = 0
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: hiding newDurableBus abort is FAIL"
else
	bad "empty boot_aborting should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/cmd/olivares/wire_noenterprise.go" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace("return nil, fmt.Errorf(\"community durable bus\")", "return nil, nil")
open(p, "w", encoding="utf-8").write(t)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: dropping the fmt.Errorf return is FAIL"
else
	bad "dropped fmt.Errorf should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'invariant closed' >>"$TMP/tree/design/C02-12-BOOT-SEAMS-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming invariant closed is FAIL"
else
	bad "false close should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/c02-12-boot-seams.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing JSON is LOOK (2)"
else
	bad "missing JSON should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-c02-12-boot-seams: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
