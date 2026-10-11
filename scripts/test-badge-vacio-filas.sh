#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-badge-vacio-filas.sh — la batería del trinquete.
#
# ⛔ LA ASERCIÓN QUE LA JUSTIFICA es la que separa los dos conjuntos: un montaje SIN `filas` pero
#    SIN `<EmptyState>` al lado NO cuenta. Si eso se rompe, el gate pasa de 68 a 76 y manda a
#    alguien a tocar ocho ficheros que no pueden superponer nada — y un gate que pide trabajo
#    inútil se desactiva, que es la forma en que estos mueren.
set -u

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

AQUI="$(cd "$(dirname "$0")" && pwd)"
GUION="$AQUI/check-badge-vacio-filas.sh"

# ⛔ TODO se ejercita sobre una COPIA del guion en un arbol de mentira con el LAYOUT POR DEFECTO
#    (`<raiz>/web/src`, `<raiz>/web/badge-vacio-filas.baseline`). Antes se inyectaban rutas por
#    variable, y esas variables eran una palanca que apagaba el gate; retiradas del guion real, la
#    bateria no las necesita: basta con poner los ficheros donde el guion los busca.
FALSO=""
GUION_COPIA=""
# ⛔ La mitad monotona se ejercita con una COPIA en un directorio SIN `.git`: asi el camino de
#    respaldo (`BADGE_ANTERIOR`) es alcanzable sin ningun override que pudiera debilitar el gate
#    real. Dentro del repositorio git siempre contesta y ese camino no existe.
GUION_COPIA=""
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass_count=0
fail_count=0
ok() { pass_count=$((pass_count + 1)); printf 'ok   %-56s %s\n' "$1" "${2:-}"; }
malo() {
	fail_count=$((fail_count + 1))
	printf 'FAIL  %-55s %s\n' "$1" "${2:-}"
}

# Árbol sintético: `web/src/…` porque el guion recorta la ruta contra la raíz del repo.
FALSO="$WORK/falso"
SRC="$FALSO/web/src/features"
mkdir -p "$SRC" "$FALSO/scripts"
cp "$GUION" "$FALSO/scripts/"
GUION_COPIA="$FALSO/scripts/check-badge-vacio-filas.sh"
BASE_FALSA="$FALSO/web/badge-vacio-filas.baseline"

pinta() { # <fichero> <con-filas|sin-filas> <con-vacio|sin-vacio>
	local f="$SRC/$1" filas="" vacio=""
	[ "$2" = "con-filas" ] && filas="        filas={items.length}"
	[ "$3" = "con-vacio" ] && vacio="      <EmptyState title=\"nada\" />"
	cat >"$f" <<FIN
export function V() {
  return (
    <div>
      <ListTruncationBadge
        query={q}
        label="x"
        hint="y"
$filas
      />
$vacio
    </div>
  )
}
FIN
}

# Siempre la COPIA, y las rutas por defecto: es como corre de verdad.
corre() { bash "$GUION_COPIA" >"$WORK/out.txt" 2>&1; printf '%s' "$?"; }
base() { cp "$1" "$BASE_FALSA"; }

echo "== Empty-row badge checks =="

# 1 · el caso que separa los conjuntos: sin `filas` pero SIN vacío → NO cuenta.
pinta a.tsx sin-filas sin-vacio
: >"$BASE_FALSA"
comprueba_rc="$(corre)"
[ "$comprueba_rc" = "0" ] && ok "no filas and NO <EmptyState> => not counted" "rc=0" || { malo "counted a mount that cannot overlap"; cat "$WORK/out.txt" | head -3; }

# 2 · con `filas` y con vacío → tampoco cuenta.
pinta a.tsx con-filas con-vacio
[ "$(corre)" = "0" ] && ok "with filas => not counted" || malo "counted a mount that already passes filas"

# 3 · ⛔ EL CASO DE RIESGO: sin `filas` Y con vacío, y NO está en la baseline => rojo con ruta:línea
pinta a.tsx sin-filas con-vacio
[ "$(corre)" = "1" ] && ok "without filas AND with <EmptyState> => red" "rc=1" || malo "the risky case did NOT block"
grep -q 'NEW' "$WORK/out.txt" && ok "and labels it NEW" || malo "does not label it as new"
grep -qE 'a\.tsx:[0-9]+' "$WORK/out.txt" && ok "and reports file:line" || malo "does not report the line"
grep -q 'Fix' "$WORK/out.txt" && ok "and the exact remedy" || malo "no remedy"

# 4 · con la baseline al día, ese mismo árbol es verde.
printf 'web/src/features/a.tsx\t1\n' >"$BASE_FALSA"
[ "$(corre)" = "0" ] && ok "in the baseline => green" || malo "an up-to-date baseline returned red"

# 5 · ⛔ SUBIR es rojo (dos montajes donde la baseline dice uno).
cat >"$SRC/a.tsx" <<'FIN'
export function V() {
  return (<div>
      <ListTruncationBadge query={a} label="x" hint="y" />
      <ListTruncationBadge query={b} label="x" hint="y" />
      <EmptyState title="nada" />
  </div>)
}
FIN
[ "$(corre)" = "1" ] && ok "increase from 1 to 2 => red" || malo "an increase did not block"
grep -q 'INCREASE' "$WORK/out.txt" && ok "and labels it INCREASE" || malo "does not distinguish an increase from a new entry"

# 6 · ⛔ BAJAR sin actualizar la baseline TAMBIÉN es rojo: si no, el trinquete no aprieta nunca.
pinta a.tsx con-filas con-vacio
[ "$(corre)" = "1" ] && ok "resolve without lowering the baseline => red" || malo "the ratchet does not tighten"
grep -q 'RESOLVED' "$WORK/out.txt" && ok "and says to remove the line" || malo "does not say how to tighten it"

# 8 · ⛔ UN RENOMBRADO NO PUEDE LEERSE COMO UN EMPEORAMIENTO. Mover un fichero produce un NUEVO y
#    un RESUELTO por una edición que no cambió una línea de JSX. Sigue siendo rojo —la baseline
#    tiene que casar con el árbol— pero el mensaje debe DECIRLO y dar la edición exacta, o acusa
#    de dos cosas que no pasaron y se aprende a ignorarlo.
rm -f "$SRC"/*.tsx
pinta renombrado.tsx sin-filas con-vacio
printf 'web/src/features/original.tsx\t1\n' >"$BASE_FALSA"
rc="$(corre)"
[ "$rc" = "1" ] && ok "rename => still red" "rc=1" || malo "a rename did not block"
grep -q 'rename or a redistribution' "$WORK/out.txt" && ok "and NAMES it as a possible rename" || malo "reports it as a regression"
grep -q 'or a resolved case plus a new one' "$WORK/out.txt" && ok "and does NOT claim that nothing was added" || malo "⛔ claims something it cannot know"
grep -q 'THE TOTAL IS UNCHANGED (1)' "$WORK/out.txt" && ok "and cites the unchanged total" || malo "does not say that the total is unchanged"
grep -q -- '- web/src/features/original.tsx' "$WORK/out.txt" && ok "and reports the line to remove" || malo "does not provide the edit"
grep -q -- '+ web/src/features/renombrado.tsx' "$WORK/out.txt" && ok "and the line to add" || malo "does not provide the new line"

# 8 bis · y un empeoramiento REAL no se disfraza de renombrado: el total sube, así que no hay aviso.
cat >"$SRC/renombrado.tsx" <<'FIN'
export function V() {
  return (<div>
      <ListTruncationBadge query={a} label="x" hint="y" />
      <ListTruncationBadge query={b} label="x" hint="y" />
      <EmptyState title="nada" />
  </div>)
}
FIN
corre >/dev/null
grep -q 'rename or a redistribution' "$WORK/out.txt" && malo "⛔ called an actual regression a rename" || ok "an actual regression is NOT disguised" "the total increases"

# 8 ter · ⛔⛔ EL CASO QUE ROMPE EL AVISO DE RENOMBRADO, y que mi primer negativo NO cubria: un
#     total invariante tambien sale de «uno ARREGLADO + uno NUEVO». Si el aviso afirmara que nadie
#     añadio nada, un alta real se colaria detras de una coincidencia aritmetica. El gate sigue en
#     rojo; lo que se exige aqui es que NO mienta.
rm -f "$SRC"/*.tsx
pinta C.tsx sin-filas con-vacio      # A renombrado a C
pinta B.tsx con-filas con-vacio      # B arreglado
pinta D.tsx sin-filas con-vacio      # D NUEVO — el empeoramiento escondido
printf 'web/src/features/A.tsx\t1\nweb/src/features/B.tsx\t1\n' >"$BASE_FALSA"
[ "$(corre)" = "1" ] && ok "rename + fix + addition => red" "rc=1" || malo "did not block"
grep -q 'or a resolved case plus a new one' "$WORK/out.txt" && ok "and NAMES the alternative interpretation" || malo "⛔ the notice claims it is a move"
grep -q 'cannot distinguish them' "$WORK/out.txt" && ok "and admits it cannot distinguish them" || malo "does not acknowledge the limit"
grep -qE '\+ web/src/features/D\.tsx' "$WORK/out.txt" && ok "and the actual addition appears in the list" || malo "the addition is not shown"

# 9 · ⛔⛔ EL TRINQUETE DE VERDAD: no basta con casar con la baseline PRESENTE. Un commit que
#     suba el riesgo en el JSX **y** suba la baseline a la vez salia VERDE — la baseline nueva se
#     autorizaba a si misma, y «SOLO PUEDE BAJAR» era prosa en la cabecera del fichero, no un
#     control. Lo encontro the reviewer (01:12Z) con este mutante exacto. Se compara con la baseline
#     del merge-base con origin/main; aqui se inyecta por fichero porque la bateria es hermetica.
mono() { # <baseline-antes> <baseline-ahora> -> rc
	cp "$2" "$BASE_FALSA"
	BADGE_ANTERIOR="$1" bash "$GUION_COPIA" >"$WORK/m.txt" 2>&1
	printf '%s' "$?"
}
cat >"$SRC/M.tsx" <<'FIN'
export function V() {
  return (<div>
      <ListTruncationBadge query={a} label="x" hint="y" />
      <ListTruncationBadge query={b} label="x" hint="y" />
      <EmptyState title="nada" />
  </div>)
}
FIN
rm -f "$SRC"/a.tsx "$SRC"/renombrado.tsx "$SRC"/B.tsx "$SRC"/C.tsx "$SRC"/D.tsx
printf 'web/src/features/M.tsx\t1\n' >"$WORK/antes.txt"
printf 'web/src/features/M.tsx\t2\n' >"$WORK/ahora.txt"
[ "$(mono "$WORK/antes.txt" "$WORK/ahora.txt")" = "1" ] &&
	ok "JSX increases + baseline increases => RED" "rc=1" || malo "⛔ the baseline authorized itself"
grep -q 'RISK INCREASED FROM THE BASE' "$WORK/m.txt" && ok "and says so explicitly" || malo "does not name the cause"
grep -q 'web/src/features/M.tsx: 1 → 2' "$WORK/m.txt" && ok "and names the path with the increase" || malo "does not provide the path"
grep -q 'TOTAL increased from the base' "$WORK/m.txt" && ok "and also checks the total" || malo "does not check the total"

# ⛔ EL NEGATIVO: una BAJADA real sigue siendo verde, o el trinquete impediria mejorar.
cat >"$SRC/M.tsx" <<'FIN'
export function V() {
  return (<div>
      <ListTruncationBadge query={a} label="x" hint="y" filas={n} />
      <EmptyState title="nada" />
  </div>)
}
FIN
: >"$WORK/vacia.txt"
[ "$(mono "$WORK/antes.txt" "$WORK/vacia.txt")" = "0" ] &&
	ok "fix and empty the baseline => GREEN" "the ratchet allows improvements" || { malo "an actual decrease returned red"; head -4 "$WORK/m.txt"; }

# ⛔ Y SIN forma de leer la base, se DECLARA parcial: no es verde silencioso.
cp "$WORK/vacia.txt" "$BASE_FALSA"
bash "$GUION_COPIA" >"$WORK/m2.txt" 2>&1
grep -q 'PARTIAL' "$WORK/m2.txt" && ok "no git or baseline => DECLARES it partial" || malo "did not disclose that the monotonic half was not checked"
grep -q 'could not verify that the baseline' "$WORK/m2.txt" && ok "and says WHAT was not verified" || malo "does not say what is missing"

# 10 · Overrides must not weaken the gate. v4 replaced the root before querying Git,
# so BADGE_RAIZ=<nongit-subdirectory> changed the same tree from rc 1 to rc 0 PARCIAL.
# the reviewer measured it. This case enforces the opposite guarantee the comment had
# already claimed without a control.
rm -f "$SRC"/*.tsx
pinta OV.tsx sin-filas con-vacio
: >"$BASE_FALSA"                       # baseline vacía ⇒ el montaje es NUEVO ⇒ rojo
rc_normal="$(corre)"
[ "$rc_normal" = "1" ] && ok "without an override, the test tree is red" "rc=1" || malo "the fixture was not red"
mkdir -p "$WORK/sin-git"
: >"$WORK/baseline-mentirosa"
BADGE_RAIZ="$WORK/sin-git" BADGE_SRC="$WORK/no-existe" BADGE_BASELINE="$WORK/baseline-mentirosa" \
	bash "$GUION_COPIA" >"$WORK/ov.txt" 2>&1
rc_ov="$?"
[ "$rc_ov" = "1" ] &&
	ok "all THREE variables are INERT: the gate stays red" "rc=1" ||
	malo "⛔ the override disabled the gate (rc=$rc_ov)"
# ⛔ Se busca el USO EJECUTABLE, no la palabra: el guion documenta en prosa por que se retiro
#    la palanca, y una sonda por token contaria ese comentario como si fuera codigo.
# ⛔ Y LA SONDA BUSCA LAS TRES, no la que me señalaron. Retire `BADGE_RAIZ` y deje vivas a sus
#    dos hermanas: al quitar una palanca se barre su CLASE.
_vivas="$(grep -vE '^[[:space:]]*#' "$GUION" | grep -cE 'BADGE_(RAIZ|SRC|BASELINE)' || true)"
if [ "${_vivas:-0}" -eq 0 ]; then
	ok "none of the three has executable uses" "only explanatory prose"
else
	malo "⛔ $_vivas executable use(s) of BADGE_RAIZ/SRC/BASELINE remain"
fi
grep -q 'NEW' "$WORK/ov.txt" && ok "and still names the finding" || malo "the override hid the details"

# 7 · «no puedo mirar» ≠ «está limpio»
mv "$FALSO/web/src" "$FALSO/web/src-guardado"
bash "$GUION_COPIA" >/dev/null 2>&1
[ "$?" = "2" ] && ok "missing source => rc=2" || malo "does not distinguish 'could not inspect'"
mv "$FALSO/web/src-guardado" "$FALSO/web/src"
mv "$BASE_FALSA" "$WORK/base-guardada"
bash "$GUION_COPIA" >/dev/null 2>&1
[ "$?" = "2" ] && ok "missing baseline => rc=2" || malo "without a baseline, did not report that inspection was impossible"

echo
echo "test-badge-vacio-filas: $pass_count passed, $fail_count failed"
[ "$fail_count" -eq 0 ]
