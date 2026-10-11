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
  local output rc
  output=$(bash "$GATE" "$f" 2>&1); rc=$?
  if [ "$rc" -ne "$esperado" ]; then
    echo "  ✖ $nombre — rc=$rc, expected $esperado"; echo "$output" | head -3 | sed 's|^|      |'
    falla=$((falla+1)); return
  fi
  if [ -n "$texto" ] && ! grep -qF "$texto" <<<"$output"; then
    echo "  ✖ $nombre — correct rc but does NOT name «$texto»"; echo "$output" | head -4 | sed 's|^|      |'
    falla=$((falla+1)); return
  fi
  pasa=$((pasa+1))
}

# ── 1. POSITIVO: un paquete al 90% se nombra ────────────────────────────────────────────────
log "$TMP/pos" race-modules 45m "modules/lento:2430" "modules/rapido:60"
comprueba "positive · 90% is named" "$TMP/pos" 1 "modules/lento"

# ── 2. NEGATIVO: el MISMO log con otro numero sale LIMPIO ───────────────────────────────────
#    Sólo cambia la duración: si esto no saliera verde, el positivo de arriba no probaría nada.
log "$TMP/neg" race-modules 45m "modules/lento:270" "modules/rapido:60"
comprueba "negative · same log at 10% is CLEAN" "$TMP/neg" 0 "CLEAN"

# ── 3. FRONTERA: exactamente en el umbral CRUZA (>=, no >) ──────────────────────────────────
log "$TMP/borde" race-modules 45m "modules/justo:2025"   # 2025s = 75.0% de 2700s
comprueba "boundary · exactly 75.0% crosses" "$TMP/borde" 1 "modules/justo"
log "$TMP/borde2" race-modules 45m "modules/casi:2024"
comprueba "boundary · one second below does NOT cross" "$TMP/borde2" 0 "CLEAN"

# ── 4. SIN DURACIONES: no es verde, es que no he mirado ─────────────────────────────────────
: > "$TMP/vacio"
comprueba "empty · NO_HE_PODIDO_MIRAR" "$TMP/vacio" 2 "NO_HE_PODIDO_MIRAR"
printf 'race\tpaso\t2026-08-19T00:00:00.0Z go test -timeout 45m ./...\n' > "$TMP/solocap"
comprueba "cap without durations · NO_HE_PODIDO_MIRAR" "$TMP/solocap" 2 "no package durations"

# ── 5. DURACIONES SIN CAP: el caso que un gate perezoso saltaría en silencio ────────────────
printf 'race\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/modules/x\t100s\n' > "$TMP/sincap"
comprueba "durations without a cap · NO_HE_PODIDO_MIRAR, names the job" "$TMP/sincap" 2 "job: race"

# ── 6. UNIDADES del cap: s, m y h han de entenderse igual ───────────────────────────────────
log "$TMP/seg" race 2700s "modules/lento:2430"
comprueba "cap in seconds · 2700s == 45m" "$TMP/seg" 1 "modules/lento"
log "$TMP/hora" race 1h0m0s "modules/lento:3240"     # 90% de 3600s
comprueba "cap in 1h0m0s" "$TMP/hora" 1 "modules/lento"
log "$TMP/hh" race 1h "modules/comodo:1800"          # 50% de 3600s
comprueba "cap in 1h · 50% is CLEAN" "$TMP/hh" 0 "CLEAN"

# ── 7. DOS JOBS, DOS CAPS: cada paquete contra el SUYO ──────────────────────────────────────
#    El mismo tiempo es cómodo bajo un cap y mortal bajo el otro. Un cap global no vería esto.
log "$TMP/a" race-modules 45m "modules/eventing:2160"      # 80% de 45m → cruza
log "$TMP/b" race-rest 90m "core/api:2160"                 # 40% de 90m → no cruza
cat "$TMP/a" "$TMP/b" > "$TMP/dos"
output=$(bash "$GATE" "$TMP/dos" 2>&1); rc=$?
if [ "$rc" -eq 1 ] && grep -qF "modules/eventing" <<<"$output" && ! grep -qF "core/api" <<<"$output"; then
  pasa=$((pasa+1))
else
  echo "  ✖ two jobs · each against its own cap — rc=$rc"; echo "$output" | head -4 | sed 's|^|      |'
  falla=$((falla+1))
fi

# ── 8. EL CAP MAS ESTRICTO DEL JOB MANDA ───────────────────────────────────────────────────
{ printf 'race\tpaso\t2026-08-19T00:00:00.0Z go test -timeout 90m ./...\n'
  printf 'race\tpaso\t2026-08-19T00:00:00.0Z go test -timeout 20m ./...\n'
  printf 'race\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/modules/y\t1000s\n'
} > "$TMP/estricto"                                   # 1000s: 18% de 90m, pero 83% de 20m
comprueba "two caps in one job · the stricter one applies" "$TMP/estricto" 1 "modules/y"

# ── 9. 'cached' no trae duración: no puede contarse como holgura ────────────────────────────
{ printf 'race\tpaso\t2026-08-19T00:00:00.0Z go test -timeout 45m ./...\n'
  printf 'race\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/modules/z\t(cached)\n'
} > "$TMP/cache"
comprueba "all cached · NO_HE_PODIDO_MIRAR, not CLEAN" "$TMP/cache" 2 "no package durations"

# ── 10. UN FAIL con duración cuenta igual que un ok ─────────────────────────────────────────
{ printf 'race\tpaso\t2026-08-19T00:00:00.0Z go test -timeout 45m ./...\n'
  printf 'race\tpaso\t2026-08-19T00:00:01.0Z FAIL\tgithub.com/olivaresai/olivares/modules/w\t2700.704s\n'
} > "$TMP/fail"
comprueba "FAIL at the cap · named at 100%" "$TMP/fail" 1 "modules/w"

# ── 11. 'FAIL pkg [build failed]' no tiene duración y no debe inventarse ────────────────────
{ printf 'race\tpaso\t2026-08-19T00:00:00.0Z go test -timeout 45m ./...\n'
  printf 'race\tpaso\t2026-08-19T00:00:01.0Z FAIL\tgithub.com/olivaresai/olivares/modules/v [build failed]\n'
} > "$TMP/build"
comprueba "build failed · no duration, NO_HE_PODIDO_MIRAR" "$TMP/build" 2 "no package durations"

# ── 12. FICHERO ILEGIBLE: no existe ≠ está limpio ──────────────────────────────────────────
comprueba "missing file · NO_HE_PODIDO_MIRAR" "$TMP/no-existe-jamas" 2 "cannot read"

# ── 13. UMBRAL configurable, y que de verdad se honra ───────────────────────────────────────
log "$TMP/umbral" race 45m "modules/medio:1350"       # 50%
output=$(OLIVARES_HEADROOM_PCT=40 bash "$GATE" "$TMP/umbral" 2>&1); rc=$?
if [ "$rc" -eq 1 ] && grep -qF "modules/medio" <<<"$output"; then pasa=$((pasa+1)); else
  echo "  ✖ threshold 40 · 50% should cross — rc=$rc"; falla=$((falla+1)); fi
output=$(OLIVARES_HEADROOM_PCT=90 bash "$GATE" "$TMP/umbral" 2>&1); rc=$?
if [ "$rc" -eq 0 ]; then pasa=$((pasa+1)); else
  echo "  ✖ threshold 90 · 50% should NOT cross — rc=$rc"; falla=$((falla+1)); fi

# ── 14. SIN ARGUMENTOS: no se pasa de largo ────────────────────────────────────────────────
output=$(bash "$GATE" 2>&1); rc=$?
if [ "$rc" -eq 2 ]; then pasa=$((pasa+1)); else
  echo "  ✖ no arguments · expected rc=2, got $rc"; falla=$((falla+1)); fi

# ── 15. TOPE DERIVADO: `go test` SIN -timeout no es un tope desconocido ─────────────────────
#    Es el defecto documentado de Go, 600s. Antes esto respondia NO_HE_PODIDO_MIRAR y dejaba el
#    gate sin poder dictaminar sobre ninguna corrida real: `examples` corre `go test ./...` a
#    secas. Medido el 2026-08-19 sobre la corrida 32233087960.
{
  printf 'examples\tpaso\t2026-08-19T00:00:00.0Z go test ./... && ./scripts/check-boundary.sh\n'
  printf 'examples\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/x/lento\t570s\n'
} > "$TMP/derivado"
comprueba "derived · without -timeout, the Go default applies" "$TMP/derivado" 1 "Go default"
comprueba "derived · 95% of 10m CROSSES" "$TMP/derivado" 1 "x/lento"

# ── 16. y el MISMO log con una duracion corta sale LIMPIO ──────────────────────────────────
#    Sin este negativo, el caso 15 pasaria aunque el tope derivado fuese cualquier otro numero.
{
  printf 'examples\tpaso\t2026-08-19T00:00:00.0Z go test ./...\n'
  printf 'examples\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/x/rapido\t60s\n'
} > "$TMP/derivado-limpio"
comprueba "derived · 60s of 600s is CLEAN" "$TMP/derivado-limpio" 0 "CLEAN"

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
comprueba "per invocation · package in the section WITHOUT -timeout crosses" "$TMP/mezcla" 1 "cp/nuevo"
output=$(bash "$GATE" "$TMP/mezcla" 2>&1)
if grep -qF "cp/viejo" <<<"$output"; then
  echo "  x per invocation - cp/viejo must NOT cross: under 45m, 270s = 10%"; falla=$((falla+1))
else pasa=$((pasa+1)); fi
if grep -qF "NO_HE_PODIDO_MIRAR" <<<"$output"; then
  echo "  x per invocation - a mixed job is NO LONGER unmeasurable"; falla=$((falla+1))
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
comprueba "sections · wider cap comes AFTER and its package does NOT cross" "$TMP/tramos" 0 "CLEAN"

# ── 17c. PROSA NO ES UNA INVOCACION ────────────────────────────────────────────────────────
#    Estuvo publicado al reves durante un commit. El job `examples` imprime
#    «next: cd …/.examples-tmp/… && go test ./... && ./scripts/check-boundary.sh» como
#    INSTRUCCION AL LECTOR, y el gate derivaba de ahi un tope de 600s y lo presentaba como dato.
#    Un tope inventado es peor que ninguno, porque ninguno se declara y este se creia.
{
  printf 'examples\tpaso\t2026-08-19T00:00:00.0Z next: cd /tmp/x && go test ./... && ./scripts/check-boundary.sh\n'
  printf 'examples\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/z/prosa\t570s\n'
} > "$TMP/prosa"
comprueba "prose · a QUOTED command does not set a cap" "$TMP/prosa" 2 "examples"
output=$(bash "$GATE" "$TMP/prosa" 2>&1)
if grep -qF "Go default" <<<"$output"; then
  echo "  x prose - CANNOT declare a cap derived from a quoted line"; falla=$((falla+1))
else pasa=$((pasa+1)); fi

# ── 17d. y la MISMA linea, ejecutada de verdad, SI fija tope ───────────────────────────────
#    Sin este par, el 17c pasaria con un gate que simplemente no derivara nunca.
{
  printf 'examples\tpaso\t2026-08-19T00:00:00.0Z go test ./...\n'
  printf 'examples\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/z/real\t570s\n'
} > "$TMP/prosa-no"
comprueba "prose · the same EXECUTED command does set a cap" "$TMP/prosa-no" 1 "Go default"

# ── 17e. PREFIJO DE ENTORNO: `GOWORK=off go test` es una invocacion ────────────────────────
#    Es la forma que usan examples/bring-your-own-protocol/smoke.sh y build-a-connector/smoke.sh.
#    Exigir que la linea EMPIECE por `go test` sin quitar el prefijo dejaba ciegos sus paquetes.
{
  printf 'examples\tpaso\t2026-08-19T00:00:00.0Z     GOWORK=off go test ./...\n'
  printf 'examples\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/e/env\t570s\n'
} > "$TMP/envprefix"
comprueba "environment prefix · GOWORK=off go test DOES set a cap" "$TMP/envprefix" 1 "Go default"

# ── 17f. y el prefijo NO abre la puerta a la prosa ─────────────────────────────────────────
#    La linea citada de `examples` lleva el mismo `go test` detras de texto. Quitar prefijos de
#    entorno no puede convertir «next: cd X && …» en una invocacion.
{
  printf 'examples\tpaso\t2026-08-19T00:00:00.0Z next: cd /tmp/x && GOWORK=off go test ./...\n'
  printf 'examples\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/e/citado\t570s\n'
} > "$TMP/envprosa"
comprueba "environment prefix · QUOTED command still does not set a cap" "$TMP/envprosa" 2 "examples"

# ── 18. CEGUERA DE VERDAD: duracion ANTES de cualquier orden visible ────────────────────────
#    No se rellena con un numero plausible. `fuzz` estaba asi hasta que scripts/fuzz-smoke.sh
#    empezo a imprimir su orden.
{
  printf 'fuzz\tpaso\t2026-08-19T00:00:01.0Z ok\tgithub.com/olivaresai/olivares/y/ciego\t100s\n'
} > "$TMP/ciego"
comprueba "unavailable evidence · without a visible command, returns 2" "$TMP/ciego" 2 "fuzz"


# ── MODO --raw: el log que un job deja con `tee`, SIN prefijo de gh ──────────────────────────
# Anadido el 2026-08-26. El defecto es real y estaba medido: sobre un log crudo de `race-rest` con
# tres paquetes al 100 % de su tope, el modo posicional contesta «NO HE PODIDO MIRAR: el log no
# trae NINGUNA duracion». Causa: separa el prefijo `<job>\t<paso>\t` BUSCANDO tabuladores, y en un
# log crudo el PRIMER tabulador es de Go (`FAIL\tpaquete\t7284.106s`), asi que se come el marcador.

# crudo <fichero> <cap> <pkg:segundos>...   — sin columnas de job/paso, con los tabuladores de Go
crudo() {
  local f="$1" cap="$2"; shift 2
  printf '2026-08-26T00:00:00.0Z go test -race -count=1 -timeout %s ./...\n' "$cap" > "$f"
  local p
  for p in "$@"; do
    printf '2026-08-26T00:00:01.0Z FAIL\tgithub.com/olivaresai/olivares/%s\t%ss\n' \
      "${p%%:*}" "${p##*:}" >> "$f"
  done
}

comprueba_raw() { # <nombre> <job> <fichero> <rc esperado> [texto]
  local nombre="$1" job="$2" f="$3" esperado="$4" texto="${5:-}"
  local output rc
  output=$(bash "$GATE" --raw "$job" "$f" 2>&1); rc=$?
  if [ "$rc" -ne "$esperado" ]; then
    echo "  ✖ $nombre — rc=$rc, expected $esperado"; echo "$output" | head -3 | sed 's|^|      |'
    falla=$((falla+1)); return
  fi
  if [ -n "$texto" ] && ! grep -qF "$texto" <<<"$output"; then
    echo "  ✖ $nombre — correct rc but does NOT name «$texto»"; echo "$output" | head -4 | sed 's|^|      |'
    falla=$((falla+1)); return
  fi
  pasa=$((pasa+1))
}

crudo "$TMP/crudo" 45m core/api:2700.5 core/auth:2700.3 core/otro:600
comprueba_raw "raw · --raw finds durations missed by positional mode" \
  race-rest "$TMP/crudo" 1 "core/api"

# ⛔ CONTROL DE MUTACION: el MISMO fichero SIN --raw tiene que seguir ciego. Sin este caso, el
# anterior no prueba que sea el modo lo que cambia — podria ser el fichero.
comprueba "raw WITHOUT --raw remains unmeasurable (mutation control)" "$TMP/crudo" 2 "no package durations"

# El job se NOMBRA, no se deduce: con --raw el nombre viene de la orden.
comprueba_raw "raw · verdict names the supplied job" race-rest "$TMP/crudo" 1 "45 min"

# Un crudo con holgura de sobra sale limpio, y el conteo lo dice.
crudo "$TMP/crudo_ok" 45m core/api:600 core/auth:300
comprueba_raw "raw with ample headroom is CLEAN" race-rest "$TMP/crudo_ok" 0 ""

# Fichero que no existe: 2, no 0.
comprueba_raw "--raw on a missing file means CANNOT INSPECT" race-rest "$TMP/no-existe" 2 ""

# Sin argumentos suficientes: 2 y lo dice.
output=$(bash "$GATE" --raw 2>&1); rc=$?
if [ "$rc" -eq 2 ]; then pasa=$((pasa+1)); else echo "  ✖ --raw without arguments — rc=$rc, expected 2"; falla=$((falla+1)); fi

# ── LOCALE: el veredicto no depende del idioma de la caja ──────────────────────────────────
# Medido el 2026-09-05: con LC_ALL=es_ES.UTF-8 heredado, `printf %.1f` rechazaba el «10.0» que el
# awk (ya en C) le pasaba, y siete casos de arriba salian 1 con LIMPIO escrito. Se ejerce el gate
# bajo un locale instalado con COMA decimal (se comprueba, no se supone: un locale que no esta
# instalado cae a C en silencio y el caso no mediria nada). Si no hay ninguno, se DICE y no cuenta
# como pasado.
omitido=0
LOC_COMA=""
while IFS= read -r cand; do
  [ -n "$cand" ] || continue
  # `printf %.1f 1` escribe «1,0» solo si el locale esta instalado Y usa coma: un locale ausente
  # cae a C y escribe «1.0», y entonces el caso no mediria nada.
  if [ "$(LC_ALL="$cand" bash -c 'printf "%.1f" 1' 2>/dev/null)" = "1,0" ]; then LOC_COMA="$cand"; break; fi
done < <(locale -a 2>/dev/null | grep -iE '^(es_ES|de_DE|fr_FR|it_IT|pt_BR|ru_RU)\.(utf8|UTF-8)$' || true)
if [ -n "$LOC_COMA" ]; then
  output=$(LC_ALL="$LOC_COMA" bash "$GATE" "$TMP/neg" 2>&1); rc=$?
  if [ "$rc" -eq 0 ] && grep -qF "CLEAN" <<<"$output" && grep -qF "10.0%" <<<"$output" \
     && ! grep -qF "invalid number" <<<"$output"; then
    pasa=$((pasa+1))
  else
    echo "  ✖ locale · under LC_ALL=$LOC_COMA, CLEAN at 10% must exit 0 with a decimal point — rc=$rc"
    echo "$output" | head -3 | sed 's|^|      |'; falla=$((falla+1))
  fi
  # y el positivo sigue nombrando al paquete bajo ese locale: la cura no ha aflojado el rojo
  output=$(LC_ALL="$LOC_COMA" bash "$GATE" "$TMP/pos" 2>&1); rc=$?
  if [ "$rc" -eq 1 ] && grep -qF "modules/lento" <<<"$output"; then pasa=$((pasa+1)); else
    echo "  ✖ locale · under LC_ALL=$LOC_COMA, 90% is still named — rc=$rc"; falla=$((falla+1)); fi
else
  omitido=1
  echo "  SKIP locale · no decimal-comma locale installed: locale case was NOT exercised"
fi

echo
if [ "$omitido" -eq 1 ]; then echo "$pasa passed, $falla failed, 1 skipped (locale)"; else echo "$pasa passed, $falla failed"; fi
[ "$falla" -eq 0 ]
