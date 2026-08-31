#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c03-28-public-prefix.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c03-28-public-prefix.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0328.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design"
	cp "$CHECK" "$TMP/tree/scripts/check-c03-28-public-prefix.sh"
	chmod +x "$TMP/tree/scripts/check-c03-28-public-prefix.sh"
	cp "$ROOT/design/c03-28-public-prefix.json" "$TMP/tree/design/"
	cat >"$TMP/tree/design/C03-28-PUBLIC-PREFIX-2026-08-20.md" <<'EOF'
NOT EXECUTED. NXDOMAIN. 404. 403. No anonymous listing on a resolving host.
EOF
	cat >"$TMP/tree/design/BACKLOG-COMPLETITUD-2026-08-16.md" <<'EOF'
| C03-28 | inspect public enterprise/ prefixes
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" OLIVARES_C0328_LIVE="" \
		bash "$TMP/tree/scripts/check-c03-28-public-prefix.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

flip() {
	python3 - "$TMP/tree/design/c03-28-public-prefix.json" "$1" "$2" <<'PY'
import json, sys
p, key, raw = sys.argv[1], sys.argv[2], sys.argv[3]
d = json.load(open(p, encoding="utf-8"))
if raw == "true":
    d[key] = True
elif raw == "false":
    d[key] = False
elif raw.isdigit():
    d[key] = int(raw)
else:
    d[key] = raw
json.dump(d, open(p, "w", encoding="utf-8"))
PY
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: pinned probe is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
flip public_listing_found true
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: public listing found is FAIL"
else
	bad "public listing found should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
flip listed_private_r2 true
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: claiming private R2 listed is FAIL"
else
	bad "private R2 listed should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
flip worker_enterprise_prefix_status 200
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: enterprise/ 200 is FAIL"
else
	bad "enterprise/ 200 should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
flip download_no_token_status 200
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: download without token 200 is FAIL"
else
	bad "download 200 should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
flip r2_registry_dns RESOLVES
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: registry DNS resolves is FAIL"
else
	bad "registry resolves should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/c03-28-public-prefix.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing JSON is LOOK (2)"
else
	bad "missing JSON should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" OLIVARES_C0328_LIVE="" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

if [ "${OLIVARES_C0328_LIVE_TEST:-}" = "1" ]; then
	if OLIVARES_ROOT="$ROOT" OLIVARES_C0328_LIVE=1 bash "$CHECK" >/dev/null 2>"$TMP/err"; then
		ok "no-fire: live HTTPS remasure stays CLEAN"
	else
		bad "live HTTPS remasure went RED ($(cat "$TMP/err"))"
	fi
fi

echo
echo "test-c03-28-public-prefix: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
