#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Batería de check-test-timeout-headroom.sh. Hermética: logs sintéticos, sin red y sin CI.
#
# Lo que esta batería existe para impedir es una sonda que conteste lo MISMO para cualquier
# entrada. Por eso cada respuesta tiene su control por los dos lados: el positivo exige que NOMBRE
# al paquete (no que devuelva 1 — un `exit 1` puede venir de un fallo del propio script), y el
# negativo exige LIMPIO sobre un log que sólo se diferencia del anterior en el NÚMERO.

set -uo pipefail
cd "$(dirname "$0")/.."
GATE="scripts/check-test-timeout-headroom.sh"

pasa=0; falla=0
TMP=$(mktemp -d "${TMPDIR:-/tmp}/headroom-bat-XXXXXX")
trap 'rm -rf "$TMP"' EXIT

# log <fichero> <job> <cap> <pkg:segundos>...
log() {
  local f="$1" job="$2" cap="$3"; shift 3
  printf '%s\tpaso\t2026-08-19T00:00:00.0Z go test -race -count=1 -timeout %s ./...\n' "$job" "$cap" > "$f"
  local p
  for p in "$@"; do
    printf '%s\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/%s\t%ss\n' \
      "$job" "${p%%:*}" "${p##*:}" >> "$f"
  done
}

comprueba() { # <nombre> <fichero> <rc esperado> [texto que DEBE aparecer]
  local nombre="$1" f="$2" esperado="$3" texto="${4:-}"
  local salida rc
  salida=$(bash "$GATE" "$f" 2>&1); rc=$?
  if [ "$rc" -ne "$esperado" ]; then
    echo "  ✖ $nombre — rc=$rc, esperaba $esperado"; echo "$salida" | head -3 | sed 's|^|      |'
    falla=$((falla+1)); return
  fi
  if [ -n "$texto" ] && ! grep -qF "$texto" <<<"$salida"; then
    echo "  ✖ $nombre — rc correcto pero NO nombra «$texto»"; echo "$salida" | head -4 | sed 's|^|      |'
    falla=$((falla+1)); return
  fi
  pasa=$((pasa+1))
}

# ── 1. POSITIVO: un paquete al 90% se nombra ────────────────────────────────────────────────
log "$TMP/pos" race-modules 45m "modules/lento:2430" "modules/rapido:60"
comprueba "positivo · 90% se nombra" "$TMP/pos" 1 "modules/lento"

# ── 2. NEGATIVO: el MISMO log con otro numero sale LIMPIO ───────────────────────────────────
#    Sólo cambia la duración: si esto no saliera verde, el positivo de arriba no probaría nada.
log "$TMP/neg" race-modules 45m "modules/lento:270" "modules/rapido:60"
comprueba "negativo · el mismo log con 10% sale LIMPIO" "$TMP/neg" 0 "LIMPIO"

# ── 3. FRONTERA: exactamente en el umbral CRUZA (>=, no >) ──────────────────────────────────
log "$TMP/borde" race-modules 45m "modules/justo:2025"   # 2025s = 75.0% de 2700s
comprueba "frontera · el 75,0% exacto cruza" "$TMP/borde" 1 "modules/justo"
log "$TMP/borde2" race-modules 45m "modules/casi:2024"
comprueba "frontera · un segundo por debajo NO cruza" "$TMP/borde2" 0 "LIMPIO"

# ── 4. SIN DURACIONES: no es verde, es que no he mirado ─────────────────────────────────────
: > "$TMP/vacio"
comprueba "vacío · NO_HE_PODIDO_MIRAR" "$TMP/vacio" 2 "NO_HE_PODIDO_MIRAR"
printf 'race\tpaso\t2026-08-19T00:00:00.0Z go test -timeout 45m ./...\n' > "$TMP/solocap"
comprueba "cap sin duraciones · NO_HE_PODIDO_MIRAR" "$TMP/solocap" 2 "NINGUNA duración"

# ── 5. DURACIONES SIN CAP: el caso que un gate perezoso saltaría en silencio ────────────────
printf 'race\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/modules/x\t100s\n' > "$TMP/sincap"
comprueba "duraciones sin cap · NO_HE_PODIDO_MIRAR, y dice el job" "$TMP/sincap" 2 "job: race"

# ── 6. UNIDADES del cap: s, m y h han de entenderse igual ───────────────────────────────────
log "$TMP/seg" race 2700s "modules/lento:2430"
comprueba "cap en segundos · 2700s == 45m" "$TMP/seg" 1 "modules/lento"
log "$TMP/hora" race 1h0m0s "modules/lento:3240"     # 90% de 3600s
comprueba "cap en 1h0m0s" "$TMP/hora" 1 "modules/lento"
log "$TMP/hh" race 1h "modules/comodo:1800"          # 50% de 3600s
comprueba "cap en 1h · 50% sale LIMPIO" "$TMP/hh" 0 "LIMPIO"

# ── 7. DOS JOBS, DOS CAPS: cada paquete contra el SUYO ──────────────────────────────────────
#    El mismo tiempo es cómodo bajo un cap y mortal bajo el otro. Un cap global no vería esto.
log "$TMP/a" race-modules 45m "modules/eventing:2160"      # 80% de 45m → cruza
log "$TMP/b" race-rest 90m "core/api:2160"                 # 40% de 90m → no cruza
cat "$TMP/a" "$TMP/b" > "$TMP/dos"
salida=$(bash "$GATE" "$TMP/dos" 2>&1); rc=$?
if [ "$rc" -eq 1 ] && grep -qF "modules/eventing" <<<"$salida" && ! grep -qF "core/api" <<<"$salida"; then
  pasa=$((pasa+1))
else
  echo "  ✖ dos jobs · cada uno contra su cap — rc=$rc"; echo "$salida" | head -4 | sed 's|^|      |'
  falla=$((falla+1))
fi

# ── 8. EL CAP MAS ESTRICTO DEL JOB MANDA ───────────────────────────────────────────────────
{ printf 'race\tpaso\t2026-08-19T00:00:00.0Z go test -timeout 90m ./...\n'
  printf 'race\tpaso\t2026-08-19T00:00:00.0Z go test -timeout 20m ./...\n'
  printf 'race\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/modules/y\t1000s\n'
} > "$TMP/estricto"                                   # 1000s: 18% de 90m, pero 83% de 20m
comprueba "dos caps en un job · manda el estricto" "$TMP/estricto" 1 "modules/y"

# ── 9. 'cached' no trae duración: no puede contarse como holgura ────────────────────────────
{ printf 'race\tpaso\t2026-08-19T00:00:00.0Z go test -timeout 45m ./...\n'
  printf 'race\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/modules/z\t(cached)\n'
} > "$TMP/cache"
comprueba "todo cacheado · NO_HE_PODIDO_MIRAR, no LIMPIO" "$TMP/cache" 2 "NINGUNA duración"

# ── 10. UN FAIL con duración cuenta igual que un ok ─────────────────────────────────────────
{ printf 'race\tpaso\t2026-08-19T00:00:00.0Z go test -timeout 45m ./...\n'
  printf 'race\tpaso\t2026-08-19T00:00:01.0Z FAIL\tgithub.com/olivaresai/olivares/modules/w\t2700.704s\n'
} > "$TMP/fail"
comprueba "FAIL en el tope · se nombra al 100%" "$TMP/fail" 1 "modules/w"

# ── 11. 'FAIL pkg [build failed]' no tiene duración y no debe inventarse ────────────────────
{ printf 'race\tpaso\t2026-08-19T00:00:00.0Z go test -timeout 45m ./...\n'
  printf 'race\tpaso\t2026-08-19T00:00:01.0Z FAIL\tgithub.com/olivaresai/olivares/modules/v [build failed]\n'
} > "$TMP/build"
comprueba "build failed · sin duración, NO_HE_PODIDO_MIRAR" "$TMP/build" 2 "NINGUNA duración"

# ── 12. FICHERO ILEGIBLE: no existe ≠ está limpio ──────────────────────────────────────────
comprueba "fichero inexistente · NO_HE_PODIDO_MIRAR" "$TMP/no-existe-jamas" 2 "no puedo leer"

# ── 13. UMBRAL configurable, y que de verdad se honra ───────────────────────────────────────
log "$TMP/umbral" race 45m "modules/medio:1350"       # 50%
salida=$(OLIVARES_HEADROOM_PCT=40 bash "$GATE" "$TMP/umbral" 2>&1); rc=$?
if [ "$rc" -eq 1 ] && grep -qF "modules/medio" <<<"$salida"; then pasa=$((pasa+1)); else
  echo "  ✖ umbral 40 · el 50% debería cruzar — rc=$rc"; falla=$((falla+1)); fi
salida=$(OLIVARES_HEADROOM_PCT=90 bash "$GATE" "$TMP/umbral" 2>&1); rc=$?
if [ "$rc" -eq 0 ]; then pasa=$((pasa+1)); else
  echo "  ✖ umbral 90 · el 50% NO debería cruzar — rc=$rc"; falla=$((falla+1)); fi

# ── 14. SIN ARGUMENTOS: no se pasa de largo ────────────────────────────────────────────────
salida=$(bash "$GATE" 2>&1); rc=$?
if [ "$rc" -eq 2 ]; then pasa=$((pasa+1)); else
  echo "  ✖ sin argumentos · esperaba rc=2, dio $rc"; falla=$((falla+1)); fi

# ── 15. TOPE DERIVADO: `go test` SIN -timeout no es un tope desconocido ─────────────────────
#    Es el defecto documentado de Go, 600s. Antes esto respondia NO_HE_PODIDO_MIRAR y dejaba el
#    gate sin poder dictaminar sobre ninguna corrida real: `examples` corre `go test ./...` a
#    secas. Medido el 2026-08-19 sobre la corrida 32233087960.
{
  printf 'examples\tpaso\t2026-08-19T00:00:00.0Z go test ./... && ./scripts/check-boundary.sh\n'
  printf 'examples\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/x/lento\t570s\n'
} > "$TMP/derivado"
comprueba "derivado · sin -timeout rige el defecto de Go" "$TMP/derivado" 1 "defecto de Go"
comprueba "derivado · y el 95% de 10m CRUZA" "$TMP/derivado" 1 "x/lento"

# ── 16. y el MISMO log con una duracion corta sale LIMPIO ──────────────────────────────────
#    Sin este negativo, el caso 15 pasaria aunque el tope derivado fuese cualquier otro numero.
{
  printf 'examples\tpaso\t2026-08-19T00:00:00.0Z go test ./...\n'
  printf 'examples\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/x/rapido\t60s\n'
} > "$TMP/derivado-limpio"
comprueba "derivado · 60s de 600s sale LIMPIO" "$TMP/derivado-limpio" 0 "LIMPIO"

# ── 17. POR INVOCACION, no por job: un job que MEZCLA se mide entero ───────────────────────
#    `control-plane` trae ordenes CON y SIN -timeout. Con un cap por JOB sus 252 paquetes eran
#    inmedibles. El log es secuencial, asi que cada paquete se mide contra la orden que lo
#    produjo: `viejo` va bajo 45m (270s = 10%, limpio) y `nuevo` bajo el defecto (570s = 95%).
{
  printf 'control-plane\tpaso\t2026-08-19T00:00:00.0Z go test -count=1 -timeout 45m ./...\n'
  printf 'control-plane\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/cp/viejo\t270s\n'
  printf 'control-plane\tpaso\t2026-08-19T00:00:02.0Z go test ./cloud/...\n'
  printf 'control-plane\tpaso\t2026-08-19T00:00:03.0Z ok\tgithub.com/olivaresai/olivares/cp/nuevo\t570s\n'
} > "$TMP/mezcla"
comprueba "por invocacion · el paquete del tramo SIN -timeout cruza" "$TMP/mezcla" 1 "cp/nuevo"
salida=$(bash "$GATE" "$TMP/mezcla" 2>&1)
if grep -qF "cp/viejo" <<<"$salida"; then
  echo "  x por invocacion - cp/viejo NO debe cruzar: bajo 45m son 270s = 10%"; falla=$((falla+1))
else pasa=$((pasa+1)); fi
if grep -qF "NO_HE_PODIDO_MIRAR" <<<"$salida"; then
  echo "  x por invocacion - un job que mezcla ya NO es inmedible"; falla=$((falla+1))
else pasa=$((pasa+1)); fi

# ── 17b. EL CASO QUE DISTINGUE «por invocacion» de «minimo del job» ────────────────────────
#    El 17 NO lo distinguia y se comprobo por mutacion: con las duraciones de aquel fixture las
#    dos lecturas dan el mismo veredicto, asi que pasaba con el codigo bueno Y con el malo. Aqui
#    el tramo ANCHO va DESPUES del estrecho: `tarde` son 2000s bajo 45m = 74% (LIMPIO), pero
#    bajo el minimo del job (600s, del `go test` a secas de antes) serian 333% y cruzaria. Un
#    caso que no puede fallar de las dos maneras no prueba cual de las dos rige.
{
  printf 'cp2\tpaso\t2026-08-19T00:00:00.0Z go test ./primero/...\n'
  printf 'cp2\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/cp2/pronto\t100s\n'
  printf 'cp2\tpaso\t2026-08-19T00:00:02.0Z go test -count=1 -timeout 45m ./segundo/...\n'
  printf 'cp2\tpaso\t2026-08-19T00:00:03.0Z ok\tgithub.com/olivaresai/olivares/cp2/tarde\t2000s\n'
} > "$TMP/tramos"
comprueba "tramos · el ancho va DESPUES y su paquete NO cruza" "$TMP/tramos" 0 "LIMPIO"

# ── 17c. PROSA NO ES UNA INVOCACION ────────────────────────────────────────────────────────
#    Estuvo publicado al reves durante un commit. El job `examples` imprime
#    «next: cd …/.examples-tmp/… && go test ./... && ./scripts/check-boundary.sh» como
#    INSTRUCCION AL LECTOR, y el gate derivaba de ahi un tope de 600s y lo presentaba como dato.
#    Un tope inventado es peor que ninguno, porque ninguno se declara y este se creia.
{
  printf 'examples\tpaso\t2026-08-19T00:00:00.0Z next: cd /tmp/x && go test ./... && ./scripts/check-boundary.sh\n'
  printf 'examples\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/z/prosa\t570s\n'
} > "$TMP/prosa"
comprueba "prosa · una orden CITADA no fija tope" "$TMP/prosa" 2 "examples"
salida=$(bash "$GATE" "$TMP/prosa" 2>&1)
if grep -qF "defecto de Go" <<<"$salida"; then
  echo "  x prosa - NO puede declarar un tope derivado de una linea citada"; falla=$((falla+1))
else pasa=$((pasa+1)); fi

# ── 17d. y la MISMA linea, ejecutada de verdad, SI fija tope ───────────────────────────────
#    Sin este par, el 17c pasaria con un gate que simplemente no derivara nunca.
{
  printf 'examples\tpaso\t2026-08-19T00:00:00.0Z go test ./...\n'
  printf 'examples\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/z/real\t570s\n'
} > "$TMP/prosa-no"
comprueba "prosa · la misma orden EJECUTADA si fija tope" "$TMP/prosa-no" 1 "defecto de Go"

# ── 17e. PREFIJO DE ENTORNO: `GOWORK=off go test` es una invocacion ────────────────────────
#    Es la forma que usan examples/bring-your-own-protocol/smoke.sh y build-a-connector/smoke.sh.
#    Exigir que la linea EMPIECE por `go test` sin quitar el prefijo dejaba ciegos sus paquetes.
{
  printf 'examples\tpaso\t2026-08-19T00:00:00.0Z     GOWORK=off go test ./...\n'
  printf 'examples\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/e/env\t570s\n'
} > "$TMP/envprefix"
comprueba "prefijo de entorno · GOWORK=off go test SI fija tope" "$TMP/envprefix" 1 "defecto de Go"

# ── 17f. y el prefijo NO abre la puerta a la prosa ─────────────────────────────────────────
#    La linea citada de `examples` lleva el mismo `go test` detras de texto. Quitar prefijos de
#    entorno no puede convertir «next: cd X && …» en una invocacion.
{
  printf 'examples\tpaso\t2026-08-19T00:00:00.0Z next: cd /tmp/x && GOWORK=off go test ./...\n'
  printf 'examples\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/e/citado\t570s\n'
} > "$TMP/envprosa"
comprueba "prefijo de entorno · pero CITADO sigue sin fijar tope" "$TMP/envprosa" 2 "examples"

# ── 18. CEGUERA DE VERDAD: duracion ANTES de cualquier orden visible ────────────────────────
#    No se rellena con un numero plausible. `fuzz` estaba asi hasta que scripts/fuzz-smoke.sh
#    empezo a imprimir su orden.
{
  printf 'fuzz\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/y/ciego\t100s\n'
} > "$TMP/ciego"
comprueba "ceguera real · sin orden visible responde 2" "$TMP/ciego" 2 "fuzz"


echo
echo "$pasa passed, $falla failed"
[ "$falla" -eq 0 ]
