#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-cli-coverage.sh — a repository gate. ¿Cuántos comandos del CLI están documentados?
#
# ⛔ POR QUÉ EXISTE. Medido el 2026-08-15: la referencia pública documenta **4 de 42 comandos** y
# **7 de 311 tokens** de configuración. Y el hueco no se descubrió por un gate: se descubrió con un
# barrido manual. El patrón de esta casa, medido una y otra vez esta semana, es exacto:
#
#   toda métrica que un gate vigila cuadra; toda métrica que no vigila nadie ha derivado.
#
# Hay gate de cobertura de MÓDULOS y no lo había de comandos, de variables, de vistas ni de frescura
# de capturas. Éste cierra el primero.
#
# ⛔ Y NO ENUMERA: deriva el denominador del ÁRBOL DE COMANDOS. Un gate que lleva su propia lista de
# miembros caduca en silencio — es la forma de gate que este repositorio ha encontrado rota más
# veces (el censo de rutas, el mapa canon↔paquete, el allowlist por ruta, y el propio gate de
# versión que no recorría `docs/*.md`).
#
# Salida: 0 cobertura >= umbral · 1 por debajo · 2 NO HE PODIDO MIRAR (nunca es un verde).
set -uo pipefail
LC_ALL=C; export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || { echo "check-cli-coverage: ⛔ NO HE PODIDO MIRAR: no existe $RAIZ" >&2; exit 2; }

UMBRAL="${OLIVARES_CLI_DOC_FLOOR:-0}"   # suelo de trinquete: no baja de lo ya conseguido
REF="docs-site/src/content/docs/reference"

[ -d "$REF" ] || { echo "check-cli-coverage: ⛔ NO HE PODIDO MIRAR: no existe $REF" >&2; exit 2; }

# ── Denominador: los comandos que el binario DECLARA, no una lista escrita a mano ─────
# Cobra declara cada comando con `Use:` dentro de una `&cobra.Command{...}`. Se toma la primera
# palabra, que es el verbo.
CMDS="$(git grep -ho 'Use:[[:space:]]*"[a-z][a-z0-9-]*' -- 'cmd/olivares/*.go' 2>/dev/null \
        | sed 's/.*"//' | sort -u | grep -v '^$')"
N_CMD="$(printf '%s\n' "$CMDS" | grep -c . || true)"

# CONTROL POSITIVO: si el árbol de comandos sale vacío, la sonda no mide y NADA se aprueba.
if [ "${N_CMD:-0}" -lt 5 ]; then
    echo "check-cli-coverage: ⛔ NO HE PODIDO MIRAR: el árbol de comandos dio ${N_CMD:-0} entradas." >&2
    echo "                    Un denominador vacío haría que cualquier numerador pareciera cobertura total." >&2
    exit 2
fi

# ── Numerador: cuáles aparecen en la referencia pública ──────────────────────────────
DOC=0; FALTAN=""
while IFS= read -r c; do
    [ -z "$c" ] && continue
    # ⛔ EL PATRON ADMITE LA RUTA DEL SUBCOMANDO. Exigir `olivares <cmd>` pegado pierde todo lo que se
    # documenta como `olivares sources export` o `olivares kb label`: el comando ESTA documentado y el
    # gate lo contaba como ausente. Medido el 2026-08-18: 8 con el patron pegado, 12 admitiendo la ruta
    # — infravaloraba en cuatro (backup, export, label, verify).
    #
    # Un instrumento que INFRAVALORA la cobertura no es «conservador»: hace que documentar un subcomando
    # no mueva el numero, y lo que no mueve el numero no se hace. El suelo sube a 12 en el mismo cambio,
    # que es lo que este gate pide en su propia salida.
    if grep -rqE "olivares([[:space:]]+[a-z0-9-]+)*[[:space:]]+$c\b" "$REF" 2>/dev/null; then
        DOC=$((DOC+1))
    else
        FALTAN="$FALTAN $c"
    fi
done <<EOF
$CMDS
EOF

PCT=$(( DOC * 100 / N_CMD ))
echo "check-cli-coverage: $DOC de $N_CMD comando(s) documentados en la referencia pública ($PCT %)"

if [ -n "$FALTAN" ]; then
    echo "check-cli-coverage: sin documentar:$(printf '%s' "$FALTAN" | tr ' ' '\n' | sort | tr '\n' ' ')"
fi

# Trinquete: el suelo es lo ya conseguido. No exige llegar al 100 % hoy — exige NO BAJAR, que es lo
# que impide que un hueco cerrado se vuelva a abrir sin que nadie lo note.
if [ "$DOC" -lt "$UMBRAL" ]; then
    echo "check-cli-coverage: ⛔ la cobertura BAJÓ: $DOC < suelo $UMBRAL" >&2
    exit 1
fi
echo "check-cli-coverage: OK (suelo $UMBRAL; súbelo con OLIVARES_CLI_DOC_FLOOR cuando documentes más)"
exit 0
