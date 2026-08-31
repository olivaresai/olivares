#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c0022-exit1-remeasure.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c0022-exit1-remeasure.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0022.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

# rc alone does not say a case fired on ITS OWN assertion: a partly-applied mutant can go red on
# a different one and still look green here. Every firing case below names the phrase it must
# produce, measured against each mutant on 2026-08-27.
fired() { # phrase label
	if [ "$(cat "$TMP/rc")" = 1 ] && grep -qF "$1" "$TMP/err"; then
		ok "$2"
	else
		bad "$2 (rc=$(cat "$TMP/rc") err=$(cat "$TMP/err"))"
	fi
}

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/commercial/license-worker/src/store" \
		"$TMP/tree/commercial/license-worker/migrations"
	cp "$CHECK" "$TMP/tree/scripts/check-c0022-exit1-remeasure.sh"
	chmod +x "$TMP/tree/scripts/check-c0022-exit1-remeasure.sh"
	cp "$ROOT/design/c0022-exit1-remeasure.json" "$TMP/tree/design/"
	cat >"$TMP/tree/design/C0022-EXIT1-REMEASURE-2026-08-20.md" <<'EOF'
Salida 1. schema NOT TOUCHED.
EOF
	cat >"$TMP/tree/design/ADJUDICACION-SLOTS-D1-LICENSE-WORKER-2026-08-17.md" <<'EOF'
### 5.1 Veredicto del carril de comercio · 2026-08-18 — **salida 1**
EOF
	# The fake carries the SHAPE the gate reads - a closed template literal with its column
	# list and its VALUES - at no particular position, because position is no longer contract.
	python3 - "$TMP/tree/commercial/license-worker/src/store/db.ts" <<'PY'
import sys
open(sys.argv[1], "w", encoding="utf-8").write("""// synthetic db.ts for the battery
        this.db
          .prepare(
            `INSERT INTO dodo_line_grants
               (business_id, payment_id, line_key, grant_id, order_line_id, product_id,
                kind, quantity)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
          )
    const res = await this.db
      .prepare(
        `INSERT OR IGNORE INTO dodo_cohort_fragments
           (business_id, subscription_id, event_timestamp, kind, webhook_id, payload, received_at)
         VALUES (?, ?, ?, ?, ?, ?, ?)`,
      )
""")
PY
	: >"$TMP/tree/commercial/license-worker/migrations/0016_dodo_cohort_barrier.sql"
	: >"$TMP/tree/commercial/license-worker/migrations/0018_dodo_atomic_issuance.sql"
	: >"$TMP/tree/commercial/license-worker/migrations/0029_dodo_serial_purpose.sql"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c0022-exit1-remeasure.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: pinned Exit 1 is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c0022-exit1-remeasure.json" <<'PY'
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
: >"$TMP/tree/commercial/license-worker/migrations/0022_dodo_cohort_fragments.sql"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: adding 0022 SQL is FAIL"
else
	bad "0022 SQL should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/store/db.ts" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace("INSERT INTO dodo_line_grants", "INSERT INTO elsewhere")
open(p, "w", encoding="utf-8").write(t)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: losing the 0018 INSERT is FAIL"
else
	bad "lost INSERT should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'CREATE TABLE IF NOT EXISTS dodo_cohort_fragments in 0022' >>"$TMP/tree/design/C0022-EXIT1-REMEASURE-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming 0022 creates the published tables is FAIL"
else
	bad "false schema claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/c0022-exit1-remeasure.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing JSON is LOOK (2)"
else
	bad "missing JSON should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# ⛔ THE DEFECT THAT ALREADY HAPPENED, first: on 2026-08-27 a change ~70 lines above the
# line_grants INSERT moved it, and the gate accused that change of breaking a contract about
# COLUMNS. Moving the statement without touching it must be CLEAN.
stage
python3 - "$TMP/tree/commercial/license-worker/src/store/db.ts" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read()
open(p, "w", encoding="utf-8").write("// unrelated edit above\n" * 500 + t)
PY
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: moving the INSERTs 500 lines down stays CLEAN"
else
	bad "a moved-but-unchanged INSERT should stay CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/store/db.ts" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read()
i = t.index("        this.db")
open(p, "w", encoding="utf-8").write(t[:i] + t[i:t.index("    const res")] + t[i:])
PY
run
fired 'must contain exactly one '\''INSERT INTO dodo_line_grants'\'', found 2' "firing: a SECOND writer of dodo_line_grants is FAIL"

stage
python3 - "$TMP/tree/commercial/license-worker/src/store/db.ts" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read()
t = t.replace("                kind, quantity)", "                kind, quantity, license_id)")
t = t.replace("VALUES (?, ?, ?, ?, ?, ?, ?, ?)", "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)")
open(p, "w", encoding="utf-8").write(t)
PY
run
fired 'grew the in-flight 0006 column license_id' "firing: line_grants growing the in-flight license_id is FAIL"

stage
python3 - "$TMP/tree/commercial/license-worker/src/store/db.ts" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read()
t = t.replace("                kind, quantity)", "                kind, quantity, note)")
t = t.replace("VALUES (?, ?, ?, ?, ?, ?, ?, ?)", "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)")
open(p, "w", encoding="utf-8").write(t)
PY
run
fired 'names 9 columns, the lote pinned 8' "firing: a NINTH column the lote never pinned is FAIL"

stage
python3 - "$TMP/tree/commercial/license-worker/src/store/db.ts" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read()
open(p, "w", encoding="utf-8").write(
    t.replace("VALUES (?, ?, ?, ?, ?, ?, ?)`", "VALUES (?, ?, ?, ?, ?, ?)`")
)
PY
run
fired 'names 7 columns and binds 6 values' "firing: fragments binding one value short of its columns is FAIL"

stage
python3 - "$TMP/tree/design/c0022-exit1-remeasure.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["line_grants_insert"] = 733
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
fired 'line_grants_insert must stay "derived"' "firing: re-pinning a LINE NUMBER in the census is FAIL"

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-c0022-exit1-remeasure: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
