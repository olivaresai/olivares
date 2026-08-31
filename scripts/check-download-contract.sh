#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-download-contract.sh — que `ci/download-contract.txt` sea EXACTAMENTE lo que sus fuentes
# dicen hoy, regenerandolo y comparando. No lee el fichero y lo cree: lo vuelve a derivar.
#
# ⛔ ESTA ES LA UNICA PROPIEDAD QUE IMPORTA, y su ausencia ya se pago una vez. `sets.ts:24-25`:
# «anadir un quinto add-on habria dejado ALLOWED_SET_SLUGS corto (17 en vez de 33) — 404 para sus
# compradores — con el test EN VERDE, porque comprobaba el acuerdo con su propia copia». Un gate
# que compara el contrato exportado contra si mismo repite ese fallo con otro nombre.
#
# TRES RESPUESTAS: 0 al dia · 1 divergencia · 2 no he podido mirar.
set -uo pipefail
ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
GEN="$ROOT/scripts/render-download-contract.sh"
OUT="$ROOT/ci/download-contract.txt"
blind() { printf 'check-download-contract: NO HE PODIDO MIRAR — %s\n' "$*" >&2; exit 2; }

[ -x "$GEN" ] || blind "falta el generador $GEN"
[ -r "$OUT" ] || blind "falta $OUT — el contrato exportado no existe; el espejo no podria comprobar su productor"

TMP="$(mktemp "${TMPDIR:-/var/tmp}/dlcontract.XXXXXX")" || blind "no pude crear un temporal"
trap 'rm -f "$TMP"' EXIT

if ! OLIVARES_ROOT="$ROOT" bash "$GEN" >"$TMP" 2>"$TMP.err"; then
	sed 's/^/    /' "$TMP.err" >&2; rm -f "$TMP.err"
	blind "el generador no pudo derivar el contrato de sus fuentes"
fi
rm -f "$TMP.err"

if diff -u "$OUT" "$TMP" >/dev/null 2>&1; then
	printf 'check-download-contract: al dia — %s coincide con lo que sus fuentes dicen hoy (%s slugs).\n' \
		"${OUT#"$ROOT"/}" "$(grep -m1 '^allowed_set_slugs=' "$OUT" | cut -d= -f2- | wc -w)"
	exit 0
fi

echo "check-download-contract: ⛔ DIVERGE — el contrato exportado NO es lo que sus fuentes dicen hoy." >&2
echo "  Izquierda: el fichero versionado. Derecha: lo que sale de regenerarlo AHORA." >&2
diff -u "$OUT" "$TMP" | sed -n '1,20p' | sed 's/^/    /' >&2
echo "  Remedio: bash scripts/render-download-contract.sh > ci/download-contract.txt" >&2
echo "  NO se edita a mano: el fichero es derivado y su fuente es commercial/…/{artifacts,sets}.ts" >&2
exit 1
