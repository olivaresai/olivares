#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Bench for scripts/catalog-dodo-discount.mjs — the instrument that writes a discount rule into
# the catalogue of the worker that charges customers.
#
# It runs against a COPY of the real wrangler.jsonc, never the file itself, so the bench cannot
# leave a rule behind in a tree someone then pushes. The copy carries the real `src/` too, because
# the whole point of the instrument is that the WORKER'S OWN parser decides what may be written —
# a bench with a stub parser would prove nothing about the gate that matters.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/scripts" "$WORK/commercial/license-worker"
cp "$ROOT/scripts/catalog-dodo-discount.mjs" "$WORK/scripts/"
cp "$ROOT/commercial/license-worker/wrangler.jsonc" "$WORK/commercial/license-worker/"
cp -r "$ROOT/commercial/license-worker/src" "$WORK/commercial/license-worker/"
ORIG="$WORK/original.jsonc"
cp "$WORK/commercial/license-worker/wrangler.jsonc" "$ORIG"

PROD_PRODUCT=pdt_0NlE6WlUBAFO7F2uZLbHE
SANDBOX_PRODUCT=pdt_0NkL6fPms1DwlDsUUcawf
ID=dsc_0BENCH000000000000001
fails=0

run() { ( cd "$WORK" && node scripts/catalog-dodo-discount.mjs "$@" ) 2>&1; }
rc_of() { ( cd "$WORK" && node scripts/catalog-dodo-discount.mjs "$@" >/dev/null 2>&1 ); echo $?; }

ok()  { printf 'ok    %s\n' "$1"; }
bad() { printf 'FAIL  %s\n' "$1"; fails=$((fails+1)); }

expect_rc() { # expect_rc <esperado> <titular> -- <args…>
  local want="$1" title="$2"; shift 3
  local got; got=$(rc_of "$@")
  [ "$got" = "$want" ] && ok "$title (rc=$got)" || bad "$title: esperaba rc=$want, obtuvo $got"
}

# ---------- lo que DEBE pasar ----------
expect_rc 0 "una regla valida en produccion pasa el parser" -- \
  --env production --id "$ID" --bp 9900 --product "$PROD_PRODUCT" --cycles 1 --check

# ---------- los NO-DISPAROS, que son la razon de que exista ----------
expect_rc 1 "NO-DISPARO 100%: una regla de 10000bp es rehusada (es Fase B)" -- \
  --env production --id "$ID" --bp 10000 --product "$PROD_PRODUCT" --cycles 1 --check
expect_rc 1 "NO-DISPARO: sin --cycles (el defecto del proveedor es PARA SIEMPRE)" -- \
  --env production --id "$ID" --bp 9900 --product "$PROD_PRODUCT" --check
expect_rc 1 "NO-DISPARO: 0bp no es un descuento" -- \
  --env production --id "$ID" --bp 0 --product "$PROD_PRODUCT" --cycles 1 --check
expect_rc 1 "NO-DISPARO: un producto de SANDBOX en el catalogo de PRODUCCION" -- \
  --env production --id "$ID" --bp 9900 --product "$SANDBOX_PRODUCT" --cycles 1 --check
expect_rc 1 "NO-DISPARO: sin --product" -- \
  --env production --id "$ID" --bp 9900 --cycles 1 --check
expect_rc 1 "NO-DISPARO: --env inventado" -- \
  --env staging --id "$ID" --bp 9900 --product "$PROD_PRODUCT" --cycles 1 --check

# --check no escribe: la comprobacion que hace inutil a todo lo anterior si falla
if cmp -s "$ORIG" "$WORK/commercial/license-worker/wrangler.jsonc"; then
  ok "--check no ha escrito nada en ninguna de las siete llamadas"
else
  bad "--check ESCRIBIO — el resto de este banco no prueba nada"
fi

# ---------- la escritura, y su alcance ----------
run --env production --id "$ID" --bp 9900 --product "$PROD_PRODUCT" --cycles 1 >/dev/null
changed=$(diff "$ORIG" "$WORK/commercial/license-worker/wrangler.jsonc" | grep -c '^[<>]' || true)
[ "$changed" = "2" ] && ok "una escritura toca UNA sola linea" || bad "toco $((changed/2)) lineas, esperaba 1"

sandbox_line_orig=$(grep -n '"DODO_CATALOG"' "$ORIG" | sed -n '2p')
sandbox_line_now=$(grep -n '"DODO_CATALOG"' "$WORK/commercial/license-worker/wrangler.jsonc" | sed -n '2p')
[ "$sandbox_line_orig" = "$sandbox_line_now" ] && ok "el bloque de SANDBOX queda intacto" || bad "el bloque de sandbox cambio"

# la regla se lee de vuelta con el parser del worker, no con una expresion regular
if ( cd "$WORK" && node --input-type=module -e "
import { parseDodoCatalog } from './commercial/license-worker/src/dodo/catalog.ts';
import { readFileSync } from 'node:fs';
const line = readFileSync('commercial/license-worker/wrangler.jsonc','utf8')
  .split('\n').filter(l => l.includes('\"DODO_CATALOG\"'))
  .find(l => l.includes('$PROD_PRODUCT'));
const c = parseDodoCatalog(JSON.parse('\"' + line.match(/\"DODO_CATALOG\": \"(.*)\",?\s*\$/)[1] + '\"'));
const r = c.discounts.get('$ID');
if (!r || r.basisPoints !== 9900 || r.subscriptionCycles !== 1 || r.appliesToAddons !== false) {
  console.error('la regla no vuelve igual:', JSON.stringify(r)); process.exit(1);
}
" 2>/dev/null ); then ok "la regla vuelve del fichero con el parser del worker"; else bad "la regla no round-trips"; fi

expect_rc 0 "repetir la misma regla no escribe (idempotente)" -- \
  --env production --id "$ID" --bp 9900 --product "$PROD_PRODUCT" --cycles 1

# ---------- y la vuelta atras ----------
run --env production --id "$ID" --remove >/dev/null
if cmp -s "$ORIG" "$WORK/commercial/license-worker/wrangler.jsonc"; then
  ok "--remove devuelve el fichero BYTE A BYTE al original"
else
  bad "--remove no restaura el fichero"
fi
expect_rc 0 "--remove de algo que no esta es un no-op, no un error" -- --env production --id "$ID" --remove

echo
if [ "$fails" -eq 0 ]; then
  echo "catalog-dodo-discount bench: OK"
else
  echo "catalog-dodo-discount bench: $fails FALLO(S)"
  exit 1
fi
