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
set -uo pipefail
LC_ALL=C; export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
HOOK="$RAIZ/.githooks/pre-push"
[ -r "$HOOK" ] || { echo "test-gate-lock-order: ⛔ NO HE PODIDO MIRAR: no se lee $HOOK" >&2; exit 2; }

ok=0; fallos=0
caso() { # caso <nombre> <STALE> <WAIT-inyectado o vacío>
    local nombre="$1" stale="$2" wait_in="${3:-}"
    # Se extrae el bloque de defaults del hook y se evalúa AISLADO: se mide el hook real, no una copia.
    local blk; blk="$(sed -n '/^GATE_LOCK_STALE_SECS=/,/^fi$/p' "$HOOK")"
    if [ -z "$blk" ]; then
        echo "  FALLO $nombre — no encuentro el bloque de defaults en el hook (¿se renombró?)"; fallos=$((fallos+1)); return
    fi
    local out
    out="$(env OLIVARES_GATE_LOCK_STALE_SECS="$stale" \
               ${wait_in:+OLIVARES_GATE_LOCK_WAIT_SECS="$wait_in"} \
           bash -c "$blk; echo \"\$GATE_LOCK_WAIT_SECS \$GATE_LOCK_STALE_SECS\"" 2>/dev/null | tail -1)"
    local w s; w="${out%% *}"; s="${out##* }"
    if [ -n "$w" ] && [ -n "$s" ] && [ "$w" -gt "$s" ]; then
        ok=$((ok+1)); printf '  ok    %-52s WAIT=%s > STALE=%s\n' "$nombre" "$w" "$s"
    else
        fallos=$((fallos+1)); printf '  FALLO %-52s WAIT=%s STALE=%s ⇒ el robo es INALCANZABLE\n' "$nombre" "${w:-?}" "${s:-?}"
    fi
}

echo "test-gate-lock-order: la relación WAIT > STALE, no el valor"
caso "por defecto"                              10800
caso "STALE bajado por el operador"             600
caso "STALE subido por el operador"             36000
# ⛔ EL CASO QUE IMPORTA, y el que fallaba antes de este arreglo: alguien fija WAIT a mano IGUAL que
#    STALE. Sin la guarda, aquí el carril se rinde en el umbral y no roba nunca.
caso "WAIT inyectado IGUAL que STALE"           10800 10800
caso "WAIT inyectado POR DEBAJO de STALE"       10800 60

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
