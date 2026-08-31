#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# T3-A / pg-roles-cloud — testigo de la degradacion honesta (NOT APPLICABLE) de #2155.
#
# QUE MIDE, y por que existe. La mainline VIAJA en el export del espejo preprod, y alli el paso
# «provision cloud control-plane roles» moria con `psql: error: cloud/control-plane/deploy/
# cloud-control-roles.sql: No such file or directory`. La curacion del export retira el MODULO
# entero, y no puede no hacerlo: el fichero lleva cabecera `LicenseRef-Olivares-Commercial` y la
# frontera de licencia es regla dura. Un arbol publico sin roles de control-plane no es un fallo.
#
# LA DISTINCION QUE ESTA BATERIA PROTEGE, y es la razon de que existan tres casos y no dos: una
# guarda `[ ! -f el.sql ]` daria PARTIAL tambien cuando el modulo SI esta y alguien borro o
# renombro el guion — taparia un defecto real con la excusa del export. Por eso la guarda mira el
# DIRECTORIO, y por eso el caso C exige que con el modulo presente y el fichero ausente el paso
# SIGA MURIENDO. Un mutante que cambia la guarda a `-f` pasa los casos A y B y muere en el C: ese
# mutante es el punto entero del fichero.
#
# El bloque `run:` no se transcribe: se EXTRAE del workflow y se ejecuta. Una copia a mano mide la
# copia.
#
# Contrato de salida: 0 limpio · 1 hallazgo · 2 NO HE PODIDO MIRAR.
set -u

ROOT=$(cd "$(dirname "$0")/.." && pwd)

# export-closure: hub-only cloud/control-plane/deploy/cloud-control-roles.sql — el modulo cloud/ no viaja al export
# Sin el modulo no hay sujeto que comprobar. La respuesta correcta es la TERCERA del
# canon —«no he podido mirar»—, no un verde y no el error crudo con el que muere hoy.
# ⛔ Y ANTES DEL «no he podido mirar», LA TERCERA RESPUESTA QUE FALTABA: en el arbol
# PUBLICADO ese fichero no esta AUSENTE POR ERROR — esta CURADO FUERA, a proposito. Un rc=2
# alli es correcto como «no he mirado» y es ruido como veredicto: la pata no tiene sujeto y
# nunca lo tendra. Se distingue con el clasificador de hub-leg.sh —firma del generador MAS
# ausencia de todo camino hub-only—, no con un fichero-marcador suelto, porque un marcador a
# pelo es una contraseña que cualquier copia teclea. Misma plantilla que
# check-int-12-no-land.sh:37 y check-gate-parity.sh:346.
if [ ! -f "$ROOT"/cloud/control-plane/deploy/cloud-control-roles.sql ] \
   && [ "$(bash "$ROOT/scripts/hub-leg.sh" --classify --root "$ROOT" 2>/dev/null)" = "public" ]; then
	printf '%s\n' "test-cloud-roles-partial: SCOPED — public export; cloud/control-plane is curated out."
	printf '%s\n' "  El modulo no viaja, asi que aqui no hay degradacion PARTIAL que medir. En el hub SI se mide."
	exit 0
fi
if [ ! -f "$ROOT"/cloud/control-plane/deploy/cloud-control-roles.sql ]; then
	printf '%s\n' "test-cloud-roles-partial: COULD NOT LOOK — cloud/control-plane/deploy/cloud-control-roles.sql is not in this tree" >&2
	exit 2
fi
WF="$ROOT/.github/workflows/mainline-ci.yml"
WORK=$(mktemp -d "${TMPDIR:-/tmp}/cloud-roles.XXXXXX") || { echo "no pude crear el area"; exit 2; }
trap 'rm -rf "$WORK"' EXIT

pass=0; fail=0
ok(){ printf 'ok    %s\n' "$1"; pass=$((pass+1)); }
no(){ printf 'FAIL  %s\n' "$1"; fail=$((fail+1)); }
cannot(){ printf 'NO HE PODIDO MIRAR: %s\n' "$1"; exit 2; }

command -v python3 >/dev/null 2>&1 || cannot "sin python3 no leo el YAML"
PEEK="$ROOT/scripts/lib/ci-yaml-peek.py"
[ -r "$PEEK" ] || cannot "falta scripts/lib/ci-yaml-peek.py, que es como leo estos YAML"
# Sin PyYAML a proposito: ver la cabecera de ci-yaml-peek.py — el runner de este job es
# autoalojado y nada en el arbol demuestra que la biblioteca se alcance alli.

step_field() { # <fichero> <id> <campo>  ; rc 3 = el paso no esta
  python3 "$PEEK" step-field "$1" control-plane "$2" "$3"
}
notice_if() { # el `if:` del paso de aviso, buscado por nombre (no tiene id)
  python3 "$PEEK" step-if-byname "$1" control-plane 'NOT APPLICABLE notice'
}

# ---------------------------------------------------------------- banco de pruebas

mk_stubs() {
  local b="$1/stubs"; mkdir -p "$b" || return 1
  cat > "$b/psql" <<'STUB'
#!/bin/sh
# Devuelve "1" a la consulta de control y sale 0 a todo lo demas, SALVO -f sobre un fichero que
# no existe: ahi imita a psql, que es justo el fallo que se esta gobernando.
prev=""
for a in "$@"; do
  case "$prev" in -f) [ -f "$a" ] || { echo "psql: error: $a: No such file or directory" >&2; exit 1; };; esac
  case "$a" in -tAc) echo 1;; esac
  prev="$a"
done
exit 0
STUB
  printf '#!/bin/sh\necho deadbeefdeadbeefdeadbeefdeadbeef\n' > "$b/openssl"
  chmod +x "$b/psql" "$b/openssl" || return 1
  printf '%s' "$b"
}

run_case() { # <etiqueta-dir> <crear-modulo 0|1> <crear-sql 0|1> <bloque>
  local d; d=$(mktemp -d "$WORK/case.XXXXXX") || return 9
  if [ "$2" = 1 ]; then
    mkdir -p "$d/cloud/control-plane/deploy"
    [ "$3" = 1 ] && printf -- '-- roles\n' > "$d/cloud/control-plane/deploy/cloud-control-roles.sql"
  fi
  local stubs; stubs=$(mk_stubs "$d") || return 9
  mkdir -p "$d/rt"
  : > "$d/env"; : > "$d/out"; : > "$d/sum"
  ( cd "$d" \
    && PATH="$stubs:$PATH" PGHOSTPORT=127.0.0.1:5432 RUNNER_TEMP="$d/rt" \
       GITHUB_ENV="$d/env" GITHUB_OUTPUT="$d/out" GITHUB_STEP_SUMMARY="$d/sum" \
       bash "$4" ) > "$d/stdout" 2>&1
  CASE_RC=$?; CASE_DIR=$d
  return 0
}

B="$WORK/block.sh"
if ! step_field "$WF" pg-roles-cloud run > "$B" 2>"$WORK/e"; then
  cannot "no encuentro el paso pg-roles-cloud ($(head -1 "$WORK/e"))"
fi
[ -s "$B" ] || cannot "el bloque run: de pg-roles-cloud salio vacio"

if bash -n "$B" 2>"$WORK/n"; then ok "el bloque run: extraido es shell valido (bash -n)"
else no "el bloque no pasa bash -n: $(head -1 "$WORK/n")"; fi

# --- caso A: el modulo NO esta (arbol publico) -> PARTIAL y rc 0
run_case A 0 0 "$B" || cannot "no pude montar el caso A"
if [ "$CASE_RC" = 0 ]; then ok "A: sin cloud/control-plane el paso NO rompe (rc 0)"
else no "A: sin el modulo el paso sale rc $CASE_RC: $(head -2 "$CASE_DIR/stdout")"; fi
if command grep -qF "cloud-control-roles: NOT APPLICABLE" "$CASE_DIR/stdout" && command grep -qF "cloud/control-plane" "$CASE_DIR/stdout"; then
  ok "A: imprime el NOT APPLICABLE de #2155, con su sujeto nombrado"
else no "A: no imprime el mensaje literal: $(head -2 "$CASE_DIR/stdout")"; fi
if command grep -q 'provisioned=false' "$CASE_DIR/out"; then ok "A: deja el marcador provisioned=false para los consumidores"
else no "A: no deja marcador: los pasos siguientes no pueden saber que fue PARTIAL"; fi
if command grep -q 'NOT APPLICABLE' "$CASE_DIR/sum"; then ok "A: y lo repite en el resumen del job (se dice tambien al final)"
else no "A: el resumen del job no repite el NOT APPLICABLE"; fi
if ! command grep -q 'DATABASE_TENANT_URL' "$CASE_DIR/env"; then ok "A: no finge DSNs que no existen"
else no "A: escribio DSNs de capacidad sin haber creado ningun rol"; fi

# --- caso B: modulo y fichero presentes -> camino normal
run_case B 1 1 "$B" || cannot "no pude montar el caso B"
if [ "$CASE_RC" = 0 ]; then ok "B: con el modulo presente sigue el camino normal (rc 0)"
else no "B: el camino normal se rompio (rc $CASE_RC): $(tail -2 "$CASE_DIR/stdout")"; fi
if command grep -q 'provisioned=true' "$CASE_DIR/out"; then ok "B: marca provisioned=true"
else no "B: no marca provisioned=true"; fi
if command grep -q 'DATABASE_TENANT_URL' "$CASE_DIR/env" && command grep -q 'DATABASE_IDEMPOTENCY_URL' "$CASE_DIR/env"; then
  ok "B: escribe las DSN de capacidad (el trabajo real sigue haciendose)"
else no "B: el camino normal ya no escribe las DSN"; fi
if ! command grep -q 'NOT APPLICABLE' "$CASE_DIR/stdout"; then ok "B: y NO dice NOT APPLICABLE cuando no lo es"
else no "B: dice NOT APPLICABLE con el modulo presente"; fi

# --- caso C: modulo presente y fichero AUSENTE -> el defecto real se sigue viendo
run_case C 1 0 "$B" || cannot "no pude montar el caso C"
if [ "$CASE_RC" != 0 ]; then ok "C: con el modulo presente y el guion ausente el paso SIGUE muriendo"
else no "C: un guion borrado dentro del hub se tapa como si fuera el export (rc 0)"; fi
if ! command grep -q 'NOT APPLICABLE' "$CASE_DIR/stdout"; then ok "C: y no lo llama NOT APPLICABLE (no es un arbol publico)"
else no "C: llama NOT APPLICABLE a un defecto real"; fi

# ---------------------------------------------------------------- el cableado de los consumidores

IF_SUITE=$(step_field "$WF" test-cloud-norace if) || cannot "no encuentro el paso test-cloud-norace"
case "$IF_SUITE" in
  *"steps.pg-roles-cloud.outputs.provisioned == 'true'"*)
    ok "el consumidor (test:cloud:norace) solo corre si los roles se provisionaron" ;;
  *) no "el consumidor no mira el marcador: correria sin DSN ($IF_SUITE)" ;;
esac
IF_NOTICE=$(notice_if "$WF") || cannot "no encuentro el paso de aviso NOT APPLICABLE"
case "$IF_NOTICE" in
  *"provisioned == 'false'"*) ok "y existe un aviso que dice CON PALABRAS que se salto y por que" ;;
  *) no "el aviso no esta atado al marcador: el salto seria mudo ($IF_NOTICE)" ;;
esac

# ---------------------------------------------------------------- mutantes

mut_file(){ python3 - "$WF" "$WORK/mut.yml" "$1" <<'PY'
import sys
s=open(sys.argv[1],encoding='utf-8').read()
kind=sys.argv[3]
if kind=='sin-guarda':
    i=s.index('          if [ ! -d cloud/control-plane ]; then')
    j=s.index('          echo "provisioned=true" >> "$GITHUB_OUTPUT"')
    s=s[:i]+s[j:]
elif kind=='por-fichero':
    s=s.replace('if [ ! -d cloud/control-plane ]; then',
                'if [ ! -f cloud/control-plane/deploy/cloud-control-roles.sql ]; then',1)
elif kind=='sin-exit0':
    s=s.replace('            exit 0\n          fi\n          echo "provisioned=true"',
                '            exit 1\n          fi\n          echo "provisioned=true"',1)
elif kind=='consumidor-suelto':
    s=s.replace("        if: ${{ success() && steps.pg-roles-cloud.outputs.provisioned == 'true' }}\n",'',1)
open(sys.argv[2],'w',encoding='utf-8').write(s)
PY
}

mutant_dies(){ # <etiqueta> <kind>
  mut_file "$2" || { printf 'MUERTO %s (la mutacion ni siquiera aplica)\n' "$1"; return 0; }
  local MB="$WORK/mutblock.sh"
  if ! step_field "$WORK/mut.yml" pg-roles-cloud run > "$MB" 2>/dev/null; then
    printf 'MUERTO %s (el paso deja de ser legible)\n' "$1"; return 0; fi
  case "$2" in
    sin-guarda|sin-exit0)
      run_case M 0 0 "$MB"; [ "$CASE_RC" = 0 ] || { printf 'MUERTO %s (caso A deja de dar rc 0)\n' "$1"; return 0; } ;;
    por-fichero)
      run_case M 1 0 "$MB"; [ "$CASE_RC" != 0 ] || { printf 'MUERTO %s (caso C deja de morir)\n' "$1"; return 0; } ;;
    consumidor-suelto)
      local i; i=$(step_field "$WORK/mut.yml" test-cloud-norace if 2>/dev/null)
      case "$i" in *"provisioned == 'true'"*) ;; *) printf 'MUERTO %s (el consumidor pierde su guarda)\n' "$1"; return 0;; esac ;;
  esac
  printf 'SOBREVIVE %s\n' "$1"; return 1
}

for m in "M1 guarda borrada:sin-guarda" \
         "M2 guarda por FICHERO en vez de por DIRECTORIO:por-fichero" \
         "M3 NOT APPLICABLE que rompe igual:sin-exit0" \
         "M4 consumidor sin guarda:consumidor-suelto"; do
  lab=${m%%:*}; kind=${m##*:}
  if mutant_dies "$lab" "$kind"; then ok "$lab: el mutante muere"; else no "$lab SOBREVIVE"; fi
done

# CONTROL sobre el propio guion: si mira un workflow sin el paso, dice que no puede mirar.
printf 'jobs:\n  control-plane:\n    steps:\n      - name: nada\n' > "$WORK/vacio.yml"
step_field "$WORK/vacio.yml" pg-roles-cloud run >/dev/null 2>&1
if [ $? -eq 3 ]; then ok "CONTROL: sin el paso, el extractor dice que no puede mirar"
else no "CONTROL: la ausencia del paso no se distingue de un pase"; fi

printf '\ntest-cloud-roles-partial: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
