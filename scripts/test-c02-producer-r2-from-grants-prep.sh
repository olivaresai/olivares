#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c02-producer-r2-from-grants-prep.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c02pkprep.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
# export-closure: hub-only scripts/publish-enterprise-artifacts.sh — el productor de
# artefactos enterprise no viaja en el arbol publicado, y este test lo COPIA a su arbol
# de pruebas (abajo, en stage). Sin el, el `cp` muere por `set -e` y el arbol publicado
# reporta un rojo que no es suyo. Se nombra la razon y se sale 0, que es lo que pide
# check-export-closure.sh:1245 para este caso.
if [ ! -r "$ROOT/scripts/publish-enterprise-artifacts.sh" ]; then
  printf 'SKIP %s: scripts/publish-enterprise-artifacts.sh es hub-only y no esta en este arbol\n' \
    "$(basename "${BASH_SOURCE[0]}")"
  exit 0
fi

. "$ROOT/scripts/lib/c02-download-contract-tests.sh"

pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts" \
    "$TMP/tree/commercial/license-worker/src/download" \
    "$TMP/tree/commercial/license-worker/test"
  cp "$ROOT/design/c02-producer-r2-from-grants-prep-2026-08-20.json" "$TMP/tree/design/"
  cp "$ROOT/design/C02-PRODUCER-R2-FROM-GRANTS-PREP-2026-08-20.md" "$TMP/tree/design/"
  cp -R "$ROOT/commercial/license-worker/src" "$TMP/tree/commercial/license-worker/"
  cp "$ROOT/commercial/license-worker/package.json" "$TMP/tree/commercial/license-worker/"
  mkdir -p "$TMP/tree/scripts/lib"
  cp "$ROOT/scripts/lib/c02-download-contract.mjs" "$TMP/tree/scripts/lib/"
  cp "$ROOT/commercial/license-worker/test/download.test.ts" \
    "$TMP/tree/commercial/license-worker/test/"
  # Guarda EN EL SITIO DE LLAMADA, no solo la salida temprana de arriba: si alguien
  # retira aquella, este cp quedaria desnudo en el arbol publicado. `if`, no `&&`:
  # una lista que acaba en `&&` con el lado izquierdo falso devuelve 1 y `set -e` mata.
  if [ -f "$ROOT/scripts/publish-enterprise-artifacts.sh" ]; then
    cp "$ROOT/scripts/publish-enterprise-artifacts.sh" "$TMP/tree/scripts/"
    # Y EL CONTRATO QUE ESE PUBLICADOR LEE: desde `f42b3442b` la clave del artefacto vive en
    # `keys.artifact` del contrato generado, no en el guion. Un banco que copia el sujeto y no
    # lo que el sujeto LEE no prueba el arbol, prueba el banco.
    if [ -f "$ROOT/commercial/license-worker/contracts/publisher.gen.json" ]; then
      mkdir -p "$TMP/tree/commercial/license-worker/contracts"
      cp "$ROOT/commercial/license-worker/contracts/publisher.gen.json" \
        "$TMP/tree/commercial/license-worker/contracts/"
    fi
  fi
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c02-producer-r2-from-grants-prep.sh"
}
run() {
  local rc=0
  unset OLIVARES_ENT_DIR || true
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c02-producer-r2-from-grants-prep.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "hub-safe producer-R2 HOLD is CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/c02-producer-r2-from-grants-prep-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["binary_key_includes_set"] = False
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (binary_key_includes_set false) is killed"
else bad "binary_key false stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
printf '\nbytes are real\n' >> \
  "$TMP/tree/design/C02-PRODUCER-R2-FROM-GRANTS-PREP-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (doc claims bytes real) is killed"
else bad "doc closed stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/c02-producer-r2-from-grants-prep-2026-08-20.json" <<'PY'
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
python3 - "$TMP/tree/commercial/license-worker/src/download/artifacts.ts" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
text = p.read_text(encoding="utf-8")
assert text.count("export function artifactKey(version: string, os: string, arch: string, set: string)") == 1
p.write_text(
    text.replace(
        "export function artifactKey(version: string, os: string, arch: string, set: string)",
        "export function artifactKey(version: string, os: string, arch: string)",
    ),
    encoding="utf-8",
)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -Fq "artifactKey arity is 3, want 4" "$TMP/err"; then ok "mutant (artifactKey 3-arg) is killed"
else bad "3-arg stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/design/c02-producer-r2-from-grants-prep-2026-08-20.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing JSON is COULD NOT LOOK"
else bad "missing JSON rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live pin stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

c02_download_contract_mutants
c02_download_contract_overrides \
  OLIVARES_C02PKP_JSON=design/c02-producer-r2-from-grants-prep-2026-08-20.json \
  OLIVARES_C02PKP_DOC=design/C02-PRODUCER-R2-FROM-GRANTS-PREP-2026-08-20.md \
  OLIVARES_C02PKP_ART=commercial/license-worker/src/download/artifacts.ts \
  OLIVARES_C02PKP_SETS=commercial/license-worker/src/download/sets.ts \
  OLIVARES_C02PKP_GATE=commercial/license-worker/src/download/gate.ts \
  OLIVARES_C02PKP_PUB=scripts/publish-enterprise-artifacts.sh \
  OLIVARES_C02_CONTRATO=commercial/license-worker/contracts/publisher.gen.json \
  OLIVARES_C02PKP_TEST=commercial/license-worker/test/download.test.ts

echo "check-c02-producer-r2-from-grants-prep selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
