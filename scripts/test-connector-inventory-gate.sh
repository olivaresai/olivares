#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-connector-inventory-gate.sh — la batería de check-connector-inventory.sh.
#
# ⛔ SEÑUELOS, NUNCA EL ÁRBOL VIVO. Las propiedades que hay que probar son las ROJAS —un conector
#    sin fila, una fila sin conector, la cifra transcrita— y probarlas mutando `connectors/` o
#    `docs/ai-context/CONNECTORS.md` de verdad dejaría el repositorio tocado si la batería muere a
#    mitad. El gate acepta `OLIVARES_CONNECTOR_INVENTORY` y `OLIVARES_CONNECTOR_DIR` justamente
#    para que exista este banco.
#
# ⚠ Y EL SEÑUELO LLEVA LO QUE EL SUJETO LEE: un documento sin «## Truth Table» no mide la
#   cobertura, mide que el gate no encuentra la sección. Cada caso construye un documento COMPLETO.
#
# Salida: 0 todas pasan · 1 alguna falla · 2 no se pudo montar el banco.
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)"
GATE="$RAIZ/scripts/check-connector-inventory.sh"
[ -r "$GATE" ] || {
	echo "test-connector-inventory-gate: ⛔ NO HE PODIDO MIRAR: no existe $GATE" >&2
	exit 2
}
BANCO="$(mktemp -d "$RAIZ/.inv-XXXXXX")" || exit 2
trap 'rm -rf "$BANCO"' EXIT

pasan=0
fallan=0
comprobar() {
	if [ "$3" -eq "$2" ]; then
		printf '  ok    %-56s rc=%s\n' "$1" "$3"
		pasan=$((pasan + 1))
	else
		printf '  FALLA %-56s rc=%s (quiere %s)\n' "$1" "$3" "$2"
		fallan=$((fallan + 1))
	fi
}

# monta <dir-conectores> <lista-de-filas> <cifra-resumen> → escribe un doc completo
monta_doc() {
	destino="$1"
	cifra="$2"
	shift 2
	{
		printf '# Connector Inventory Truth Table (señuelo)\n\n## Summary\n\n'
		printf '| Metric | Value | Evidence |\n|---|---:|---|\n'
		printf '| Connector directories | %s | señuelo |\n\n' "$cifra"
		printf '## Truth Table\n\n| Connector | Class | Evidence | Tests | Registry |\n|---|---|---|---|---|\n'
		for c in "$@"; do printf '| `%s` | inproc | x | yes | y |\n' "$c"; done
		printf '\n## Canonical Public Phrasing\n\ntexto\n'
	} >"$destino"
}

# monta un árbol de conectores con código Go
monta_dirs() {
	base="$1"
	shift
	rm -rf "$base"
	for c in "$@"; do
		mkdir -p "$base/$c"
		printf 'package %s\n' "$(printf '%s' "$c" | tr -cd 'a-z')" >"$base/$c/x.go"
	done
}

correr() {
	OLIVARES_CONNECTOR_INVENTORY="$1" OLIVARES_CONNECTOR_DIR="$2" bash "$GATE" >"$BANCO/out.log" 2>&1
}

# ── 1 · SUELO: árbol y tabla de acuerdo ⇒ limpio ─────────────────────────────────────────
monta_dirs "$BANCO/c" alfa beta
monta_doc "$BANCO/d.md" 2 alfa beta
correr "$BANCO/d.md" "$BANCO/c"
comprobar "tabla y árbol de acuerdo salen limpios" 0 "$?"

# ── 2 · EL DEFECTO QUE TRAJO EL GATE: conector con Go y sin fila ──────────────────────────
monta_dirs "$BANCO/c" alfa beta gamma
monta_doc "$BANCO/d.md" 3 alfa beta
correr "$BANCO/d.md" "$BANCO/c"
comprobar "un conector con Go y SIN fila es un hallazgo" 1 "$?"
if grep -q '`gamma`' "$BANCO/out.log"; then
	printf '  ok    %-56s\n' "y el mensaje NOMBRA cuál falta"
	pasan=$((pasan + 1))
else
	printf '  FALLA %-56s\n' "el hallazgo no dice qué conector falta"
	fallan=$((fallan + 1))
fi

# ── 3 · La dirección contraria: fila que nombra algo inexistente ─────────────────────────
monta_dirs "$BANCO/c" alfa
monta_doc "$BANCO/d.md" 1 alfa fantasma
correr "$BANCO/d.md" "$BANCO/c"
comprobar "una fila sin conector es un hallazgo" 1 "$?"

# ── 4 · Un directorio SIN Go no necesita fila (el caso `backstage`) ──────────────────────
# Sin esto, el gate exigiría fila a los plugins TypeScript y pondría rojo un árbol correcto —
# la forma más rápida de que un gate acabe desactivado.
monta_dirs "$BANCO/c" alfa
mkdir -p "$BANCO/c/soloweb" && printf '{}' >"$BANCO/c/soloweb/package.json"
monta_doc "$BANCO/d.md" 2 alfa
correr "$BANCO/d.md" "$BANCO/c"
comprobar "un directorio sin Go no necesita fila" 0 "$?"

# ── 5 · La cifra del resumen se re-deriva, no se transcribe ──────────────────────────────
monta_dirs "$BANCO/c" alfa beta
monta_doc "$BANCO/d.md" 7 alfa beta
correr "$BANCO/d.md" "$BANCO/c"
comprobar "una cifra de resumen transcrita mal es un hallazgo" 1 "$?"

# ── 6 · TERCERA RESPUESTA: lo que no se puede mirar no es verde ──────────────────────────
correr "$BANCO/no-existe.md" "$BANCO/c"
comprobar "documento ausente es NO HE PODIDO MIRAR" 2 "$?"
correr "$BANCO/d.md" "$BANCO/no-existe-dir"
comprobar "árbol de conectores ausente es NO HE PODIDO MIRAR" 2 "$?"

# Un documento SIN la sección: no se adivina la tabla, se dice que no se pudo mirar.
printf '# sin tabla\n\ntexto\n' >"$BANCO/sin-tabla.md"
correr "$BANCO/sin-tabla.md" "$BANCO/c"
comprobar "documento sin «## Truth Table» es NO HE PODIDO MIRAR" 2 "$?"

# Y una tabla presente pero VACÍA: cero filas contra todo daría un hallazgo por conector, que se
# lee como catástrofe. Es una medición que no se ha podido hacer.
monta_doc "$BANCO/vacia.md" 2
correr "$BANCO/vacia.md" "$BANCO/c"
comprobar "tabla sin filas reconocibles es NO HE PODIDO MIRAR" 2 "$?"

echo "test-connector-inventory-gate: $pasan pasan, $fallan fallan"
[ "$fallan" -eq 0 ] || exit 1
exit 0
