#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-gate-lock-order.sh — la batería de D-1/L-02: el ROBO se evalúa ANTES de rendirse.
#
# ⛔ QUÉ PRUEBA, y por qué la propiedad es de ORDEN y no de valor. El mutex del gate pesado promete
# recuperar un candado abandonado: si su edad supera STALE, se roba. Con los valores enviados
# (WAIT == STALE == 10800) el carril se rendía EXACTAMENTE en el umbral, así que la comparación del
# robo —que exige `age > STALE`— **nunca llegaba a ser cierta antes del abandono**. La promesa
# estaba escrita y era inalcanzable, que es peor que no prometerla: alguien confía en ella.
#
# No se prueba «WAIT vale 12600». Se prueba la RELACIÓN: WAIT > STALE, siempre, incluso cuando
# alguien fija WAIT a mano por debajo. Un test del valor pasaría igual con la relación rota.
#
# ⛔ Y LA RELACIÓN SOLA NO BASTA. La guarda CORRIGE un WAIT ≤ STALE, o sea restaura esa misma
# relación. Un mutante que pincha la derivación a 12600 sigue sacando WAIT > STALE en todas las
# filas viejas: 10800 y 600 quedan por debajo de 12600 (la guarda ni habla), y a 36000 la guarda
# sube a 37800. El oráculo es el stderr de la guarda, no un parser del gancho: derivación viva ⇒
# silencio; WAIT explícito igual o por debajo ⇒ corrige y lo DICE.
set -uo pipefail
LC_ALL=C; export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
HOOK="$RAIZ/.githooks/pre-push"
# Sujeto de prueba: sin perilla no se puede apuntar a un gancho MUTADO, y un mutante que no se
# aplica se lee como un mutante que murió. Falta o ilegible ⇒ 2, nunca verde.
SUT="${OLIVARES_GATE_LOCK_HOOK:-$HOOK}"
[ -f "$SUT" ] && [ -r "$SUT" ] || {
    echo "test-gate-lock-order: ⛔ NO HE PODIDO MIRAR: no se lee $SUT" >&2
    exit 2
}

ok=0; fallos=0
ERR=""
TMPERR="$(mktemp "${TMPDIR:-/tmp}/glo.XXXXXX")" || {
    echo "test-gate-lock-order: ⛔ NO HE PODIDO MIRAR: no pude crear el fichero de captura" >&2
    exit 2
}
trap 'rm -f "$TMPERR"' EXIT

caso() { # caso <nombre> <STALE> <WAIT-inyectado o vacío>
    local nombre="$1" stale="$2" wait_in="${3:-}"
    # Se extrae el bloque de defaults del hook y se evalúa AISLADO: se mide el hook real, no una copia.
    local blk
    if ! blk="$(sed -n '/^GATE_LOCK_STALE_SECS=/,/^fi$/p' "$SUT")" || [ -z "$blk" ]; then
        echo "test-gate-lock-order: NO HE PODIDO MIRAR: missing or unreadable defaults block" >&2
        exit 2
    fi
    local out
    # Default cases must not inherit an operator override from the test caller.
    if ! out="$(env -u OLIVARES_GATE_LOCK_WAIT_SECS OLIVARES_GATE_LOCK_STALE_SECS="$stale" \
               ${wait_in:+OLIVARES_GATE_LOCK_WAIT_SECS="$wait_in"} \
           bash -euo pipefail -c "$blk; echo \"\$GATE_LOCK_WAIT_SECS \$GATE_LOCK_STALE_SECS\"" 2>"$TMPERR")"; then
        echo "test-gate-lock-order: NO HE PODIDO MIRAR: defaults block did not execute" >&2
        exit 2
    fi
    if ! ERR="$(cat "$TMPERR")"; then
        echo "test-gate-lock-order: NO HE PODIDO MIRAR: guard diagnostic could not be read" >&2
        exit 2
    fi
    local w s; w="${out%% *}"; s="${out##* }"
    if [ -n "$w" ] && [ -n "$s" ] && [ "$w" -gt "$s" ]; then
        ok=$((ok+1)); printf '  ok    %-52s WAIT=%s > STALE=%s\n' "$nombre" "$w" "$s"
    else
        fallos=$((fallos+1)); printf '  FALLO %-52s WAIT=%s STALE=%s ⇒ el robo es INALCANZABLE\n' "$nombre" "${w:-?}" "${s:-?}"
    fi
}

guarda_callada() { # <nombre> — con la derivación viva, la guarda no debe tener nada que corregir
    case "$ERR" in
        *"Elevando WAIT"*|*"<= STALE"*)
            fallos=$((fallos+1))
            printf '  FALLO %-52s la guarda DISPARO: el WAIT no se deriva, se corrige\n' "$1" ;;
        *) ok=$((ok+1)); printf '  ok    %-52s la guarda no tuvo nada que corregir\n' "$1" ;;
    esac
}
guarda_hablo() { # <nombre> — la dirección contraria: cuando SÍ corrige, tiene que DECIRLO
    case "$ERR" in
        *"Elevando WAIT"*) ok=$((ok+1)); printf '  ok    %-52s la guarda corrigio Y LO DIJO\n' "$1" ;;
        *) fallos=$((fallos+1))
           printf '  FALLO %-52s corrigio en SILENCIO: la promesa rota no se ve\n' "$1" ;;
    esac
}

echo "test-gate-lock-order: la relación WAIT > STALE, no el valor"
caso "por defecto"                              10800
caso "STALE bajado por el operador"             600
caso "STALE subido por el operador"             36000
# ⛔ EL CASO QUE IMPORTA, y el que fallaba antes de este arreglo: alguien fija WAIT a mano IGUAL que
#    STALE. Sin la guarda, aquí el carril se rinde en el umbral y no roba nunca.
caso "WAIT inyectado IGUAL que STALE"           10800 10800
guarda_hablo "igual: al corregirlo lo DICE"
caso "WAIT inyectado POR DEBAJO de STALE"       10800 60
guarda_hablo "y al corregirlo lo DICE"

# ⛔⛔ EL AGUJERO QUE ESTA BATERIA TENIA, Y POR QUE `WAIT > STALE` NO BASTA. La guarda del gancho
#    CORRIGE un WAIT por debajo de STALE, o sea **restaura la relacion que los casos de arriba
#    miden**: un mutante que sustituya la DERIVACION por un numero fijo pasa TODAS esas filas. Con
#    `WAIT=12600` a pelo: STALE 10800 -> 12600 ✓, 600 -> 12600 ✓, y con 36000 la guarda dispara y lo
#    sube a 37800 ✓. Cinco verdes sobre un gancho cuya derivacion ya no existe.
#
#    KERNEL24 lo dejo escrito como «hace falta un oraculo EXTERNO». **No hace falta: el oraculo es
#    la propia guarda**, que avisa cuando corrige — y esta bateria lo tiraba a `/dev/null`. Lo que
#    separa «derivado» de «fijado» no es el valor ni la relacion: es que con un STALE que el
#    operador SUBE, la guarda no tenga NADA que corregir.
caso "STALE subido: la derivacion lo sigue"     36000
guarda_callada "y la guarda NO tuvo que intervenir"
caso "STALE muy alto: la derivacion lo sigue"   86400
guarda_callada "y tampoco aqui"

# CONTROL NEGATIVO: la batería tiene que poder FALLAR. Se comprueba que un bloque con la relación
# rota a propósito NO pasa — sin esto, «5 ok» no demuestra nada.
roto="$(printf 'GATE_LOCK_STALE_SECS=100\nGATE_LOCK_WAIT_SECS=100\n')"
r="$(bash -c "$roto; echo \"\$GATE_LOCK_WAIT_SECS \$GATE_LOCK_STALE_SECS\"")"
if [ "${r%% *}" -gt "${r##* }" ]; then
    echo "  FALLO control negativo — un bloque con la relación ROTA pasó el criterio"; fallos=$((fallos+1))
else
    ok=$((ok+1)); echo "  ok    control negativo: la relación rota NO pasa"
fi

echo "test-gate-lock-order: $ok pasan, $fallos fallan"
[ "$fallos" -eq 0 ] || exit 1
exit 0
