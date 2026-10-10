#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-commerce-entity-fks.sh — la lista blanca de claves foráneas hacia
# commerce.legal_entities vive TECLEADA dentro de un SQL embebido en
# commercial/commerce/cmd/commerce/main.go, y nada la derivaba de las migraciones que las
# crean. El 2026-08-20 la migración 010 añadió commerce.legal_entity_decisions con una FK
# hacia legal_entities; la lista no se tocó desde el 08-13. Consecuencia medida: el
# postflight de arranque la clasificaba como FK INESPERADA y el servicio REHUSABA servir
# —ocho tests de race-rest en rojo dos días seguidos con «run exited before serving»—, y
# el servicio de comercio no habría arrancado contra una base migrada a 010.
#
# TRES RESPUESTAS, como todo gate de este árbol:
#   0  limpio    — las dos listas coinciden exactamente
#   1  hallazgo  — sobra o falta alguna
#   2  NO PUDE MIRAR — no encuentro los ficheros, o una migración usa una forma que este
#                     guion no sabe leer. No adivina: una FK que no sé nombrar es un 2.
set -euo pipefail

RAIZ="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
MAIN="$RAIZ/commercial/commerce/cmd/commerce/main.go"
MIGR="$RAIZ/commercial/commerce/migrations"

if [ ! -r "$MAIN" ]; then
	echo "check-commerce-entity-fks: COULD NOT CHECK — cannot read $MAIN" >&2; exit 2
fi
if [ ! -d "$MIGR" ]; then
	echo "check-commerce-entity-fks: COULD NOT CHECK — missing directory $MIGR" >&2; exit 2
fi

tmp="$(mktemp -d "${TMPDIR:-/tmp}/commfk.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

# --- 1. lo DECLARADO: la lista blanca del SQL embebido -----------------------------------
# Una línea por entrada, formato "<tabla> <columna>".
awk '
	/expected_entity_foreign_keys\(/ { dentro=1; next }
	dentro && /^[[:space:]]*\)/      { dentro=0 }
	dentro && /^[[:space:]]*\(/ {
		linea=$0
		gsub(/[()]/,"",linea); gsub(/'"'"'/,"",linea); gsub(/,[[:space:]]*$/,"",linea)
		n=split(linea,c,/,[[:space:]]*/)
		if (n>=3) { gsub(/^[[:space:]]+|[[:space:]]+$/,"",c[2]); gsub(/^[[:space:]]+|[[:space:]]+$/,"",c[3]); print c[2], c[3] }
	}
' "$MAIN" | sort -u > "$tmp/declarado"

if [ ! -s "$tmp/declarado" ]; then
	echo "check-commerce-entity-fks: COULD NOT CHECK — cannot read expected_entity_foreign_keys in $MAIN" >&2
	exit 2
fi

# --- 2. lo REAL: las FK que crean las migraciones ----------------------------------------
# La tabla es el CREATE TABLE vigente y la columna el primer campo de la linea REFERENCES.
# ⛔ Y SE RASTREA SI ESTAMOS DENTRO DEL BLOQUE: una REFERENCES a legal_entities que aparezca
# FUERA de un CREATE TABLE (un ALTER TABLE ... ADD CONSTRAINT, por ejemplo) no se puede
# atribuir a una tabla leyendo hacia atras — la version anterior de este guion se la colgaba
# al ultimo CREATE TABLE visto y contestaba 1 con una lista INVENTADA donde debia contestar 2.
# Lo cazo su propia bateria con un fixture de tres lineas. Una FK que no se atribuir es un 2.
awk '
	BEGIN { IGNORECASE=1; dentro=0; huerfana=0 }
	/^[[:space:]]*CREATE[[:space:]]+TABLE/ {
		linea=$0
		sub(/.*CREATE[[:space:]]+TABLE[[:space:]]+(IF[[:space:]]+NOT[[:space:]]+EXISTS[[:space:]]+)?/,"",linea)
		sub(/^commerce\./,"",linea)
		sub(/[[:space:](].*/,"",linea)
		tabla=linea; dentro=1
	}
	/REFERENCES[[:space:]]+commerce\.legal_entities/ {
		if (dentro && tabla != "" && $1 != "") print tabla, $1
		else huerfana=1
	}
	/^[[:space:]]*\)[[:space:]]*;/ { dentro=0; tabla="" }
	END { if (huerfana) print "__HUERFANA__ __HUERFANA__" }
' "$MIGR"/*.up.sql | sort -u > "$tmp/real"

if command grep -q '^__HUERFANA__' "$tmp/real"; then
	echo "check-commerce-entity-fks: COULD NOT CHECK — a REFERENCES to commerce.legal_entities appears" >&2
	echo "  outside a CREATE TABLE (ALTER TABLE ... ADD CONSTRAINT, or a form this script cannot" >&2
	echo "  associate with its table). Refusing to approve an incomplete list." >&2
	exit 2
fi

if [ ! -s "$tmp/real" ]; then
	echo "check-commerce-entity-fks: COULD NOT CHECK — no foreign keys to legal_entities found in $MIGR" >&2
	exit 2
fi

# --- 3. comparación -----------------------------------------------------------------------
faltan="$(comm -13 "$tmp/declarado" "$tmp/real" || true)"
sobran="$(comm -23 "$tmp/declarado" "$tmp/real" || true)"
n_dec="$(command grep -c . "$tmp/declarado" || true)"
n_real="$(command grep -c . "$tmp/real" || true)"

if [ -z "$faltan" ] && [ -z "$sobran" ]; then
	echo "check-commerce-entity-fks: CLEAN — the $n_real foreign keys to legal_entities created by migrations"
	echo "  exactly match the $n_dec declared by expected_entity_foreign_keys."
	exit 0
fi

echo "check-commerce-entity-fks: FINDING — the postflight allowlist and migrations disagree." >&2
if [ -n "$faltan" ]; then
	echo "  IN MIGRATIONS BUT NOT IN THE LIST (the service will refuse to start):" >&2
	printf '%s\n' "$faltan" | while read -r t c; do
		[ -n "$t" ] || continue
		echo "    ('${t}_${c}_fkey', '${t}', '${c}')" >&2
	done
fi
if [ -n "$sobran" ]; then
	echo "  IN THE LIST BUT NOT IN MIGRATIONS (declares something that no longer exists):" >&2
	printf '%s\n' "$sobran" | sed 's/^/    /' >&2
fi
echo "  fix: add or remove these rows in expected_entity_foreign_keys in" >&2
echo "  commercial/commerce/cmd/commerce/main.go. Declared=$n_dec actual=$n_real." >&2
exit 1
