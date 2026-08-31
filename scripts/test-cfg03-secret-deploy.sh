#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-cfg03-secret-deploy.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-cfg03-secret-deploy.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/cfg03s.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/commercial/license-worker"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-cfg03-secret-deploy.sh"
	cp "$ROOT/design/cfg-03-secret-deploy.json" "$TMP/tree/design/"
	cp "$ROOT/design/CFG-03-SECRET-DEPLOY-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/commercial/license-worker/wrangler.jsonc" \
		"$TMP/tree/commercial/license-worker/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-cfg03-secret-deploy.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live secret-deploy pin is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/license-worker/wrangler.jsonc" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
t = p.read_text()
# ⛔ EL ANCLA ES UN PATRON, NO UN LITERAL, y la razon esta medida: aqui habia
# `'"production": {\\n      "vars": {'` — los dos `{` SEGUIDOS— y el bloque gano una clave
# `"triggers"` en medio (mas comentarios JSONC). El literal dejo de casar y este banco
# empezo a salir `production vars block not found`. Hizo BIEN en gritar: un inyector que no
# encuentra donde inyectar no puede acreditar nada. Pero el arreglo es que el ancla tolere
# lo que un JSONC legitimo puede tener entre esas dos claves.
#
# El salto permite UN nivel de anidamiento (`{...}` sin anidar dentro) para cruzar el objeto
# `"triggers"`, y es NO CODICIOSO, asi que no puede alcanzar el `"vars"` de otro entorno.
import re as _re
_anc = _re.compile(r'"production":\s*\{(?:[^{}]|\{[^{}]*\})*?"vars":\s*\{', _re.S)
_m = _anc.search(t)
if _m is None:
    raise SystemExit("production vars block not found")
needle = _m.group(0)
repl = needle + '\n        "PORTAL_SESSION_SECRET": "",'
p.write_text(t.replace(needle, repl, 1))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: PORTAL_SESSION_SECRET as a var is FAIL"
else
	bad "secret-as-var should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/cfg-03-secret-deploy.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["live_secret_list"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: live_secret_list true is FAIL"
else
	bad "live_secret_list true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'C-16 complete' >>"$TMP/tree/design/CFG-03-SECRET-DEPLOY-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims C-16 complete is FAIL"
else
	bad "C-16 complete claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/CFG-03-SECRET-DEPLOY-2026-08-20.md" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
p.write_text(p.read_text().replace("version nobody deploys", "version already live"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: lost panel-secret fact is FAIL"
else
	bad "lost fact should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/CFG-03-SECRET-DEPLOY-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing secret-deploy doc is COULD NOT LOOK"
else
	bad "missing doc should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/commercial/license-worker/wrangler.jsonc"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing wrangler config is COULD NOT LOOK"
else
	bad "missing wrangler should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "check-cfg03-secret-deploy selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
