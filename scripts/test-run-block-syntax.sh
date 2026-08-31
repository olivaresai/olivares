#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# test-run-block-syntax.sh — a repository gate: bateria de check-run-block-syntax.sh.
#
# ⛔ EL PRIMER CASO ES EL DEFECTO REAL DE HOY, no uno inventado: el `if … else …` sin `fi` que mato
#    al job `hook-only-legs` en su estreno (run 33284926144). Un gate se prueba con el fallo que ya
#    ocurrio.
# ⛔ Y LA DIRECCION DE NO DISPARO ESTA AQUI A PROPOSITO: sin ella, un gate que rechazara TODO
#    pasaria todas las casillas de rechazo. Por eso hay bloques validos, y en particular uno con
#    `${{ }}` DENTRO de un heredoc — la forma que un parser ingenuo rompe.
set -uo pipefail
LC_ALL=C
export LC_ALL
RAIZ="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)" || exit 2
SUT="$RAIZ/scripts/check-run-block-syntax.sh"
[ -r "$SUT" ] || { echo "test-run-block-syntax: ⛔ NO HE PODIDO MIRAR: no encuentro $SUT" >&2; exit 2; }
PASS=0; FAIL=0
check() { if [ "$2" = "$3" ]; then PASS=$((PASS+1)); printf 'ok   %-56s rc=%s\n' "$1" "$3"
	else FAIL=$((FAIL+1)); printf 'FAIL %-56s esperaba rc=%s, dio rc=%s\n' "$1" "$2" "$3"; fi; }
TMP="$(mktemp -d "${TMPDIR:-/tmp}/rbs.XXXXXX")" || exit 2
trap 'rm -rf "$TMP"' EXIT
corre() { OLIVARES_RUNBLOCK_WFDIR="$1" OLIVARES_RUNBLOCK_MIN="${2:-1}" bash "$SUT" >"$TMP/out" 2>&1; echo $?; }
wf() { mkdir -p "$TMP/$1"; cat > "$TMP/$1/w.yml"; printf '%s' "$TMP/$1"; }

# 1 · EL DEFECTO REAL: `if … else …` sin `fi`
d="$(wf falta_fi <<'EOF'
name: t
on: [push]
jobs:
  j:
    runs-on: ubuntu-latest
    steps:
      - name: paso con if sin cerrar
        id: roto
        run: |
          if [ -n "$X" ]; then
            echo a
          else
            echo b
EOF
)"
check "(1) if/else sin \`fi\` -> 1" 1 "$(corre "$d")"
grep -q 'roto' "$TMP/out" && { PASS=$((PASS+1)); printf 'ok   %-56s\n' "(1b) y NOMBRA el paso por su id"; } \
	|| { FAIL=$((FAIL+1)); printf 'FAIL %-56s\n' "(1b) no nombra el paso"; }

# 2 · NO DISPARO: un bloque valido
d="$(wf valido <<'EOF'
name: t
on: [push]
jobs:
  j:
    runs-on: ubuntu-latest
    steps:
      - run: |
          if [ -n "$X" ]; then echo a; else echo b; fi
EOF
)"
check "(2) bloque valido -> 0" 0 "$(corre "$d")"

# 3 · NO DISPARO con `${{ }}` DENTRO de un heredoc — la forma que rompe a un parser ingenuo
d="$(wf expr_heredoc <<'EOF'
name: t
on: [push]
jobs:
  j:
    runs-on: ubuntu-latest
    steps:
      - run: |
          cat <<FIN
          ref=${{ github.ref }} sha=${{ github.sha }}
          FIN
          echo "${{ vars.ALGO }}" | tr -d '\n'
EOF
)"
check "(3) \${{ }} dentro de heredoc -> 0, no rompe" 0 "$(corre "$d")"

# 4 · una expresion que ocupa el comando ENTERO tampoco rompe
d="$(wf expr_sola <<'EOF'
name: t
on: [push]
jobs:
  j:
    runs-on: ubuntu-latest
    steps:
      - run: ${{ vars.COMANDO }}
EOF
)"
check "(4) expresion como comando entero -> 0" 0 "$(corre "$d")"

# 5 · shell NO-bash: se OMITE y se declara, nunca se da por limpio en silencio
d="$(wf pwsh <<'EOF'
name: t
on: [push]
jobs:
  j:
    runs-on: ubuntu-latest
    steps:
      - shell: pwsh
        run: |
          if ($true) { Write-Host "esto no es bash" }
      - run: echo ok
EOF
)"
check "(5) shell pwsh: se omite, el resto pasa -> 0" 0 "$(corre "$d")"
grep -q 'omitido:.*pwsh' "$TMP/out" && { PASS=$((PASS+1)); printf 'ok   %-56s\n' "(5b) y DECLARA la omision"; } \
	|| { FAIL=$((FAIL+1)); printf 'FAIL %-56s\n' "(5b) omite en silencio"; }

# 6 · el shell del JOB manda sobre el defecto, y el del PASO sobre el del job
d="$(wf shell_job <<'EOF'
name: t
on: [push]
jobs:
  j:
    runs-on: ubuntu-latest
    defaults:
      run:
        shell: pwsh
    steps:
      - run: |
          if ($true) { Write-Host "job en pwsh" }
      - shell: bash
        run: |
          if [ 1 ]; then echo a
EOF
)"
check "(6) shell del job y del paso: sólo el bash se juzga -> 1" 1 "$(corre "$d")"

# 7 · cero bloques analizables NO es limpio
d="$(wf vacio <<'EOF'
name: t
on: [push]
jobs:
  j:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
EOF
)"
check "(7) cero bloques analizables -> 2, no 0" 2 "$(corre "$d" 1)"

# 8 · YAML ilegible -> 2, jamas limpio
d="$(wf roto_yaml <<'EOF'
jobs:
  j:
   steps:
  - - :
EOF
)"
check "(8) workflow que no parsea -> 2" 2 "$(corre "$d")"

# 9 · directorio inexistente -> 2
check "(9) sin directorio de workflows -> 2" 2 "$(corre "$TMP/no-existe")"

# 10 · el ARBOL REAL sale limpio
check "(10) el repositorio real -> 0" 0 "$( OLIVARES_RUNBLOCK_WFDIR="$RAIZ/.github/workflows" bash "$SUT" >/dev/null 2>&1; echo $? )"

# ── 11-14 · UN ARGUMENTO QUE NO SE HONRA SALE 2 ───────────────────────────────────────────────
# El defecto que los motiva es real y es mio: el 2026-08-30 pase la ruta del arbol de `origin/main`
# como posicional, el guion la ignoro, midio MI worktree y contesto 0. Un sujeto equivocado con un
# veredicto convincente.
check "(11) un posicional -> 2, aunque el arbol sea valido" 2 \
  "$( OLIVARES_RUNBLOCK_WFDIR="$RAIZ/.github/workflows" bash "$SUT" /da/igual >/dev/null 2>&1; echo $? )"
check "(12) y tambien CON OLIVARES_ROOT explicito" 2 \
  "$( OLIVARES_ROOT="$RAIZ" bash "$SUT" "$RAIZ/.github/workflows" >/dev/null 2>&1; echo $? )"
# ⛔ SIN TUBERIA: bajo `pipefail` un `bash "$SUT" x | grep -q …` devuelve el 2 del guion y no el
# veredicto del grep, asi que el caso fallaba midiendo otra cosa. Se captura primero y se juzga
# despues, que es la misma leccion que este carril aprendio con `$?` detras de un `| head`.
OLIVARES_RUNBLOCK_WFDIR="$RAIZ/.github/workflows" bash "$SUT" x >"$TMP/msg" 2>&1 || true
check "(13) el mensaje NOMBRA las DOS variables que si se honran" 0 \
  "$( grep -q OLIVARES_ROOT "$TMP/msg" && grep -q OLIVARES_RUNBLOCK_WFDIR "$TMP/msg"; echo $? )"

# MUTANTE · si alguien retira la guarda, el guion vuelve a IGNORAR el argumento y a contestar 0.
# El mutante tiene que MORIR aqui, y se le exige que muera por esta razon y no por otra.
# ⛔ ESTE CASO ESTABA MAL Y LO CAZO the reviewer (quinta vez que me pasa lo mismo): juzgaba SOLO al
# mutante —«el mutante no sale 2»—, y eso es cierto EXISTA O NO la guarda en el original. Retirando
# la guarda del guion real, 11/12/13 caian y **14 seguia VERDE**: acreditaba una pata que no medía.
# Un mutante acredita la pata que NOMBRA, y para eso tiene que juzgar la DIFERENCIA entre el guion
# con guarda y sin ella, no el comportamiento del mutante a solas.
MUT="$TMP/mutante.sh"
sed '/^if \[ "\$#" -gt 0 \]; then$/,/^fi$/d' "$SUT" > "$MUT"
# Y antes de nada: un `sed` que no casa nada produce un «mutante» IDENTICO al original, y entonces
# el caso compara algo consigo mismo y sale verde sin haber mutado. Se exige que difieran.
check "(14a) el mutante REALMENTE difiere del original" 0 \
  "$( cmp -s "$SUT" "$MUT" && echo 1 || echo 0 )"
check "(14b) MUTANTE 'ignora el posicional' es CAZADO por la DIFERENCIA" 0 \
  "$( OLIVARES_RUNBLOCK_WFDIR="$RAIZ/.github/workflows" bash "$SUT" /da/igual >/dev/null 2>&1; real=$?
      OLIVARES_RUNBLOCK_WFDIR="$RAIZ/.github/workflows" bash "$MUT" /da/igual >/dev/null 2>&1; mut=$?
      if [ "$real" -eq 2 ] && [ "$mut" -ne 2 ]; then echo 0; else echo "real=$real mut=$mut"; fi )"

echo
echo "check-run-block-syntax selftest: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
