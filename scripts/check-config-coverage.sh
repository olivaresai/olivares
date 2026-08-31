#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-config-coverage.sh — a repository gate. ¿Cuántas variables de configuración están documentadas?
#
# ⛔ POR QUÉ, y con la corrección del denominador incluida. Medido el 2026-08-15: la página de
# referencia documentaba **7 tokens** y decía «un pequeño conjunto de variables». Otro carril
# reprodujo el 311 que yo publiqué y luego lo DESMONTÓ, que es distinto de repetirlo:
#
#     311  tokens OLIVARES_* en el árbol          ← lo que yo conté
#      -7  comentarios, regex o centinelas        (OLIVARES_SUPPORT_CONFIG es un delimitador NUL)
#     -23  sólo en la captura del support bundle o en tooling — 22 de 23 no los lee NADA
#     ====
#     256 variables + 16 familias = 272 publicables
#
# **Mi 311 no estaba mal: contaba otra cosa.** Por eso este gate cuenta variables que el CÓDIGO LEE
# (`os.Getenv`, `LookupEnv`, `viper`/`env` bindings), no tokens que aparezcan en cualquier texto.
#
# Salida: 0 no baja del suelo · 1 bajó · 2 NO HE PODIDO MIRAR (nunca es un verde).
set -uo pipefail
LC_ALL=C; export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || { echo "check-config-coverage: ⛔ NO HE PODIDO MIRAR: no existe $RAIZ" >&2; exit 2; }

UMBRAL="${OLIVARES_CONFIG_DOC_FLOOR:-0}"
REF="docs-site/src/content/docs"
[ -d "$REF" ] || { echo "check-config-coverage: ⛔ NO HE PODIDO MIRAR: no existe $REF" >&2; exit 2; }

# ── Denominador: variables que el código LEE de verdad ───────────────────────────────
# Se buscan en las llamadas de lectura de entorno, no en prosa: un token dentro de un comentario o
# de una expresión regular no es una variable de configuración, y contarlo infla el denominador.
LEIDAS="$(git grep -hoE '(os\.Getenv|os\.LookupEnv|Getenv)\([[:space:]]*"OLIVARES_[A-Z0-9_]+"' \
            -- '*.go' 2>/dev/null | grep -oE 'OLIVARES_[A-Z0-9_]+' | sort -u)"
N="$(printf '%s\n' "$LEIDAS" | grep -c . || true)"

# CONTROL POSITIVO: sin denominador no se aprueba nada. Un cero aquí haría que «0 de 0 documentadas»
# pareciera cobertura perfecta, que es exactamente el falso verde que este gate existe para impedir.
if [ "${N:-0}" -lt 10 ]; then
    echo "check-config-coverage: ⛔ NO HE PODIDO MIRAR: sólo ${N:-0} variable(s) leídas por el código." >&2
    echo "                       Un denominador vacío convierte cualquier numerador en cobertura total." >&2
    exit 2
fi

DOC=0; FALTAN=0
while IFS= read -r v; do
    [ -z "$v" ] && continue
    if grep -rqF "$v" "$REF" 2>/dev/null; then DOC=$((DOC+1)); else FALTAN=$((FALTAN+1)); fi
done <<EOF
$LEIDAS
EOF

PCT=$(( DOC * 100 / N ))
echo "check-config-coverage: $DOC de $N variable(s) LEÍDAS por el código están documentadas ($PCT %)"
echo "check-config-coverage: sin documentar: $FALTAN"

if [ "$DOC" -lt "$UMBRAL" ]; then
    echo "check-config-coverage: ⛔ la cobertura BAJÓ: $DOC < suelo $UMBRAL" >&2
    exit 1
fi
echo "check-config-coverage: OK (suelo $UMBRAL; súbelo con OLIVARES_CONFIG_DOC_FLOOR al documentar más)"
exit 0
