#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md.
#
# test-docs-redirects.sh — selftest de check-docs-redirects.sh.
#
# ⛔ Lleva CONTROL POSITIVO y CONTROLES NEGATIVOS. Un selftest que solo prueba mutantes no
#    distingue "la puerta caza el defecto" de "la puerta siempre dice 1"; y uno que solo prueba
#    el caso sano no distingue "limpio" de "no he mirado".
set -uo pipefail
ROOT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
GATE="$ROOT/scripts/check-docs-redirects.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/docsred.XXXXXX")" || { echo "test-docs-redirects: COULD NOT LOOK — no TMPDIR" >&2; exit 2; }
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$TMP/docs/reference" "$TMP/docs/start"
printf '# cli\n'        > "$TMP/docs/reference/cli.md"
printf '# quickstart\n' > "$TMP/docs/start/quickstart.md"

pass=0; fail=0
ok()  { printf '  ok    %s\n' "$1"; pass=$((pass+1)); }
bad() { printf '  FAIL  %s\n' "$1"; fail=$((fail+1)); }
corre() { OLIVARES_DOCS_REDIRECTS="$1" OLIVARES_DOCS_CONTENT="$TMP/docs" bash "$GATE" >/dev/null 2>&1; printf '%s' "$?"; }

# control positivo
printf '/cli  /reference/cli/  301\n/quickstart  /start/quickstart/  301\n' > "$TMP/sano"
[ "$(corre "$TMP/sano")" = 0 ] && ok "control: valid rules with existing targets -> CLEAN (0)" \
                               || bad "valid input did not return 0"

# M1: destino inexistente -> 1
printf '/cli  /reference/cli/  301\n/viejo  /reference/NO-EXISTE/  301\n' > "$TMP/m1"
[ "$(corre "$TMP/m1")" = 1 ] && ok "mutant: nonexistent target is a FINDING (1)" \
                             || bad "nonexistent target did not return 1"

# M2: regla mas larga que el limite de la plataforma -> 1
{ printf '/cli  /reference/cli/  301\n/'; head -c 1200 /dev/zero | tr '\0' 'a'; printf '  /reference/cli/  301\n'; } > "$TMP/m2"
[ "$(corre "$TMP/m2")" = 1 ] && ok "mutant: rule exceeding 1000 characters is a FINDING (1)" \
                             || bad "long rule did not return 1"

# M3: fichero ilegible -> 2, nunca 0
[ "$(corre "$TMP/NO-EXISTE")" = 2 ] && ok "missing rules file: COULD NOT LOOK (2)" \
                                    || bad "missing file did not return 2"

# M4: fichero SOLO con comentarios -> 2, nunca 0. Cero reglas no es limpieza.
printf '# solo comentarios\n#\n' > "$TMP/m4"
[ "$(corre "$TMP/m4")" = 2 ] && ok "zero rules: COULD NOT LOOK (2), not CLEAN" \
                             || bad "zero rules did not return 2"

# M5 (control negativo): un destino EXTERNO no se comprueba contra el arbol y no debe fallar
printf '/cli  /reference/cli/  301\n/fuera  https://example.invalid/x  301\n' > "$TMP/m5"
[ "$(corre "$TMP/m5")" = 0 ] && ok "control: external target is skipped, not a finding" \
                             || bad "external target returned nonzero"

# M6 (control negativo): el comodin :splat se normaliza y NO se lee como pagina
printf '/cli/*  /reference/cli/:splat  301\n' > "$TMP/m6"
[ "$(corre "$TMP/m6")" = 0 ] && ok "control: :splat wildcard is not mistaken for a page" \
                             || bad ":splat returned nonzero"

printf 'check-docs-redirects selftest: %s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
exit 0
