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
blind() { printf 'check-download-contract: COULD NOT CHECK — %s\n' "$*" >&2; exit 2; }

[ -x "$GEN" ] || blind "missing generator $GEN"
[ -r "$OUT" ] || blind "missing $OUT — the exported contract is absent; the mirror cannot check its producer"

TMP="$(mktemp "${TMPDIR:-/var/tmp}/dlcontract.XXXXXX")" || blind "could not create a temporary file"
trap 'rm -f "$TMP"' EXIT

if ! OLIVARES_ROOT="$ROOT" bash "$GEN" >"$TMP" 2>"$TMP.err"; then
	sed 's/^/    /' "$TMP.err" >&2; rm -f "$TMP.err"
	blind "the generator could not derive the contract from its sources"
fi
rm -f "$TMP.err"

if diff -u "$OUT" "$TMP" >/dev/null 2>&1; then
	printf 'check-download-contract: current — %s matches its sources today (%s slugs).\n' \
		"${OUT#"$ROOT"/}" "$(grep -m1 '^allowed_set_slugs=' "$OUT" | cut -d= -f2- | wc -w)"
	exit 0
fi

echo "check-download-contract: ⛔ MISMATCH — the exported contract differs from its current sources." >&2
echo "  Left: the versioned file. Right: the output of regenerating it now." >&2
diff -u "$OUT" "$TMP" | sed -n '1,20p' | sed 's/^/    /' >&2
echo "  Remedy: bash scripts/render-download-contract.sh > ci/download-contract.txt" >&2
echo "  Do not edit by hand: this file is derived from commercial/…/{artifacts,sets}.ts" >&2
exit 1
