#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-module-bridge.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/mod-bridge.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

# ⛔ EL ÁRBOL DE PRUEBA MONTA EL DERIVADOR. Sin él, el gate contesta SCOPED y los casos pasan sin
# ejercitar la comprobación que de verdad decide — que es exactamente cómo este banco quedó rojo en
# la pata 277 de un push: la guarda pasó a «no contradicción» y el caso del slug inventado se quedó
# sin nadie que lo cazara. El derivador se construye UNA vez y se pasa por variable.
export GOWORK=off
MCBIN="$(mktemp -u "${TMPDIR:-/workspace/.olivares-tmptest}/mb-bin.XXXXXX")"
( cd "$ROOT/commercial/commerce-lint" && go build -o "$MCBIN" . ) >/dev/null 2>&1 || {
	echo "test-module-bridge: NO PUDE MIRAR — el derivador no construye" >&2; exit 2; }
export OLIVARES_MODULE_CATALOG_BIN="$MCBIN"

stage_derivation() {
	mkdir -p "$TMP/tree/commercial/license-worker/src/catalog" "$TMP/tree/commercial/license-worker/contracts"
	cp "$ROOT/design/PRICING-CANON.md" "$ROOT/design/c13-02-package-view.json" "$TMP/tree/design/"
	cp "$ROOT/commercial/module-package-slugs.json" "$TMP/tree/commercial/"
	cp "$ROOT/commercial/license-worker/src/catalog/module-slug-package.json" \
		"$TMP/tree/commercial/license-worker/src/catalog/"
	cp -r "$ROOT/commercial/commerce-lint" "$TMP/tree/commercial/"
	cp "$ROOT/scripts/module-catalog-go.sh" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/module-catalog-go.sh"
}

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/commercial" "$TMP/tree/scripts"
  cp "$ROOT/design/VOCABULARIO-MODULOS-2026-08-08.md" "$TMP/tree/design/"
  cp "$ROOT/commercial/module-slug-package.json" "$TMP/tree/commercial/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-module-bridge.sh"
  stage_derivation
}
run() { OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-module-bridge.sh" >/dev/null 2>"$TMP/err"; }

stage
if run; then ok "live map is CLEAN"; else bad "live map should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/commercial/module-slug-package.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p))
d["entries"]=[e for e in d["entries"] if e["slug"]!="iso42001"]
json.dump(d, open(p,"w"))
PY
if run; then bad "dropping iso42001 stayed CLEAN"; else ok "JSON missing a VOCABULARIO slug is a finding"; fi

stage
python3 - "$TMP/tree/commercial/module-slug-package.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p))
d["entries"].append({"slug":"invented-addon","package":"enterprise/invented"})
json.dump(d, open(p,"w"))
PY
# ⛔ ESTE CASO MATÓ UN PUSH EN LA PATA 277, y tenía razón. La guarda pasó de igualdad exacta con el
# VOCABULARIO —necesaria de retirar: esa tabla es una medida fechada y el canon crece— a «no
# contradicción», y con eso un slug INVENTADO pasaba a reportarse como crecimiento. La cura no es
# volver al pin: es preguntar a la AUTORIDAD. Un slug que el canon no vende hace que el mapa deje de
# ser la derivación, y eso lo mide `-module-catalog=check`. Ahora el testigo lo dice.
if run; then bad "invented slug stayed CLEAN"; else
  grep -qE 'canon derivation|derivación del canon' "$TMP/err" \
    && ok "JSON slug the canon does not sell is a finding, BY THE DERIVATION" \
    || ok "JSON slug not in VOCABULARIO is a finding ($(head -1 "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/commercial/module-slug-package.json"
if run; then bad "missing JSON stayed CLEAN"; else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing JSON is COULD NOT LOOK"
  else bad "missing JSON should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-module-bridge selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
