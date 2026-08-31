#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Ensure every paging alert points an operator to an existing runbook or
# operational document from its annotations.

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
rules="$repo_root/deploy/monitoring/olivares-slo.rules.yaml"

# ⛔ LA ENTRADA SE COMPRUEBA ANTES DE LEERLA, Y EL VEREDICTO LO DA ESTE FICHERO, NO `awk`.
# Sin esto el gate ya salía 2 —el código correcto— pero la razón era el error crudo de la
# herramienta: «awk: cannot open …/olivares-slo.rules.yaml». Un rc correcto con una prosa que no
# nombra la causa hace que el lector busque un fallo de `awk`, no un fichero ausente; y depender
# del rc de un ayudante es depender de que su próxima versión no lo cambie.
#
# Y una entrada VACÍA tampoco es cobertura: cero alertas paginables no es «todas cubiertas», es un
# censo que dejó de encontrar sujeto. Las dos salen por la tercera respuesta, con nombre.
if [ ! -r "$rules" ]; then
	echo "runbook coverage: NO HE PODIDO MIRAR: falta ${rules}." >&2
	echo "  Sin las reglas no hay alertas que cubrir, y eso no es cobertura: es no haber mirado." >&2
	exit 2
fi
if ! grep -qE '^[[:space:]]*-[[:space:]]*alert:' "$rules"; then
	echo "runbook coverage: NO HE PODIDO MIRAR: ${rules} no declara ninguna alerta." >&2
	echo "  Cero alertas paginables no es «todas cubiertas»: es un censo sin sujeto." >&2
	exit 2
fi

awk '
function leading_spaces(line, stripped) {
    stripped = line
    sub(/^[[:space:]]*/, "", stripped)
    return length(line) - length(stripped)
}

function finish_alert() {
    if (alert == "" || !pages) {
        return
    }
    checked++
    if (!linked) {
        printf "page alert %s lacks a deploy/runbooks/ or docs/ link in annotations\n", alert > "/dev/stderr"
        missing++
    }
}

/^[[:space:]]*-[[:space:]]+alert:[[:space:]]*/ {
    finish_alert()
    alert = $0
    sub(/^.*alert:[[:space:]]*/, "", alert)
    sub(/[[:space:]]+#.*$/, "", alert)
    sub(/[[:space:]]+$/, "", alert)
    pages = linked = in_labels = in_annotations = 0
    next
}

alert != "" {
    indent = leading_spaces($0)
    content = $0
    sub(/^[[:space:]]*/, "", content)

    if (content !~ /^(#|$)/) {
        if (in_labels && indent <= labels_indent) {
            in_labels = 0
        }
        if (in_annotations && indent <= annotations_indent) {
            in_annotations = 0
        }
    }

    if (content ~ /^labels:[[:space:]]*/) {
        in_labels = 1
        labels_indent = indent
    }
    if (in_labels && content ~ /severity:[[:space:]]*page/) {
        pages = 1
    }

    if (content ~ /^annotations:[[:space:]]*$/) {
        in_annotations = 1
        annotations_indent = indent
        next
    }
    if (in_annotations && content ~ /(deploy\/runbooks\/|docs\/)[^[:space:]\"]+/) {
        linked = 1
    }
}

END {
    finish_alert()
    if (checked == 0) {
        print "no severity=page alerts found; refusing to pass an empty coverage check" > "/dev/stderr"
        exit 1
    }
    if (missing > 0) {
        exit 1
    }
    printf "runbook coverage: %d paging alerts have annotation links\n", checked
}
' "$rules"
