#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# META-12: the substitution for "zero phone-home" is written AND applied.
# Three answers: 0 CLEAN · 1 finding · 2 could not look.
#
# ⛔ ESTE GATE ESTUVO INVERTIDO, Y NO POR DESCUIDO: el 2026-08-20 exigía el literal
# `NO APLICADO al docs-site` y `applied_to_docs_site == false`, y su batería **mataba el mutante
# que ponía `true`**. Era correcto ese día —META-12 REDACTABA y C09-05 APLICABA, en otro carril— y
# quedó al revés en el instante en que C09-05 se ejecutó (2026-08-28): el gate probaba que
# el trabajo correcto enrojeciera. Un gate que codifica el estado viejo como esperado no envejece
# en silencio: **bloquea su propio arreglo**.
#
# Y EL CAMBIO DE FONDO, que es lo que evita repetirlo: antes comprobaba un FLAG en un JSON, o sea
# una afirmación sobre el árbol escrita a mano. Ahora comprueba el ÁRBOL — que el docs-site vivo
# está a CERO promesas absolutas—, y lo hace **llamando a `check-phone-home-claims.sh`** en vez de
# copiar su patrón: un hecho escrito en dos sitios deriva, y aquí el hecho es «qué forma tiene una
# promesa absoluta», que ya vive allí con su batería multilingüe.
set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-meta-12-phone-home: FAIL — $*" >&2; exit 1; }
cannot() { say "check-meta-12-phone-home: COULD NOT LOOK — $*" >&2; exit 2; }

AQUI="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd -P)"
ROOT="${OLIVARES_ROOT:-$(cd "$AQUI/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_META12_JSON:-design/meta-12-phone-home-2026-08-20.json}"
DOC="${OLIVARES_META12_DOC:-design/META-12-PHONE-HOME-REDACCION-2026-08-20.md}"
README="${OLIVARES_META12_README:-README.md}"
LICENCIA="${OLIVARES_META12_LICENSING:-LICENSING.md}"
RATCHET="${OLIVARES_META12_RATCHET:-$AQUI/check-phone-home-claims.sh}"
SITE="${OLIVARES_META12_SITE:-docs-site/src/content/docs}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$README" ] || cannot "missing $README"
[ -r "$LICENCIA" ] || cannot "missing $LICENCIA — sin el canon no puedo juzgar si se aplicó"
[ -r "$RATCHET" ] || cannot "missing $RATCHET — no puedo mirar el árbol sin el patrón"
[ -d "$SITE" ] || cannot "missing $SITE"
command -v python3 >/dev/null || cannot "no python3"

# ⛔ SE JUZGA **LA LÍNEA DE CABECERA**, NO EL FICHERO ENTERO, y esa distinción tiene su propio
# coste medido: el fichero explica el arreglo CITANDO la cadena vieja («ESTA CABECERA DECÍA "NO
# APLICADO al docs-site"»), así que un `grep` sobre todo el documento no distingue la CITA de la
# AFIRMACIÓN — el mismo defecto que `web/src/features/attestation/components.tsx:634` ya tenía
# escrito para el otro gate. Y anclar en `docs-site\*\*` tampoco valía: la batería lo cazó en su
# primera corrida (el mutante escribía `docs-site.**` y sobrevivía con rc 0).
CABECERA="$(grep -m1 '^\*\*REDACTADO' "$DOC" || true)"
[ -n "$CABECERA" ] || cannot "$DOC no tiene línea de cabecera '**REDACTADO…' que juzgar"
case "$CABECERA" in
  *"NO APLICADO al docs-site"*)
    fail "the header still declares NO APLICADO — the substitution IS applied since 2026-08-28" ;;
esac
case "$CABECERA" in
  *"APLICADO al docs-site"*) : ;;
  *) fail "the header does not declare APLICADO al docs-site: $CABECERA" ;;
esac
grep -q 'community_line' "$DOC" || fail "prepare doc lost community_line"
grep -q 'commercial_line' "$DOC" || fail "prepare doc lost commercial_line"
grep -q 'no mandatory telemetry' "$DOC" \
  || fail "prepare doc lost the README.md:44 canonical wording"
# ⛔ ESTA REFERENCIA DECÍA «README.md:44» Y EL LITERAL YA NO ESTÁ AHÍ — señalado por el contraste
# `sol max` (F8): hoy vive en `README.md:41`. Se comprueba por CONTENIDO, no por número de línea:
# una cita por línea envejece en silencio en cuanto alguien añade un párrafo encima.
grep -q 'no mandatory telemetry' "$README" \
  || fail "$README lost the canonical wording this lote cites"

# ⛔⛔ LA MITAD POSITIVA, QUE FALTABA POR COMPLETO. Hallazgo ALTO del contraste (F2): este gate sólo
# comprobaba AUSENCIA léxica, así que **borrar entera la explicación correcta de una página seguía
# siendo CLEAN**. Un gate que sólo prohíbe no puede ver una ausencia — es literalmente lo que el
# backlog registra de `commerce-lint` en C09-10. Lo que se puede comprobar sin leer siete idiomas
# es que el ANCLA sigue en pie: si `LICENSING.md` pierde cualquiera de las dos mitades firmadas, lo
# que se propagó a las superficies deja de tener fuente y la sustitución no está «aplicada», está
# huérfana.
#
# ⚠ Y LO QUE ESTO SIGUE SIN PROBAR, dicho en vez de tapado: que la prosa de cada página diga bien
# lo que el canon firma. Eso necesita una LECTURA —la que hizo el contraste `sol max` y encontró la
# desviación japonesa de `こちらから`—, no un `grep`. Este gate cubre ancla + ausencia; el sentido
# es de la revisión.
# ⚠ Y SE COMPARA CON LOS SALTOS DE LÍNEA PLANCHADOS, no línea a línea: la frase firmada está
# ENVUELTA en el fichero («…**Verifying a\nlicence never calls anyone…»), así que un `grep -q` con
# la frase entera no casa nunca y el gate habría enrojecido sobre un `LICENSING.md` intacto. Lo
# comprobé en la primera corrida: FAIL con el canon delante y correcto.
_licencia_plano="$(tr '\n' ' ' < "$LICENCIA" | tr -s ' ')"
_falta() {
  case "$_licencia_plano" in
    *"$1"*) : ;;
    *) fail "$LICENCIA lost the signed wording: «$1»" ;;
  esac
}
_falta 'Verifying a licence never calls anyone.'
_falta 'Downloading what you paid for does.'
_falta 'Phone-home is approved for licence issuance and updates.'
_falta 'no mandatory telemetry and no control-plane egress by default.'
# Las formas prohibidas viven en el JSON que este guion ya lee y que el export NO
# publica. Escribirlas aqui metia el nombre propio en el arbol publico; leerlas de
# alli es el mismo patron en el unico sitio donde su literal no es una fuga.
_reask="$(python3 -c 'import json,sys;d=json.load(open(sys.argv[1],encoding="utf-8"));print("|".join(d["doc_must_not_reask"]))' "$JSON")" \
  || cannot "$JSON lost doc_must_not_reask"
[ -n "$_reask" ] || cannot "$JSON has an empty doc_must_not_reask"
if grep -qiE "$_reask" "$DOC"; then
  fail "prepare doc re-asks the owner (regla 9)"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-meta-12-phone-home: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-meta-12-phone-home: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"JSON is not readable: {e}")

def want(key, value):
    got = data.get(key)
    if got != value:
        fail(f"{key} is {got!r}, want {value!r}")

want("lote", "META-12")
want("fran_asked", False)
want("applied_to_docs_site", True)
want("community_keeps_agpl_truth", True)
want("commercial_line_replaces_zero_phone_home", True)
# ⛔ ESTO EXIGÍA «README.md:44» Y EL LITERAL ESTÁ HOY EN `README.md:41` — señalado por el
# contraste `sol max` (F8). Una cita por número de línea envejece en silencio en cuanto alguien
# añade un párrafo encima, y el gate seguiría verde porque sólo compara la CADENA del JSON con
# ella misma. Se guarda el fichero, y la frase se comprueba por contenido más arriba.
want("canonical_source", "README.md")
want("signed_wording_source", "LICENSING.md:166-176")
print("json-ok")
PY

# ── EL ÁRBOL, que es la mitad que faltaba ────────────────────────────────────────────────────
# «Aplicado» significa: el docs-site VIVO no hace ninguna promesa absoluta, en ninguno de los
# siete locales. Se mide con el trinquete a repository gate acotado a esa superficie y con una línea base
# de CEROS generada aquí — deny-closed: cualquier cuenta > 0 sube y enrojece. No se copia su
# patrón; se le llama, para que exista una sola definición de «promesa absoluta».
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base" || cannot "no puedo crear $_tmp_base"
BASE_TMP="$(mktemp "$_tmp_base/meta12-base.XXXXXX")" || cannot "mktemp falló"
trap 'rm -f "$BASE_TMP"' EXIT
# Las rutas del docs-site que la línea base real ya vigila, forzadas a 0. Sin ellas el trinquete
# vería el conjunto vacío y contestaría «NO HE PODIDO MIRAR», que es lo correcto por su parte.
REAL_BASE="${OLIVARES_PHONEHOME_BASELINE:-docs/phone-home-claims-baseline.txt}"
if [ -r "$REAL_BASE" ]; then
  awk -F'\t' -v s="$SITE" '$2 ~ "^" s "/" { printf "0\t%s\n", $2 }' "$REAL_BASE" > "$BASE_TMP"
fi
[ -s "$BASE_TMP" ] || cannot "no hay ninguna ruta de $SITE en $REAL_BASE — sin control positivo no juzgo"

_rc=0
OLIVARES_CLONE="$ROOT" \
OLIVARES_PHONEHOME_DIRS="$SITE" \
OLIVARES_PHONEHOME_FILES="$README" \
OLIVARES_PHONEHOME_BASELINE="$BASE_TMP" \
  bash "$RATCHET" > "$BASE_TMP.out" 2>&1 || _rc=$?
case "$_rc" in
  0) : ;;
  1) say "check-meta-12-phone-home: FAIL — la sustitución NO está aplicada en $SITE:" >&2
     sed 's/^/    /' "$BASE_TMP.out" >&2
     rm -f "$BASE_TMP.out"
     exit 1 ;;
  *) say "check-meta-12-phone-home: COULD NOT LOOK — el trinquete salió $_rc:" >&2
     sed 's/^/    /' "$BASE_TMP.out" >&2
     rm -f "$BASE_TMP.out"
     exit 2 ;;
esac
rm -f "$BASE_TMP.out"

say "check-meta-12-phone-home: CLEAN — substitution written AND applied to $SITE; owner not asked."
exit 0
