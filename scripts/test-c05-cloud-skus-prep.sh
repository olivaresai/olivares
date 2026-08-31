#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(
  cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2
  pwd
)" || exit 2
CHECK="$ROOT/scripts/check-c05-cloud-skus-prep.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c05sku881prep.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0; nolook=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }
# LA TERCERA RESPUESTA, y no es cortesia: es la diferencia entre «esta limpio» y «no he podido
# mirar». El caso del typecheck necesita el tsc de commercial/license-worker, que NO existe en un
# worktree recien creado — solo en un arbol donde alguien corrio un install. Sin esta salida el
# caso imprimia FAIL culpando al MUTANTE de un binario que falta: acusaba al codigo por el
# entorno, y ponia rojo el push de cualquier carril que trabaje donde trabajamos todos.
# Va por stderr Y al RESUMEN a proposito: un caso no ejecutado contado como pasado es un verde
# silencioso, que es peor que el rojo que sustituye.
nolook() { printf 'NO-MIRADO %s\n' "$1" >&2; nolook=$((nolook + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts" \
    "$TMP/tree/commercial/license-worker/src/dodo" \
    "$TMP/tree/commercial/license-worker/src/download"
  cp "$ROOT/design/c05-cloud-skus-prep-2026-08-20.json" "$TMP/tree/design/"
  cp "$ROOT/design/C05-CLOUD-SKUS-PREP-2026-08-20.md" "$TMP/tree/design/"
  cp "$ROOT/commercial/license-worker/src/dodo/catalog.ts" \
    "$TMP/tree/commercial/license-worker/src/dodo/"
  cp "$ROOT/commercial/license-worker/src/dodo/events.ts" \
    "$ROOT/commercial/license-worker/src/dodo/provider-id.ts" \
    "$TMP/tree/commercial/license-worker/src/dodo/"
  cp "$ROOT/commercial/license-worker/src/download/sets.ts" \
    "$TMP/tree/commercial/license-worker/src/download/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c05-cloud-skus-prep.sh"
}
run() {
  local rc=0
  unset OLIVARES_ENT_DIR || true
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c05-cloud-skus-prep.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}
typecheck_catalog() {
  local rc=0
  local tsc="$ROOT/commercial/license-worker/node_modules/.bin/tsc"
  if [ ! -x "$tsc" ]; then
    printf 'no-tsc\n' >"$TMP/tsc.rc"
    printf '%s\n' "$tsc" >"$TMP/tsc.out"
    return 0
  fi
  "$tsc" \
    --noEmit \
    --target ES2022 \
    --module ESNext \
    --moduleResolution bundler \
    --lib ES2023 \
    --strict \
    --allowImportingTsExtensions \
    --verbatimModuleSyntax \
    --skipLibCheck \
    --forceConsistentCasingInFileNames \
    --noUnusedLocals \
    --noUnusedParameters \
    "$TMP/tree/commercial/license-worker/src/dodo/catalog.ts" \
    >"$TMP/tsc.out" 2>&1 || rc=$?
  echo "$rc" >"$TMP/tsc.rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "hub-safe Cloud SKUs pin is CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/c05-cloud-skus-prep-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["remainder_applied"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (remainder-applied) is killed"
else bad "remainder-applied stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/dodo/catalog.ts" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
text = p.read_text(encoding="utf-8")
old = "    if (setCodes.has(id)) {\n"
new = "    if (false && setCodes.has(id)) {\n"
if text.count(old) != 1:
    raise SystemExit("catalog boundary guard changed: executable overlap mutant could not be applied")
p.write_text(text.replace(old, new), encoding="utf-8")
PY
typecheck_catalog
if [ "$(cat "$TMP/tsc.rc")" = no-tsc ]; then
  nolook "executable overlap mutant is TypeScript-compilable: no tsc at $(cat "$TMP/tsc.out") (repair: pnpm --dir commercial/license-worker install)"
elif [ "$(cat "$TMP/tsc.rc")" = 0 ]; then ok "executable overlap mutant is TypeScript-compilable"
else bad "overlap mutant did not typecheck ($(cat "$TMP/tsc.out"))"; fi
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "executable mutant (overlapping Cloud/set-code id accepted) is killed"
else bad "overlap mutant stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/dodo/catalog.ts" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
text = p.read_text(encoding="utf-8")
old = "assertCloudChannelDisjointFromSetCodes"
if text.count(old) != 2:
    raise SystemExit("catalog boundary helper shape changed: semantic rename could not be applied")
p.write_text(text.replace(old, "enforceCatalogChannelBoundary"), encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: disjoint behavior survives helper rename"
else bad "semantic disjoint no-fire returned rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/c05-cloud-skus-prep-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["overlay_remeasured_in_this_gate"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (overlay remasure leaked) is killed"
else bad "overlay remasure stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/design/c05-cloud-skus-prep-2026-08-20.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing JSON is COULD NOT LOOK"
else bad "missing JSON rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live pin stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-c05-cloud-skus-prep selftest: $pass passed, $fail failed, $nolook NOT RUN"
if [ "$nolook" -ne 0 ]; then
  echo "check-c05-cloud-skus-prep: ⚠ $nolook caso(s) NO EJECUTADO(S) — este veredicto es PARCIAL." >&2
fi
if [ "$fail" -ne 0 ]; then exit 1; fi
