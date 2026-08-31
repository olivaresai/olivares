#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c02-18-oci-no-latest.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c02-18-oci-no-latest.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0218.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design"
	cp "$CHECK" "$TMP/tree/scripts/check-c02-18-oci-no-latest.sh"
	chmod +x "$TMP/tree/scripts/check-c02-18-oci-no-latest.sh"
	cp "$ROOT/design/c02-18-oci-no-latest.json" "$TMP/tree/design/"
	cat >"$TMP/tree/design/C02-18-OCI-NO-LATEST-2026-08-19.md" <<'EOF'
NOT PROVISIONED. Per set. Commercial :latest forbidden.
EOF
	cat >"$TMP/tree/.goreleaser.yaml" <<'EOF'
# NO `latest` manifest here — aliases are promotion, not build.
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c02-18-oci-no-latest.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: written constraint is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-18-oci-no-latest.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["provisioned"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: provisioned true is FAIL"
else
	bad "provisioned true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-18-oci-no-latest.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["commercial_latest_forbidden"] = False
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: latest allowed is FAIL"
else
	bad "latest allowed should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-18-oci-no-latest.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["latest_templates"] = d["latest_templates"][:2]
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: drop one latest template is FAIL"
else
	bad "dropped template should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'registry provisioned' >>"$TMP/tree/design/C02-18-OCI-NO-LATEST-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming registry provisioned is FAIL"
else
	bad "provision claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/.goreleaser.yaml"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing hub goreleaser is LOOK (2)"
else
	bad "missing goreleaser should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-c02-18-oci-no-latest: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
