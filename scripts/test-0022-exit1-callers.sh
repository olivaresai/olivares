#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-0022-exit1-callers.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-0022-exit1-callers.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0022.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/commercial/license-worker/migrations" \
		"$TMP/tree/commercial/license-worker/src/store"
	cp "$CHECK" "$TMP/tree/scripts/check-0022-exit1-callers.sh"
	chmod +x "$TMP/tree/scripts/check-0022-exit1-callers.sh"
	cp "$ROOT/design/c0022-exit1-callers.json" "$TMP/tree/design/"
	cat >"$TMP/tree/design/ADJUDICACION-0022-SALIDA-1-CALLERS-2026-08-19.md" <<'EOF'
SALIDA 1. Published 0016/0018 win. Callers write those columns.
EOF
	cat >"$TMP/tree/commercial/license-worker/migrations/0016_dodo_cohort_barrier.sql" <<'EOF'
CREATE TABLE IF NOT EXISTS dodo_cohort_fragments (
  business_id TEXT NOT NULL
);
EOF
	cat >"$TMP/tree/commercial/license-worker/migrations/0018_dodo_atomic_issuance.sql" <<'EOF'
CREATE TABLE dodo_line_grants (
  business_id TEXT NOT NULL
);
EOF
	cat >"$TMP/tree/commercial/license-worker/src/store/db.ts" <<'EOF'
            `INSERT INTO dodo_line_grants
               (business_id, payment_id, line_key, grant_id, order_line_id, product_id,
                kind, quantity)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
        `INSERT OR IGNORE INTO dodo_cohort_fragments
           (business_id, subscription_id, event_timestamp, kind, webhook_id, payload, received_at)
         VALUES (?, ?, ?, ?, ?, ?, ?)`,
  // ---- the cohort barrier (migrations/0016) ---------------------------------------------
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-0022-exit1-callers.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: exit 1 callers CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c0022-exit1-callers.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["exit"] = 2
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: exit 2 is FAIL"
else
	bad "exit 2 should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'CREATE TABLE dodo_line_grants (x INT);' \
	>"$TMP/tree/commercial/license-worker/migrations/0022_dodo_fulfillment.sql"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: 0022 CREATE is FAIL"
else
	bad "0022 sql should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's|INSERT INTO dodo_line_grants|INSERT INTO other_grants|' \
	"$TMP/tree/commercial/license-worker/src/store/db.ts"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: drop line-grants writer is FAIL"
else
	bad "dropped writer should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's|migrations/0016|migrations/0006|' \
	"$TMP/tree/commercial/license-worker/src/store/db.ts"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: barrier attributed to 0006 is FAIL"
else
	bad "0006 comment should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/commercial/license-worker/migrations/0016_dodo_cohort_barrier.sql"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing 0016 is LOOK (2)"
else
	bad "missing 0016 should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-0022-exit1-callers: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
