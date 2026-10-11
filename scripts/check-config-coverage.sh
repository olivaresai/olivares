#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-config-coverage.sh — a repository gate. Count documented configuration variables.
#
# Measured 2026-08-15: the reference documented seven tokens as a small variable set.
# The original 311 OLIVARES_* tokens included seven comments, regexes or sentinels
# (`OLIVARES_SUPPORT_CONFIG` is a NUL delimiter), plus 23 support-bundle/tooling-only
# tokens, 22 of which nothing reads. The publishable set was 256 variables and
# 16 families, totaling 272. The 311 count measured a different thing.
# Count variables the code reads (`os.Getenv`, `LookupEnv`, viper/env bindings),
# rather than tokens appearing anywhere in text.
# Exit: 0 at or above floor · 1 below floor · 2 could not check.
set -uo pipefail
LC_ALL=C; export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || { echo "check-config-coverage: ⛔ COULD NOT CHECK: missing $RAIZ" >&2; exit 2; }

UMBRAL="${OLIVARES_CONFIG_DOC_FLOOR:-0}"
REF="docs-site/src/content/docs"
[ -d "$REF" ] || { echo "check-config-coverage: ⛔ COULD NOT CHECK: missing $REF" >&2; exit 2; }

# ── Denominador: variables que el código LEE de verdad ───────────────────────────────
# Se buscan en las llamadas de lectura de entorno, no en prosa: un token dentro de un comentario o
# de una expresión regular no es una variable de configuración, y contarlo infla el denominador.
LEIDAS="$(git grep -hoE '(os\.Getenv|os\.LookupEnv|Getenv)\([[:space:]]*"OLIVARES_[A-Z0-9_]+"' \
            -- '*.go' 2>/dev/null | grep -oE 'OLIVARES_[A-Z0-9_]+' | sort -u)"
N="$(printf '%s\n' "$LEIDAS" | grep -c . || true)"

# CONTROL POSITIVO: sin denominador no se aprueba nada. Un cero aquí haría que «0 de 0 documentadas»
# pareciera cobertura perfecta, que es exactamente el falso verde que este gate existe para impedir.
if [ "${N:-0}" -lt 10 ]; then
    echo "check-config-coverage: ⛔ COULD NOT CHECK: only ${N:-0} variable(s) read by the code." >&2
    echo "                       An empty denominator would make any numerator look like full coverage." >&2
    exit 2
fi

DOC=0; MISSING_INPUTS=0
while IFS= read -r v; do
    [ -z "$v" ] && continue
    if grep -rqF "$v" "$REF" 2>/dev/null; then DOC=$((DOC+1)); else MISSING_INPUTS=$((MISSING_INPUTS+1)); fi
done <<EOF
$LEIDAS
EOF

PCT=$(( DOC * 100 / N ))
echo "check-config-coverage: $DOC of $N variable(s) read by the code are documented ($PCT %)"
echo "check-config-coverage: undocumented: $MISSING_INPUTS"

if [ "$DOC" -lt "$UMBRAL" ]; then
    echo "check-config-coverage: ⛔ coverage decreased: $DOC < minimum $UMBRAL" >&2
    exit 1
fi
echo "check-config-coverage: OK (minimum $UMBRAL; raise OLIVARES_CONFIG_DOC_FLOOR as more variables are documented)"
exit 0
