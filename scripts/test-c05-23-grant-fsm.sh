#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c05-23-grant-fsm.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c05-23-grant-fsm.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0523.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/commercial/license-worker/src"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c05-23-grant-fsm.sh"
	cp "$ROOT/design/c05-23-grant-fsm.json" "$TMP/tree/design/"
	cp "$ROOT/design/C05-23-GRANT-FSM-HOLD-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/commercial/license-worker/src/index.ts" \
		"$TMP/tree/commercial/license-worker/src/"
	cp "$ROOT/commercial/license-worker/wrangler.jsonc" \
		"$TMP/tree/commercial/license-worker/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-c05-23-grant-fsm.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live grant-FSM HOLD is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
printf '\n  async scheduled(event: unknown, env: unknown, ctx: unknown) { return; },\n' \
	>>"$TMP/tree/commercial/license-worker/src/index.ts"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: planted scheduled handler is FAIL"
else
	bad "scheduled handler should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/license-worker/wrangler.jsonc" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
t = p.read_text()
# Insert a crons key after the first opening brace of the object.
p.write_text(t.replace("{", '{\n  "crons": ["0 * * * *"],', 1))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: wrangler crons is FAIL"
else
	bad "wrangler crons should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c05-23-grant-fsm.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["grant_fsm"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: grant_fsm true is FAIL"
else
	bad "grant_fsm flag should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'scheduled handler shipped' >>"$TMP/tree/design/C05-23-GRANT-FSM-HOLD-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims shipped is FAIL"
else
	bad "shipped claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/C05-23-GRANT-FSM-HOLD-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing HOLD doc is COULD NOT LOOK"
else
	bad "missing doc should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# ⛔ EL CASO QUE DA SENTIDO AL GATE DESDE 2026-08-31. El check dejó de prohibir la palabra
# «scheduled» —que era un proxy, y se rompió el día que una decisión de aterrizó la purga de
# retención— y pasó a prohibir la PROPIEDAD: que un cron mueva grants. Sin este caso, esa
# generalización no la probaría nadie, y ya nos ha pasado hoy: escribir una generalización no es
# cubrirla — hay que mutar exactamente lo que añade.
stage
python3 - "$TMP/tree/commercial/license-worker/src/index.ts" <<'PY2'
from pathlib import Path
import sys
p = Path(sys.argv[1])
t = p.read_text()
# Dentro del ÚNICO handler scheduled, una línea que toca grants.
needle = "const out = await purgeExpiredCustodyBodies(env, new Date());"
assert t.count(needle) == 1, "ancla del handler de purga no única"
p.write_text(t.replace(needle, needle + '\n    await env.DB.prepare("UPDATE grants SET paid_through = ?").bind(1).run();'))
PY2
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: a scheduled handler that TOUCHES grants is FAIL"
else
	bad "grants-touching scheduled should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# Y su contrapeso: el handler que NO es la purga tampoco pasa, aunque no toque grants.
stage
sed -i 's/purgeExpiredCustodyBodies(env, new Date())/somethingElse(env)/' \
	"$TMP/tree/commercial/license-worker/src/index.ts"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: a scheduled that is NOT the declared purge is FAIL"
else
	bad "non-purge scheduled should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "check-c05-23-grant-fsm selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
