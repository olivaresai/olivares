#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c13-05-catalog-diverge-prep.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c1305div.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

# Built once and handed in; see the note in test-c13-02-worker-map.sh. Without the deriver in the
# staged tree the gate answers NOT APPLICABLE and every case below would pass without exercising it.
export GOWORK=off
MCBIN="$(mktemp -u "${TMPDIR:-/workspace/.olivares-tmptest}/mc-bin.XXXXXX")"
( cd "$ROOT/commercial/commerce-lint" && go build -o "$MCBIN" . ) >/dev/null 2>&1 || {
	echo "no pude construir el derivador: la bateria mediria NOT APPLICABLE" >&2; exit 2; }
export OLIVARES_MODULE_CATALOG_BIN="$MCBIN"
stage_derivation() {
	mkdir -p "$TMP/tree/commercial/license-worker/src/catalog"
	cp "$ROOT/design/PRICING-CANON.md" "$TMP/tree/design/"
	cp "$ROOT/design/c13-02-package-view.json" "$TMP/tree/design/"
	cp "$ROOT/commercial/module-package-slugs.json" "$TMP/tree/commercial/"
	cp "$ROOT/commercial/license-worker/src/catalog/module-slug-package.json" \
		"$TMP/tree/commercial/license-worker/src/catalog/"
	cp -r "$ROOT/commercial/commerce-lint" "$TMP/tree/commercial/"
	cp "$ROOT/scripts/module-catalog-go.sh" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/module-catalog-go.sh"
}

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts" "$TMP/tree/commercial"
  cp "$ROOT/design/c13-05-catalog-diverge-prep-2026-08-20.json" "$TMP/tree/design/"
  cp "$ROOT/design/C13-05-CATALOG-DIVERGE-PREP-2026-08-20.md" "$TMP/tree/design/"
  cp "$ROOT/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" "$TMP/tree/design/"
  cp "$ROOT/commercial/module-slug-package.json" "$TMP/tree/commercial/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c13-05-catalog-diverge-prep.sh"
  stage_derivation
}
run() {
  local rc=0
  unset OLIVARES_ENT_DIR || true
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c13-05-catalog-diverge-prep.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "hub-safe diverge HOLD is CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

# ⛔ LOS DOS CASOS QUE HABÍA AQUÍ EXIGÍAN QUE LA DIVERGENCIA SIGUIERA EXISTIENDO. Uno ponía
# `overlay_matches_sold: true` y esperaba rojo; el otro añadía «catalog matches sold» al documento y
# esperaba rojo. Los dos defendían el ESTADO (hay divergencia) en vez de la PROPIEDAD, así que el día
# que alguien curó la divergencia la batería la habría llamado regresión. Se sustituyen por casos
# sobre lo que sí decide hoy.

stage
# Un veredicto sobre el overlay reaparece en un gate que no lee el overlay.
python3 - "$TMP/tree/design/c13-05-catalog-diverge-prep-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
assert "overlay_matches_sold" not in d, "control de mutacion: el campo ya estaba"
d["overlay_matches_sold"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (a verdict about a subject this gate never reads) is killed"
else bad "overlay verdict stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
# El documento se atribuye el cierre del lote, que esta mitad no puede certificar.
printf '\nC13-05 cerrado\n' >> \
  "$TMP/tree/design/C13-05-CATALOG-DIVERGE-PREP-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (doc claims the lote close) is killed"
else bad "doc close stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
# Un HOLD de empaquetado que nombra un slug que el mapa SÍ lleva: la condición está curada y el
# HOLD escrito es teatro. Es la dirección que el pin de dos nombres no podía comprobar.
printf '\nhold-slug: content-firewall\n' >> \
  "$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
  grep -q 'content-firewall' "$TMP/err" \
    && ok "mutant (cured HOLD still written) is killed BY NAME" \
    || bad "killed, but the message does not name content-firewall ($(cat "$TMP/err"))"
else bad "cured HOLD stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
# Y el mapa vendido deja de ser la derivación del canon: lo caza la comparación, no un literal.
python3 - "$TMP/tree/commercial/module-slug-package.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
before = len(d["entries"])
d["entries"] = [e for e in d["entries"] if e["slug"] != "credential-minter"]
assert len(d["entries"]) == before - 1, "control de mutacion: credential-minter no estaba"
json.dump(d, open(p, "w", encoding="utf-8"), indent=2)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
  grep -q 'differs from the canon derivation' "$TMP/err" \
    && ok "mutant (sold map no longer the derivation) is killed BY THE DERIVATION" \
    || bad "killed, but not by the derivation ($(cat "$TMP/err"))"
else bad "drifted map stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/c13-05-catalog-diverge-prep-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["overlay_remeasured_in_this_gate"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (overlay remasure leaked) is killed"
else bad "overlay remasure stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/commercial/module-slug-package.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["entries"] = [e for e in d["entries"] if e.get("slug") != "iso42001"]
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (sold map lost iso42001) is killed"
else bad "sold-map mutant stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/design/c13-05-catalog-diverge-prep-2026-08-20.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing JSON is COULD NOT LOOK"
else bad "missing JSON rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live pin stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-c13-05-catalog-diverge-prep selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
