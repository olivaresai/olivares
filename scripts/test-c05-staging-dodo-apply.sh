#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for C05 staging Dodo apply. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# export-closure: hub-only cloud/staging/docker-compose.yml — el modulo cloud/ no viaja al export
# Sin el modulo no hay sujeto que comprobar. La respuesta correcta es la TERCERA del
# canon —«no he podido mirar»—, no un verde y no el error crudo con el que muere hoy.
if [ ! -f "$ROOT"/cloud/staging/docker-compose.yml ]; then
	printf '%s\n' "test-c05-staging-dodo-apply: COULD NOT LOOK — cloud/staging/docker-compose.yml is not in this tree" >&2
	exit 2
fi
CHECK="$ROOT/scripts/check-c05-staging-dodo-apply.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c05apply.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" "$TMP/tree/cloud/staging"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c05-staging-dodo-apply.sh"
	cp "$ROOT/design/C05-STAGING-DODO-APPLY-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/cloud/staging/docker-compose.yml" "$TMP/tree/cloud/staging/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c05-staging-dodo-apply.sh" \
		>/dev/null 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return "$rc"
}

stage
if run; then ok "live compose is CLEAN"
else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
sed -i '/COMMERCE_PROVIDER:/d' "$TMP/tree/cloud/staging/docker-compose.yml"
if run; then bad "compose without COMMERCE_PROVIDER stayed CLEAN"
else ok "mutant (no COMMERCE_PROVIDER) is killed"; fi

stage
sed -i '/CLOUD_CP_API_KEY:/d' "$TMP/tree/cloud/staging/docker-compose.yml"
if run; then bad "compose without CLOUD_CP_API_KEY stayed CLEAN"
else ok "mutant (no CLOUD_CP_API_KEY) is killed"; fi

stage
sed -i 's/pdt_0NlE7N9AZ9CV7wNAemXAO/prod_staging_pro/' \
	"$TMP/tree/cloud/staging/docker-compose.yml"
sed -i 's/cloud-standard-m/pro/' "$TMP/tree/cloud/staging/docker-compose.yml"
if run; then bad "Polar monthly substitute stayed CLEAN"
else ok "mutant (monthly Cloud SKU replaced) is killed"; fi

stage
python3 - "$TMP/tree/cloud/staging/docker-compose.yml" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
s = p.read_text()
old = '{"products":{"pdt_0NlE7N9AZ9CV7wNAemXAO":{"tier":"cloud-standard-m","max_seats":0,"features":[],"region":""},"pdt_0NlE7ZtwL8GfOeYefL7M8":{"tier":"cloud-standard-y","max_seats":0,"features":[],"region":""}}}'
new = '{"products":{"prod_staging_pro":{"tier":"pro","max_seats":5,"features":["sso"],"region":""}}}'
if old not in s:
    raise SystemExit('default map not found')
p.write_text(s.replace(old, new, 1))
PY
if run; then bad "Polar-only default map stayed CLEAN"
else ok "mutant (Polar-only default map) is killed"; fi

stage
rm -f "$TMP/tree/design/C05-STAGING-DODO-APPLY-2026-08-20.md"
if run; then bad "missing prep doc stayed CLEAN"
else
	if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing prep doc is COULD NOT LOOK"
	else bad "missing doc should be 2 ($(cat "$TMP/err"))"; fi
fi

stage
if run; then ok "no-fire: live compose stays CLEAN"
else bad "restored live should be CLEAN ($(cat "$TMP/err"))"; fi

echo "check-c05-staging-dodo-apply selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
