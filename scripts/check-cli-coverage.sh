#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-cli-coverage.sh — a repository gate. Count documented CLI commands.
#
# Measured 2026-08-15: the public reference covered 4 of 42 commands and 7 of 311
# configuration tokens. A manual sweep found the gap; existing gates covered modules,
# but not commands, variables, views or capture freshness. This gate covers commands.
# Derive the denominator from the command tree: a separate member list silently ages,
# as happened with route censuses, canonical package maps, route allowlists and the
# version guard that missed `docs/*.md`.
# Exit: 0 coverage meets threshold · 1 below threshold · 2 could not check.
set -uo pipefail
LC_ALL=C; export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || { echo "check-cli-coverage: ⛔ COULD NOT CHECK: missing $RAIZ" >&2; exit 2; }

UMBRAL="${OLIVARES_CLI_DOC_FLOOR:-0}"   # suelo de trinquete: no baja de lo ya conseguido
REF="docs-site/src/content/docs/reference"

[ -d "$REF" ] || { echo "check-cli-coverage: ⛔ COULD NOT CHECK: missing $REF" >&2; exit 2; }

# ── Denominador: los comandos que el binario DECLARA, no una lista escrita a mano ─────
# Cobra declara cada comando con `Use:` dentro de una `&cobra.Command{...}`. Se toma la primera
# palabra, que es el verbo.
CMDS="$(git grep -ho 'Use:[[:space:]]*"[a-z][a-z0-9-]*' -- 'cmd/olivares/*.go' 2>/dev/null \
        | sed 's/.*"//' | sort -u | grep -v '^$')"
N_CMD="$(printf '%s\n' "$CMDS" | grep -c . || true)"

# CONTROL POSITIVO: si el árbol de comandos sale vacío, la sonda no mide y NADA se aprueba.
if [ "${N_CMD:-0}" -lt 5 ]; then
    echo "check-cli-coverage: ⛔ COULD NOT CHECK: the command tree yielded ${N_CMD:-0} entries." >&2
    echo "                    An empty denominator would make any numerator look like full coverage." >&2
    exit 2
fi

# ── Numerador: cuáles aparecen en la referencia pública ──────────────────────────────
DOC=0; MISSING_INPUTS=""
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
        MISSING_INPUTS="$MISSING_INPUTS $c"
    fi
done <<EOF
$CMDS
EOF

PCT=$(( DOC * 100 / N_CMD ))
echo "check-cli-coverage: $DOC of $N_CMD command(s) documented in the public reference ($PCT %)"

if [ -n "$MISSING_INPUTS" ]; then
    echo "check-cli-coverage: undocumented:$(printf '%s' "$MISSING_INPUTS" | tr ' ' '\n' | sort | tr '\n' ' ')"
fi

# Trinquete: el suelo es lo ya conseguido. No exige llegar al 100 % hoy — exige NO BAJAR, que es lo
# que impide que un hueco cerrado se vuelva a abrir sin que nadie lo note.
if [ "$DOC" -lt "$UMBRAL" ]; then
    echo "check-cli-coverage: ⛔ coverage decreased: $DOC < minimum $UMBRAL" >&2
    exit 1
fi
echo "check-cli-coverage: OK (minimum $UMBRAL; raise OLIVARES_CLI_DOC_FLOOR as more commands are documented)"
exit 0
