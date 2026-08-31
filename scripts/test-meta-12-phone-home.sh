#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ⛔ ESTA BATERÍA PROBABA LA POLARIDAD CONTRARIA, Y ESO NO ES UN DETALLE: su caso
# «mutant (applied to docs-site) is killed» exigía que poner `applied_to_docs_site = true`
# devolviera rc 1 — es decir, **certificaba que el trabajo correcto enrojeciera**. Era coherente el
# 2026-08-20 (META-12 redactaba, C09-05 aplicaba en otro carril) y quedó al revés el día en que
# C09-05 se ejecutó. Se invierte con su gate en el mismo PR (2026-08-28).
#
# Y se añade lo que faltaba desde el principio: el mutante del ÁRBOL. Antes todos los mutantes
# tocaban un JSON o una cabecera; ninguno tocaba una página. Un gate cuyo único sujeto es un flag
# escrito a mano no puede distinguir «aplicado» de «declarado aplicado».
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-meta-12-phone-home.sh"
RATCHET="$ROOT/scripts/check-phone-home-claims.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/meta12.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

PAGE=docs-site/src/content/docs/how-to/air-gap-install.md

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts" "$TMP/tree/docs" \
           "$TMP/tree/docs-site/src/content/docs/how-to"
  cp "$ROOT/design/meta-12-phone-home-2026-08-20.json" "$TMP/tree/design/"
  cp "$ROOT/design/META-12-PHONE-HOME-REDACCION-2026-08-20.md" "$TMP/tree/design/"
  cp "$ROOT/README.md" "$ROOT/LICENSING.md" "$TMP/tree/"
  cp "$CHECK" "$RATCHET" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-meta-12-phone-home.sh" \
           "$TMP/tree/scripts/check-phone-home-claims.sh"
  # Una página viva con la redacción APLICADA — la de `origin/main` después de C09-05.
  cat > "$TMP/tree/$PAGE" <<'PAGE_EOF'
# Air-gap install
The engine makes no mandatory outbound calls at boot, so nothing inside the gap
reaches the internet. The vendor is reached on the online side: building the bundle
downloads the release, and the subscription is the credential for the add-ons.
PAGE_EOF
  printf '0\t%s\n' "$PAGE" > "$TMP/tree/docs/phone-home-claims-baseline.txt"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" TMPDIR="$_tmp_base" \
    bash "$TMP/tree/scripts/check-meta-12-phone-home.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "written AND applied, docs-site at zero, is CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/meta-12-phone-home-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["applied_to_docs_site"] = False
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (declares NOT applied) is killed"
else bad "not-applied stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/meta-12-phone-home-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["fran_asked"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (owner re-asked) is killed"
else bad "fran_asked stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# ⛔ Y EL MUTANTE SE COMPRUEBA APLICADO ANTES DE JUZGAR AL GATE. Un mutante que no se inyecta
# acusa al gate de ciego siendo el mutante el que falló: aquí pasó de verdad en la primera
# corrida —la primera versión de este `sed` casaba y la del gate anclaba en `docs-site**`, así que
# el caso salía FAIL por dos razones a la vez y sólo una era real.
stage
_doc="$TMP/tree/design/META-12-PHONE-HOME-REDACCION-2026-08-20.md"
sed -i '0,/^\*\*REDACTADO/s/^\*\*REDACTADO.*$/**REDACTADO. NO APLICADO al docs-site.**/' "$_doc"
if grep -q '^\*\*REDACTADO\. NO APLICADO al docs-site\.\*\*$' "$_doc"; then
  run
  if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (header claims NOT applied) is killed"
  else bad "doc not-applied stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi
else
  bad "el mutante de cabecera NO se inyectó — el caso no dice nada del gate"
fi

# ⭐ EL MUTANTE DEL ÁRBOL — el que faltaba, y el único que distingue «aplicado» de «declarado».
# Se reintroduce la promesa RETIRADA en una página viva, con el literal exacto que recibió
# por correo el 2026-08-28. Si esto no enrojece, el gate mide un flag y no el producto.
stage
printf '\nThe licence never phones home, and validates fully offline.\n' >> "$TMP/tree/$PAGE"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'air-gap-install' "$TMP/err"; then
  ok "mutant (the retired promise is back on a live page) is killed and named"
else bad "tree mutant stayed rc=$(cat "$TMP/rc") ($(head -4 "$TMP/err"))"; fi

# El mismo mutante en otro idioma: el gate heredó el patrón multilingüe, no una copia inglesa.
stage
printf '\n## フォンホームゼロ\n' >> "$TMP/tree/$PAGE"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (promise re-added in Japanese) is killed"
else bad "ja tree mutant stayed rc=$(cat "$TMP/rc") ($(head -4 "$TMP/err"))"; fi

# ⭐ LA MITAD POSITIVA (F2 del contraste): borrar el ANCLA no puede seguir siendo CLEAN. Antes este
# gate sólo comprobaba ausencia léxica, así que arrancar entera la explicación correcta pasaba.
stage
_lic="$TMP/tree/LICENSING.md"
python3 - "$_lic" <<'PY2'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
old = "Downloading what you paid for does."
assert s.count(old) == 1, s.count(old)
open(p, "w", encoding="utf-8").write(s.replace(old, "Nothing else happens."))
PY2
if grep -q 'Nothing else happens' "$_lic"; then
  run
  if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'Downloading what you paid for does' "$TMP/err"; then
    ok "mutant (LICENSING.md loses the commercial half) is killed and names the missing wording"
  else bad "anchor mutant stayed rc=$(cat "$TMP/rc") ($(head -3 "$TMP/err"))"; fi
else
  bad "el mutante del ancla NO se inyectó — el caso no dice nada del gate"
fi

# Y sin el canon no se juzga: «no he podido mirar», nunca «aplicado».
stage
rm -f "$TMP/tree/LICENSING.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing LICENSING.md is COULD NOT LOOK, never CLEAN"
else bad "missing LICENSING rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/design/meta-12-phone-home-2026-08-20.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing JSON is COULD NOT LOOK"
else bad "missing JSON rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

# Sin el trinquete no hay patrón: «no he podido mirar», nunca «está aplicado».
stage
rm -f "$TMP/tree/scripts/check-phone-home-claims.sh"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing ratchet is COULD NOT LOOK, never CLEAN"
else bad "missing ratchet rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

# Y sin ninguna ruta de docs-site en la línea base tampoco se juzga: sin control positivo, un
# conjunto vacío y «todo limpio» se escriben igual.
stage
: > "$TMP/tree/docs/phone-home-claims-baseline.txt"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "empty baseline is COULD NOT LOOK, never CLEAN"
else bad "empty baseline rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live pin stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-meta-12-phone-home selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
