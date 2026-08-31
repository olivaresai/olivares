#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# La bateria de check-download-contract.sh.
#
# ⛔ EL CASO QUE JUSTIFICA EL GATE ES EL 3, no el 2. Cambiar el fichero versionado y ver rojo solo
# prueba que compara algo. Lo que hay que probar es que **regenera desde la FUENTE**: si se anade
# un slug a sets.ts y el gate sigue verde, el contrato exportado puede quedarse corto sin que nadie
# lo note — que es literalmente el episodio que `sets.ts:24-25` narra (17 en vez de 33, con el test
# en verde, porque comprobaba el acuerdo con su propia copia).
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
pass=0; fail=0
ok()  { printf 'ok    %-56s %s\n' "$1" "${2:-}"; pass=$((pass+1)); }
bad() { printf 'FAIL  %-56s %s\n' "$1" "${2:-}"; fail=$((fail+1)); }

W="$(mktemp -d "${TMPDIR:-/var/tmp}/dlcbat.XXXXXX")" || { echo "sin scratch"; exit 2; }
trap 'rm -rf "$W"' EXIT
D=commercial/license-worker/src/download

stage() {
	rm -rf "$W/t"; mkdir -p "$W/t/scripts" "$W/t/ci" "$W/t/$D"
	for par in \
		"scripts/render-download-contract.sh:$W/t/scripts/render-download-contract.sh" \
		"scripts/check-download-contract.sh:$W/t/scripts/check-download-contract.sh" \
		"ci/download-contract.txt:$W/t/ci/download-contract.txt" \
		"$D/artifacts.ts:$W/t/$D/artifacts.ts" \
		"$D/sets.ts:$W/t/$D/sets.ts"; do
		s="$ROOT/${par%%:*}"; d="${par#*:}"
		cp "$s" "$d" 2>/dev/null || { echo "test-check-download-contract: NO HE PODIDO MIRAR — no pude copiar $s" >&2; exit 2; }
		[ -s "$d" ] || { echo "test-check-download-contract: NO HE PODIDO MIRAR — $d quedo vacio" >&2; exit 2; }
	done
	chmod +x "$W/t/scripts/"*.sh
}
corre() { ( cd "$W/t" && OLIVARES_ROOT="$W/t" bash scripts/check-download-contract.sh >"$W/out" 2>&1 ); echo $?; }

# 1 · NO-FIRE
stage; rc="$(corre)"
[ "$rc" = 0 ] && ok "no-fire: el contrato al dia sale limpio" "rc=$rc" \
              || { bad "no-fire: esperaba 0" "rc=$rc"; sed 's/^/       /' "$W/out" | head -4; }

# 2 · el fichero VERSIONADO manipulado -> rojo
stage
sed -i 's/^artifact_basename_prefix=.*/artifact_basename_prefix=manipulado/' "$W/t/ci/download-contract.txt"
rc="$(corre)"
{ [ "$rc" = 1 ] && grep -q 'DIVERGE' "$W/out"; } \
  && ok "fichero manipulado -> rojo" "rc=$rc" \
  || { bad "fichero manipulado: esperaba 1" "rc=$rc"; sed 's/^/       /' "$W/out" | head -3; }

# 3 · ⛔ LA FUENTE cambia y el contrato no: TIENE que salir rojo.
stage
python3 - "$W/t/$D/sets.ts" <<'PY'
import sys
p = sys.argv[1]; s = open(p, encoding="utf-8").read()
a = 'export const ALLOWED_SET_SLUGS: ReadonlySet<string> = new Set([\n'
assert s.count(a) == 1, "ancla de ALLOWED_SET_SLUGS no unica"
open(p, "w", encoding="utf-8").write(s.replace(a, a + '  "biz+nuevo",\n', 1))
PY
rc="$(corre)"
{ [ "$rc" = 1 ] && grep -q 'biz+nuevo' "$W/out"; } \
  && ok "la FUENTE cambia -> rojo, y nombra el slug nuevo" "rc=$rc" \
  || { bad "la fuente cambio y el gate no lo vio: compara contra su copia" "rc=$rc"; sed 's/^/       /' "$W/out" | head -5; }

# 4 · sin la fuente -> 2, nunca 0
stage; rm -f "$W/t/$D/sets.ts"; rc="$(corre)"
[ "$rc" = 2 ] && ok "sin la fuente: 2, no un verde por vacuidad" "rc=$rc" \
              || { bad "sin la fuente: esperaba 2" "rc=$rc"; sed 's/^/       /' "$W/out" | head -3; }

# 5 · sin el contrato exportado -> 2 (el espejo no podria comprobar nada)
stage; rm -f "$W/t/ci/download-contract.txt"; rc="$(corre)"
[ "$rc" = 2 ] && ok "sin el contrato exportado: 2" "rc=$rc" \
              || { bad "sin el contrato: esperaba 2" "rc=$rc"; }

# 6 · el CUARTO hecho carga peso: si `artifactFilename` cambia de forma, es rojo.
#     Sin este caso, `artifact_filename_shape` podria estar mal derivado y nadie se enteraria
#     —es el hecho que `check-set-producer.sh` exige para certificar el nombre del tarball.
stage
python3 - "$W/t/$D/artifacts.ts" <<'PY2'
import sys
p=sys.argv[1]; s=open(p,encoding="utf-8").read()
a="${ARTIFACT_BASENAME_PREFIX}_${version}_${os}_${arch}.tar.gz"
assert s.count(a)==1, "el literal del nombre de fichero no es unico"
open(p,"w",encoding="utf-8").write(s.replace(a, a.replace(".tar.gz",".tgz"), 1))
PY2
rc="$(corre)"
{ [ "$rc" = 1 ] && grep -q 'tgz' "$W/out"; } \
  && ok "artifactFilename cambia -> rojo, y nombra la forma nueva" "rc=$rc" \
  || { bad "el nombre del tarball cambio y el gate no lo vio" "rc=$rc"; sed 's/^/       /' "$W/out" | head -5; }

# 7 y 8 · LA GUARDA DE COMPLETITUD, que es lo que hace fiable al contrato. El espejo no tiene la
#          fuente: no puede comprobar que la lista llegue ENTERA, solo creersela. Si el renderer
#          emite en silencio lo que su parser entendio, la lista corta viaja y el productor del
#          espejo construye un conjunto menos EN VERDE. Estos dos casos exigen 2, nunca un 0 corto.
stage; sed -i 's/"biz+reg",/"biz+reg2",/' "$W/t/$D/sets.ts"; rc="$(corre)"
{ [ "$rc" = 2 ] && grep -q 'declara 17' "$W/out"; } \
  && ok "un slug ilegible -> 2, no un contrato CORTO en verde" "rc=$rc" \
  || { bad "un slug ilegible tenia que dar 2 nombrando la cuenta" "rc=$rc"; sed 's/^/       /' "$W/out" | head -4; }

stage; sed -i "s/ADDON_CODES = \[\"airs\"/ADDON_CODES = ['airs'/" "$W/t/$D/sets.ts"; rc="$(corre)"
{ [ "$rc" = 2 ] && grep -q 'ADDON_CODES declara' "$W/out"; } \
  && ok "un add-on ilegible -> 2 (su tag faltaria en TODOS los builds)" "rc=$rc" \
  || { bad "un add-on ilegible tenia que dar 2" "rc=$rc"; sed 's/^/       /' "$W/out" | head -4; }

printf '\ncheck-download-contract selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
