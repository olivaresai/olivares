#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c05-both-pdt-applied.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c05-both-pdt-applied.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c05pdt.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/cloud/control-plane/internal/billing" \
		"$TMP/tree/cloud/control-plane/internal/tenant"
	cp "$CHECK" "$TMP/tree/scripts/check-c05-both-pdt-applied.sh"
	chmod +x "$TMP/tree/scripts/check-c05-both-pdt-applied.sh"
	cp "$ROOT/design/c05-both-pdt-applied.json" "$TMP/tree/design/"
	cat >"$TMP/tree/design/C05-BOTH-PDT-APPLIED-2026-08-20.md" <<'EOF'
Hermetic. sandbox e2e NOT RUN. Both Cloud pdt_ apply a plan.
EOF
	cat >"$TMP/tree/cloud/control-plane/internal/billing/dodo-cloud-product-map.json" <<'EOF'
{"products":{
  "pdt_0NlE7N9AZ9CV7wNAemXAO":{"tier":"cloud-standard-m","max_seats":0,"features":[],"region":""},
  "pdt_0NlE7ZtwL8GfOeYefL7M8":{"tier":"cloud-standard-y","max_seats":0,"features":[],"region":""}
}}
EOF
	cat >"$TMP/tree/cloud/control-plane/internal/billing/dodoenvelope_test.go" <<'EOF'
pdt_0NlE7N9AZ9CV7wNAemXAO cloud-standard-m
pdt_0NlE7ZtwL8GfOeYefL7M8 cloud-standard-y
EOF
	cat >"$TMP/tree/cloud/control-plane/internal/tenant/manager_test.go" <<'EOF'
cloud-standard-m cloud-standard-y CreateUser GrantMembership
EOF
	cat >"$TMP/tree/cloud/control-plane/internal/tenant/manager.go" <<'EOF'
func inviteFirstOwner() {
  CreateUser()
  GrantMembership()
}
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c05-both-pdt-applied.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: pinned both-pdt apply is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c05-both-pdt-applied.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["sandbox_e2e_run"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: sandbox e2e claimed is FAIL"
else
	bad "sandbox e2e true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/cloud/control-plane/internal/billing/dodo-cloud-product-map.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
del d["products"]["pdt_0NlE7ZtwL8GfOeYefL7M8"]
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: dropping the annual Cloud SKU is FAIL"
else
	bad "lost annual SKU should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/cloud/control-plane/internal/billing/dodoenvelope_test.go" <<'PY'
import sys
open(sys.argv[1], "w", encoding="utf-8").write("pdt_0NlE7N9AZ9CV7wNAemXAO cloud-standard-m\n")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: webhook tests without the annual id is FAIL"
else
	bad "lost annual webhook should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'sandbox e2e passed' >>"$TMP/tree/design/C05-BOTH-PDT-APPLIED-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming sandbox e2e passed is FAIL"
else
	bad "false e2e close should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/c05-both-pdt-applied.json"
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
echo "test-c05-both-pdt-applied: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
