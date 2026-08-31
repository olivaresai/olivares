#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C03-11 — FASE R stays HOLD until a customer binary exists to replace.
# Exit 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT"

DOC="$(find design -maxdepth 1 -name 'C03-11-FASE-R-HOLD-*.md' -print | sort | tail -n 1 || true)"
SEAT="$ROOT/core/auth/seatcap.go"
WRANGLER="$ROOT/commercial/license-worker/wrangler.jsonc"

if [[ -z "$DOC" || ! -f "$DOC" ]]; then
	echo "C03-11: COULD NOT LOOK — no FASE R HOLD prep doc under design/" >&2
	exit 2
fi
if [[ ! -f "$SEAT" ]]; then
	echo "C03-11: COULD NOT LOOK — core/auth/seatcap.go missing" >&2
	exit 2
fi
if [[ ! -f "$WRANGLER" ]]; then
	echo "C03-11: COULD NOT LOOK — wrangler.jsonc missing" >&2
	exit 2
fi

fail=0
doc="$(cat "$DOC")"

if ! grep -q 'HOLD' "$DOC"; then
	echo "C03-11: HOLD marker missing from $DOC" >&2
	fail=1
fi
if ! grep -q 'unlimitedSeatPolicy' "$DOC"; then
	echo "C03-11: overlay seats measurement missing from $DOC" >&2
	fail=1
fi
# Do not match "does not claim FIRMA A" — that is the HOLD sentence.
if grep -qiE 'binaries replaced|FASE R complete|substitution is done' "$DOC"; then
	echo "C03-11: prep doc claims a completed substitution" >&2
	fail=1
fi

if ! grep -q 'func (a \*Authenticator) enforceSeatCapTx' "$SEAT"; then
	echo "C03-11: COULD NOT LOOK — enforceSeatCapTx not in seatcap.go" >&2
	exit 2
fi
# The function body must stay a bare return nil (the no-op). A later
# session that counts seats here re-opens the cap this HOLD is about.
body="$(awk '/func \(a \*Authenticator\) enforceSeatCapTx/,/^}/' "$SEAT")"
if ! grep -q 'return nil' <<<"$body"; then
	echo "C03-11: enforceSeatCapTx no longer returns nil" >&2
	fail=1
fi
if grep -qE 'MaxUsers|countSeats|limit' <<<"$body"; then
	echo "C03-11: enforceSeatCapTx body reads a seat figure" >&2
	fail=1
fi

# Production block: FULFILLMENT_ENABLED must stay the explicit off.
# Sandbox may be on; that is not a customer binary of the old cap.
# ⛔ AQUI EL TERMINADOR ERA `/^[ ]{4}\},$/` Y EL RANGO NO TERMINABA NUNCA. Medido con
# control positivo sobre el awk de esta caja:
#
#   printf 'aaaa\n' | awk '/^a{4}$/{print}'   → NADA
#   printf 'a{4}\n' | awk '/^a{4}$/{print}'   → a{4}      ← lo lee LITERAL
#
# Este awk NO hace intervalos (no es gawk con --re-interval), asi que `[ ]{4}` casa la
# cadena «un espacio seguido de {4}», que no existe en el fichero: el rango se abria en
# `"production": {` y corria HASTA EL FINAL. El `head -n 40` lo tapaba a medias.
#
# ⚠ Y LA CURA QUE ME LLEGO PROPUESTA —`/^    \},$/`— TAMPOCO TERMINA, medido: el bloque de
# production es el ULTIMO del fichero y su cierre es `    }` SIN COMA (:282). Con coma
# obligatoria el rango vuelve a irse a EOF. Por eso la coma va OPCIONAL.
#
# Hoy esto NO estaba abriendo la puerta, y conviene decirlo con precision: production es
# la ultima seccion, asi que detras solo hay llaves de cierre y el `grep` no podia
# satisfacerse con el valor de otro entorno. Era un fail-open LATENTE: el dia que alguien
# anada un entorno DESPUES de production, el `"FULFILLMENT_ENABLED": "false"` de ESE
# entorno satisface la comprobacion y production puede quedarse en `true` sin que nada
# lo diga. Por eso ademas de acotar, se AFIRMA que el bloque acota (abajo).
prod_block="$(awk '/"production": \{/,/^    \},?$/' "$WRANGLER")"
if [[ -z "$prod_block" ]]; then
	echo "C03-11: COULD NOT LOOK — no encuentro el bloque \"production\" en $WRANGLER" >&2
	exit 2
fi
# El rango tiene que ACOTAR. Si arrastra otra seccion, el grep de abajo puede quedar
# satisfecho por el valor de OTRO entorno, que es justo el fallo que esta cura corta.
if grep -qE '^[[:space:]]{4}"(sandbox|staging|preview|development)"[[:space:]]*:' <<<"$prod_block"; then
	echo "C03-11: el bloque de production arrastra otra seccion — el rango no acota" >&2
	fail=1
fi
if ! grep -q '"FULFILLMENT_ENABLED": "false"' <<<"$prod_block"; then
	echo "C03-11: production fulfillment is not the explicit off" >&2
	fail=1
fi

if [[ "$fail" -ne 0 ]]; then
	echo "C03-11: $fail finding(s)" >&2
	exit 1
fi
echo "C03-11: CLEAN — FASE R remains HOLD; seat seam still no-op; production fulfillment off"
exit 0
