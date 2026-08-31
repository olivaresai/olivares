#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Bateria de scripts/check-classify-paths-parity.sh. Fixtures propios, sin red y sin repo.
set -u
SUT="${SUT:-scripts/check-classify-paths-parity.sh}"
PASS=0; FAIL=0
TMP=$(mktemp -d "${TMPDIR:-/tmp}/tcp.XXXXXX") || exit 2
trap 'rm -rf "$TMP"' EXIT
check(){ local n="$1" e="$2" g="$3"
  if [ "$e" = "$g" ]; then PASS=$((PASS+1)); printf 'ok   %-62s %s\n' "$n" "$g"
  else FAIL=$((FAIL+1)); printf 'FAIL %s esperaba [%s], dio [%s]\n' "$n" "$e" "$g"; fi; }

wf(){ # $1 = fichero destino, $2 = items de paths-ignore (uno por linea), $3 = rama del case
  { echo "on:"; echo "  push:"; echo "    paths-ignore:"
    printf '%s\n' "$2" | while IFS= read -r x; do [ -n "$x" ] && echo "      - '$x'"; done
    echo "jobs:"; echo "  classify:"; echo "    steps:"; echo "      - run: |"
    echo "          case \"\$f\" in"; echo "            $3) ;;"
    echo "            *) journal_only=false; break ;;"; echo "          esac"; } > "$1"; }

mkdir -p "$TMP/ok/.github/workflows" "$TMP/mal/.github/workflows" "$TMP/com/.github/workflows"
wf "$TMP/ok/.github/workflows/mainline-ci.yml"  "$(printf 'a/**\nb/**\n')" "a/* | b/*"
wf "$TMP/mal/.github/workflows/mainline-ci.yml" "$(printf 'a/**\n')"       "a/* | b/*"
# el mismo caso bueno, pero con un COMENTARIO dentro de la lista: la primera version paraba ahi
wf "$TMP/com/.github/workflows/mainline-ci.yml" "$(printf 'a/**\nb/**\n')" "a/* | b/*"
python3 - "$TMP/com/.github/workflows/mainline-ci.yml" <<'PYEOF'
import sys
p=sys.argv[1]; s=open(p).read()
open(p,'w').write(s.replace("      - 'b/**'","      # razon de b, escrita entre los items\n      - 'b/**'",1))
PYEOF

corre(){ OLIVARES_ROOT="$1" bash "${2:-$SUT}" > "$TMP/out" 2>&1; echo $?; }
check "(1) las dos listas coinciden -> rc 0" 0 "$(corre "$TMP/ok")"
check "(1b) y lo dice" 0 "$( grep -q 'dicen lo mismo' "$TMP/out"; echo $? )"
check "(2) una familia solo en el case -> rc 1" 1 "$(corre "$TMP/mal")"
check "(2b) y NOMBRA cual y de que lado" 0 "$( grep -q 'solo en el `case` de classify' "$TMP/out" && grep -q 'b/\*' "$TMP/out"; echo $? )"
check "(3) un COMENTARIO entre los items no rompe la lectura" 0 "$(corre "$TMP/com")"
check "(4) sin fichero de workflow -> 2, no 0" 2 "$(corre "$TMP/no-existe")"

# 4b · ⛔ TOLERANCIA AL REFORMATEO, y es la fila que faltaba (A-02 de the reviewer): el extractor usa
#      `^ *paths-ignore:` a proposito. Sin un fixture con OTRA sangria, un mutante que la fije a la
#      real (4 espacios) no mata ninguna fila y la bateria certifica una tolerancia que no prueba.
mkdir -p "$TMP/sangria/.github/workflows"
wf "$TMP/sangria/.github/workflows/mainline-ci.yml" "$(printf 'a/**\nb/**\n')" "a/* | b/*"
python3 - "$TMP/sangria/.github/workflows/mainline-ci.yml" <<'PYEOF'
import sys,re
p=sys.argv[1]; s=open(p).read()
# el mismo contenido con DOS espacios mas en el bloque de paths-ignore y sus items
s=s.replace("    paths-ignore:","      paths-ignore:").replace("      - '","        - '")
open(p,'w').write(s)
PYEOF
check "(4b) el bloque con OTRA sangria se lee igual" 0 "$(corre "$TMP/sangria")"
check "(4c) y da el mismo veredicto que con la sangria de siempre" 0 \
  "$( grep -q 'dicen lo mismo' "$TMP/out"; echo $? )"

# 4d · MUTANTE · fijar la sangria a la real: el fixture de 4b tiene que MORIR con el
sed 's|/\^ \*paths-ignore:/|/^    paths-ignore:/|' "$SUT" > "$TMP/msang.sh"
check "(4e) el mutante de sangria fija REALMENTE difiere" 0 "$( cmp -s "$SUT" "$TMP/msang.sh" && echo 1 || echo 0 )"
M4=$(corre "$TMP/sangria" "$TMP/msang.sh")
check "(4f) MUTANTE 'sangria fija' es CAZADO por el fixture desplazado" 0 \
  "$( [ "$M4" = "2" ] && echo 0 || echo "dio $M4" )"
check "(4g) y por su MENSAJE: dice que no encuentra la lista" 0 \
  "$( grep -q 'no encuentro la lista' "$TMP/out"; echo $? )"

# 5 · MUTANTE · dejar de saltar comentarios: el caso (3) se vuelve un falso positivo permanente
sed 's|f&&/\^ \*#/{next} ||' "$SUT" > "$TMP/m1.sh"
check "(5a) el mutante de comentarios REALMENTE difiere" 0 "$( cmp -s "$SUT" "$TMP/m1.sh" && echo 1 || echo 0 )"
M1=$(corre "$TMP/com" "$TMP/m1.sh")
check "(5b) MUTANTE 'no salto comentarios' es CAZADO por su rc" 0 "$( [ "$M1" = "1" ] && echo 0 || echo "dio $M1" )"
check "(5c) y por su MENSAJE: acusa de deriva una lista que SI coincide" 0 \
  "$( grep -q 'han DERIVADO' "$TMP/out"; echo $? )"

# 6 · MUTANTE · comparar solo en un sentido: una familia de mas en paths-ignore pasaria
sed 's|if \[ -z "$SOLO_CAS" \] && \[ -z "$SOLO_IGN" \]; then|if [ -z "$SOLO_CAS" ]; then|' "$SUT" > "$TMP/m2.sh"
mkdir -p "$TMP/inv/.github/workflows"
wf "$TMP/inv/.github/workflows/mainline-ci.yml" "$(printf 'a/**\nb/**\nc/**\n')" "a/* | b/*"
check "(6a) el mutante de un solo sentido REALMENTE difiere" 0 "$( cmp -s "$SUT" "$TMP/m2.sh" && echo 1 || echo 0 )"
check "(6b) el ORIGINAL caza la familia de mas en paths-ignore" 1 "$(corre "$TMP/inv")"
M2=$(corre "$TMP/inv" "$TMP/m2.sh")
check "(6c) MUTANTE 'un solo sentido' la deja pasar" 0 "$( [ "$M2" = "0" ] && echo 0 || echo "dio $M2" )"

echo
echo "check-classify-paths-parity selftest: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
