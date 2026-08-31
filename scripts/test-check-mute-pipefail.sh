#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Banco de `check-mute-pipefail.sh`. Cada caso VE CORTAR al gate sobre un arbol de mentira, y el
# caso que mas importa es el de SUSTITUCION: es el unico que justifica que la linea base sea una
# lista y no un numero.
set -uo pipefail

# ⛔ EL ENTORNO GIT AMBIENTE MANDA SOBRE `-C` Y SOBRE EL cwd. Con `GIT_DIR` exportada —y git la
# exporta desde CUALQUIER worktree enlazado, o sea desde cualquier sesion en paralelo— los
# repositorios de usar y tirar de esta bateria se conducirian al repositorio VIVO. Medido el
# 2026-08-06: dejo la rama de la PR #526 apuntando a un commit de fixture. Falla cerrado: un
# saneador que falta es «no he podido aislar», nunca «no hacia falta aislar».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env
RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SUT="$RAIZ/scripts/check-mute-pipefail.sh"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
OK=0; MAL=0
paso() { OK=$((OK+1)); echo "ok   $*"; }
malo() { MAL=$((MAL+1)); echo "FAIL $*" >&2; }

# Un arbol de mentira: git real (el gate usa ls-files) con los guiones que le demos.
arbol() {                       # arbol <nombre> -> imprime la raiz
	local d="$T/$1"
	mkdir -p "$d/scripts" "$d/ci"
	git -C "$d" init -q 2>/dev/null
	printf '%s\n' "$d"
}
guion() {                       # guion <raiz> <nombre> <cuerpo>
	printf '%s' "$3" > "$2/scripts/$1" 2>/dev/null || printf '%s' "$3" > "$1"
}
sembrar() {                     # sembrar <raiz> <fichero> <cuerpo>
	printf '%s' "$3" > "$1/scripts/$2"
	git -C "$1" add -A >/dev/null 2>&1
}
correr() {                      # correr <raiz> [baseline] -> imprime rc y deja salida en $T/out
	local r="$1" b="${2:-$1/ci/mute-pipefail-baseline.txt}" rc=0
	MUTE_PIPEFAIL_ROOT="$r" MUTE_PIPEFAIL_BASELINE="$b" bash "$SUT" >"$T/out" 2>"$T/err" || rc=$?
	printf '%s' "$rc"
}

CULPABLE='#!/usr/bin/env bash
set -euo pipefail
idle="$(grep -E "x" "$F" | head -1)"
[ -n "$idle" ] || fail "no pude leer x"
'
CURADO='#!/usr/bin/env bash
set -euo pipefail
idle="$( { grep -E "x" "$F" || true; } | head -1)"
[ -n "$idle" ] || fail "no pude leer x"
'
PROTEGIDO='#!/usr/bin/env bash
set -euo pipefail
H="$(grep -E "x" "$F" | head -1)" \
  || cannot "no pude derivarlo"
[ -n "$H" ] || cannot "vacio"
'

# ── 1 · un incumplidor NUEVO se nombra y sale 1 ────────────────────────────────
R="$(arbol nuevo)"; sembrar "$R" a.sh "$CULPABLE"
printf '# vacia\n' > "$R/ci/mute-pipefail-baseline.txt"
rc="$(correr "$R")"
if [ "$rc" = "1" ] && command grep -q 'scripts/a.sh' "$T/err" && command grep -q 'idle' "$T/err"; then
	paso "un incumplidor nuevo sale 1 y se nombra con fichero y variable"
else
	malo "incumplidor nuevo: rc=$rc (esperado 1) — err: $(head -c 200 "$T/err")"
fi

# ── 2 · el mismo, DECLARADO en la linea base, no falla ─────────────────────────
printf 'scripts/a.sh\tidle\n' > "$R/ci/mute-pipefail-baseline.txt"
rc="$(correr "$R")"
[ "$rc" = "0" ] && paso "un incumplidor declarado en la linea base no falla" \
	|| malo "declarado: rc=$rc (esperado 0) — $(head -c 200 "$T/err")"

# ── 3 · SUSTITUCION: uno curado y otro nuevo, el TOTAL no se mueve ─────────────
# ⛔ ES EL CASO QUE JUSTIFICA LA LISTA. Con un contador, 1 -> 1 y el gate calla.
R3="$(arbol sust)"; sembrar "$R3" a.sh "$CURADO"; sembrar "$R3" b.sh "$CULPABLE"
printf 'scripts/a.sh\tidle\n' > "$R3/ci/mute-pipefail-baseline.txt"
rc="$(correr "$R3")"
if [ "$rc" = "1" ] && command grep -q 'scripts/b.sh' "$T/err"; then
	paso "SUSTITUCION: cura uno y rompe otro, el total no se mueve y el gate CORTA por el nuevo"
else
	malo "sustitucion: rc=$rc (esperado 1, nombrando b.sh) — $(head -c 200 "$T/err")"
fi

# ── 4 · una forma PROTEGIDA (|| cannot pegado) NO se reporta ───────────────────
# Falso positivo real que cometi al censar: el `||` es continuacion de la MISMA sentencia.
R4="$(arbol prot)"; sembrar "$R4" a.sh "$PROTEGIDO"
printf '# vacia\n' > "$R4/ci/mute-pipefail-baseline.txt"
rc="$(correr "$R4")"
[ "$rc" = "0" ] && paso "una asignacion con su propio || cannot NO se reporta" \
	|| malo "protegida: rc=$rc (esperado 0) — $(head -c 200 "$T/err")"

# ── 5 · la deuda que PUEDE BAJAR se dice, y no falla ───────────────────────────
R5="$(arbol baja)"; sembrar "$R5" a.sh "$CURADO"
printf 'scripts/a.sh\tidle\n' > "$R5/ci/mute-pipefail-baseline.txt"
rc="$(correr "$R5")"
if [ "$rc" = "0" ] && command grep -q 'PUEDE BAJAR' "$T/err"; then
	paso "un curado que sigue en la linea base se anuncia como deuda que puede bajar"
else
	malo "puede bajar: rc=$rc — $(head -c 200 "$T/err")"
fi

# ── 6 · sin linea base es NO HE PODIDO MIRAR (2), nunca un verde ───────────────
rc="$(correr "$R5" "$T/no-existe.txt")"
if [ "$rc" = "2" ] && command grep -q 'NO HE PODIDO MIRAR' "$T/err"; then
	paso "sin linea base sale 2 y lo dice"
else
	malo "sin linea base: rc=$rc (esperado 2)"
fi

# ── 7 · --gate RECHAZA las anulaciones que la bateria si usa ───────────────────
rc=0
MUTE_PIPEFAIL_ROOT="$R5" bash "$SUT" --gate >/dev/null 2>"$T/err" || rc=$?
if [ "$rc" = "2" ] && command grep -q 'no admite anulaciones' "$T/err"; then
	paso "--gate rechaza MUTE_PIPEFAIL_ROOT y sale 2"
else
	malo "--gate: rc=$rc (esperado 2) — $(head -c 200 "$T/err")"
fi

# ── 8 · MUTANTE DEL PROPIO GATE: si deja de mirar el `|| true`, el curado se delata ──
# Sin esto, el caso 5 podria estar verde porque el gate no encuentra NADA nunca.
M="$T/sut-mutante.sh"
sed 's/|| true|| true/XX/; s/true|:|cannot|fail|malo|die/NUNCA_CASA/' "$SUT" > "$M"
if cmp -s "$SUT" "$M"; then
	malo "NO se pudo construir el mutante del gate: sin artefacto no hay juicio"
else
	# ⛔ CON LINEA BASE VACIA, no con la de R5: alli el fichero YA esta declarado, asi que el
	#    mutante lo destaparia y el gate lo taparia igual — el caso saldria verde sin medir nada.
	#    (Me paso: 7/1 con el mutante aplicado y actuando. El señuelo se construye desde el
	#    control hacia atras, y aqui el control es «sale como NUEVO».)
	VACIA="$T/vacia.txt"; printf '# vacia\n' > "$VACIA"
	rc_sano=0
	MUTE_PIPEFAIL_ROOT="$R5" MUTE_PIPEFAIL_BASELINE="$VACIA" bash "$SUT" >/dev/null 2>&1 || rc_sano=$?
	rc=0
	MUTE_PIPEFAIL_ROOT="$R5" MUTE_PIPEFAIL_BASELINE="$VACIA" bash "$M" >/dev/null 2>"$T/err" || rc=$?
	if [ "$rc_sano" = "0" ] && [ "$rc" = "1" ] && command grep -q 'scripts/a.sh' "$T/err"; then
		paso "mutante: el gate sano NO ve al curado (0) y el mutado SI lo delata (1) — mira la cura de verdad"
	else
		malo "mutante del gate: sano=$rc_sano (esp. 0) mutado=$rc (esp. 1) — $(head -c 160 "$T/err")"
	fi
fi

# ── 9 · LA EXCLUSION SIGUE AL GATE SI LO RENOMBRAN ────────────────────────────
# ⛔ ESTE CASO NACIO DE UN NO. La version anterior comprobaba que se excluia
#    `test-check-mute-pipefail.sh`… con el gate llamandose asi, asi que acreditaba igual un
#    LITERAL que una derivacion — y lo que habia dentro ERA un literal (`__file__ if False else
#    "check-mute-pipefail.sh"`) mientras el comentario afirmaba derivar. Un lector lo desmintio
#    copiando el gate con otro nombre. Ahora el caso hace eso mismo: renombra.
R9="$(arbol excl)"
cp "$SUT" "$R9/scripts/check-renamed.sh"; chmod +x "$R9/scripts/check-renamed.sh"
sembrar "$R9" test-check-renamed.sh "$CULPABLE"
sembrar "$R9" test-otra-cosa.sh "$CULPABLE"
printf '# vacia\n' > "$T/vacia9.txt"
rc=0
MUTE_PIPEFAIL_ROOT="$R9" MUTE_PIPEFAIL_BASELINE="$T/vacia9.txt" \
	bash "$R9/scripts/check-renamed.sh" >/dev/null 2>"$T/err" || rc=$?
if [ "$rc" = "1" ] && command grep -q 'test-otra-cosa.sh' "$T/err" \
	&& ! command grep -q 'test-check-renamed.sh' "$T/err"; then
	paso "renombrado el gate, la exclusion LO SIGUE: excluye test-check-renamed.sh y acusa al otro"
else
	malo "derivacion: rc=$rc — deberia acusar test-otra-cosa.sh y NO test-check-renamed.sh: $(head -c 220 "$T/err")"
fi

# ── 10 · LA RUTA POR DEFECTO DE LA LINEA BASE RESUELVE ────────────────────────
# ⛔ Todos los casos de arriba INYECTAN la linea base, asi que ninguno mira la ruta por defecto —
#    y mover ese fichero deja el gate en rc 2 con el banco entero en verde. Me paso: lo mande a
#    `design/` para contentar a `check-claim-safety` y ahi NO VIAJA EN EL EXPORT (design/ publica
#    cero rutas), o sea que el gate publicado nacia roto para el lector publico. Este caso corre
#    el gate SIN inyectar nada.
rc=0
bash "$SUT" >/dev/null 2>"$T/err" || rc=$?
if [ "$rc" != "2" ]; then
	paso "la ruta por defecto de la linea base resuelve sobre el repositorio real (rc=$rc, no 2)"
else
	malo "la linea base por defecto NO resuelve: $(head -c 160 "$T/err")"
fi

echo "check-mute-pipefail selftest: $OK passed, $MAL failed"
[ "$MAL" -eq 0 ]
