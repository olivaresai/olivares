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
  [ "$got" = "$want" ] && ok "$title (rc=$got)" || bad "$title: expected rc=$want, got $got"
}

# ---------- lo que DEBE pasar ----------
expect_rc 0 "a valid production rule passes the parser" -- \
  --env production --id "$ID" --bp 9900 --product "$PROD_PRODUCT" --cycles 1 --check

# ---------- los NO-DISPAROS, que son la razon de que exista ----------
expect_rc 1 "NO TRIGGER 100%: a 10000bp rule is refused (it is Phase B)" -- \
  --env production --id "$ID" --bp 10000 --product "$PROD_PRODUCT" --cycles 1 --check
expect_rc 1 "NO TRIGGER: no --cycles (the provider default is FOREVER)" -- \
  --env production --id "$ID" --bp 9900 --product "$PROD_PRODUCT" --check
expect_rc 1 "NO TRIGGER: 0bp is not a discount" -- \
  --env production --id "$ID" --bp 0 --product "$PROD_PRODUCT" --cycles 1 --check
expect_rc 1 "NO TRIGGER: a SANDBOX product in the PRODUCTION catalog" -- \
  --env production --id "$ID" --bp 9900 --product "$SANDBOX_PRODUCT" --cycles 1 --check
expect_rc 1 "NO TRIGGER: no --product" -- \
  --env production --id "$ID" --bp 9900 --cycles 1 --check
expect_rc 1 "NO TRIGGER: unknown --env" -- \
  --env staging --id "$ID" --bp 9900 --product "$PROD_PRODUCT" --cycles 1 --check

# --check no escribe: la comprobacion que hace inutil a todo lo anterior si falla
if cmp -s "$ORIG" "$WORK/commercial/license-worker/wrangler.jsonc"; then
  ok "--check wrote nothing in any of the seven calls"
else
  bad "--check WROTE DATA — the rest of this test proves nothing"
fi

# ---------- la escritura, y su alcance ----------
run --env production --id "$ID" --bp 9900 --product "$PROD_PRODUCT" --cycles 1 >/dev/null
changed=$(diff "$ORIG" "$WORK/commercial/license-worker/wrangler.jsonc" | grep -c '^[<>]' || true)
[ "$changed" = "2" ] && ok "a write touches ONE line" || bad "touched $((changed/2)) lines, expected 1"

sandbox_line_orig=$(grep -n '"DODO_CATALOG"' "$ORIG" | sed -n '2p')
sandbox_line_now=$(grep -n '"DODO_CATALOG"' "$WORK/commercial/license-worker/wrangler.jsonc" | sed -n '2p')
[ "$sandbox_line_orig" = "$sandbox_line_now" ] && ok "the SANDBOX block remains intact" || bad "the sandbox block changed"

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
  console.error('the rule did not round-trip:', JSON.stringify(r)); process.exit(1);
}
" 2>/dev/null ); then ok "the rule round-trips from the file through the worker parser"; else bad "the rule does not round-trip"; fi

expect_rc 0 "repeating the same rule does not write (idempotent)" -- \
  --env production --id "$ID" --bp 9900 --product "$PROD_PRODUCT" --cycles 1

# ---------- y la vuelta atras ----------
run --env production --id "$ID" --remove >/dev/null
if cmp -s "$ORIG" "$WORK/commercial/license-worker/wrangler.jsonc"; then
  ok "--remove restores the file BYTE FOR BYTE"
else
  bad "--remove does not restore the file"
fi
expect_rc 0 "--remove of a missing rule is a no-op, not an error" -- --env production --id "$ID" --remove

echo
if [ "$fails" -eq 0 ]; then
  echo "catalog-dodo-discount bench: OK"
else
  echo "catalog-dodo-discount bench: $fails FAILURE(S)"
  exit 1
fi
