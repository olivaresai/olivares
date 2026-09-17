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

# ── 11 · ESTADO GUARDADO: `|| _rc=$?` no es un incumplidor ────────────────────
# Es la forma que el propio gate pide —el rc se conserva y la guarda siguiente lo LEE— y el
# escaner la acusaba (check-ver-01-build-tree.sh:295, 2026-09-05). El fixture copia esa forma.
GUARDADO='#!/usr/bin/env bash
set -euo pipefail
_grc=0
act="$(printf "%s\n" "$comm_nm" | grep -c "enterprise/activation\.")" || _grc=$?
[ "$_grc" -le 1 ] || cannot "the counter exited $_grc: a reader failure, not a count"
[ "${act:-0}" = "0" ] || fail "carries $act activation symbols"
'
R11="$(arbol guardado)"; sembrar "$R11" a.sh "$GUARDADO"
printf '# vacia\n' > "$R11/ci/mute-pipefail-baseline.txt"
rc="$(correr "$R11")"
[ "$rc" = "0" ] && paso "una asignacion con su rc GUARDADO (|| _rc=\$?) NO se reporta" \
	|| malo "rc guardado: rc=$rc (esperado 0) — $(head -c 200 "$T/err")"

# ── 12 · LA MISMA LINEA SIN EL `|| _rc=$?` SI se reporta ──────────────────────
# El control del caso 11: lo unico que cambia es el discriminante, asi que si esto no sale 1
# el caso 11 estaria verde porque el gate no ve NADA, no porque distinga la forma.
DESNUDO='#!/usr/bin/env bash
set -euo pipefail
_grc=0
act="$(printf "%s\n" "$comm_nm" | grep -c "enterprise/activation\.")"
[ "$_grc" -le 1 ] || cannot "the counter exited $_grc: a reader failure, not a count"
[ "${act:-0}" = "0" ] || fail "carries $act activation symbols"
'
R12="$(arbol desnudo)"; sembrar "$R12" a.sh "$DESNUDO"
printf '# vacia\n' > "$R12/ci/mute-pipefail-baseline.txt"
rc="$(correr "$R12")"
if [ "$rc" = "1" ] && command grep -q 'scripts/a.sh' "$T/err" && command grep -q 'act' "$T/err"; then
	paso "la misma asignacion SIN guardar el rc sigue siendo incumplidora (1, variable act)"
else
	malo "desnudo: rc=$rc (esperado 1 nombrando act) — $(head -c 200 "$T/err")"
fi

# ── 13 · FORMAS VALIDAS DEL SUFIJO: continuacion `\` y cuerpo multilinea ─────────
# El sufijo puede venir tras una continuacion o en la linea que cierra `)"`: las dos son codigo
# pegado al cierre de la asignacion y las dos se aceptan.
CONTINUACION='#!/usr/bin/env bash
set -euo pipefail
_rc=0
H="$(grep -E "x" "$F" | head -1)" \
  || _rc=$?
[ "$_rc" -le 1 ] || cannot "the reader failed ($_rc)"
[ -n "$H" ] || fail "no pude leer x"
'
CUERPO_MULTI='#!/usr/bin/env bash
set -euo pipefail
rc=0
matches="$(
  grep -c x "$input"
)" || rc=$?
[ "$rc" -le 1 ] || cannot "the reader failed ($rc)"
[ "${matches:-0}" = "0" ] || fail "could not derive the match count"
'
R13="$(arbol formas)"; sembrar "$R13" a.sh "$CONTINUACION"; sembrar "$R13" b.sh "$CUERPO_MULTI"
printf '# vacia\n' > "$R13/ci/mute-pipefail-baseline.txt"
rc="$(correr "$R13")"
[ "$rc" = "0" ] && paso "el sufijo tras una continuacion \\ y el sufijo en la linea del cierre )\" se aceptan" \
	|| malo "formas validas: rc=$rc (esperado 0) — $(head -c 200 "$T/err")"

# ── 15 · EL SEÑUELO DE LA REVISION: la grafia en un COMENTARIO dentro de la sustitucion ──
# Fixture literal de la revision independiente (repro-mute-comment-bypass.sh): con un `grep -c`
# que devuelve 1 el guion muere con 1 y sin mensaje antes de su propia guarda. La primera version
# de la excepcion lo aceptaba (gate 0). Tiene que acusarse, nombrando `matches`.
COMENTARIO='#!/usr/bin/env bash
set -euo pipefail
fail() { echo "OWN GUARD: $*" >&2; exit 1; }
input="$1"
matches="$(
  # Suggested saved-status spelling: || read_rc=$?
  grep -c x "$input"
)"
[ "${matches:-0}" = "0" ] || fail "could not derive the match count"
echo "SCRIPT_REACHED_END"
'
R15="$(arbol comentario)"; sembrar "$R15" comment-bypass.sh "$COMENTARIO"
printf '# vacia\n' > "$R15/ci/mute-pipefail-baseline.txt"
rc="$(correr "$R15")"
if [ "$rc" = "1" ] && command grep -q 'scripts/comment-bypass.sh' "$T/err" && command grep -q 'matches' "$T/err"; then
	paso "la grafia en un comentario DENTRO de la sustitucion no protege: se acusa (1, variable matches)"
else
	malo "señuelo de comentario: rc=$rc (esperado 1 nombrando matches) — $(head -c 200 "$T/err")"
fi
# Y el guion del fixture muere DE VERDAD asi: 1, sin una linea en stdout ni stderr.
: > "$T/vacio.txt"; rc=0
bash "$R15/scripts/comment-bypass.sh" "$T/vacio.txt" >"$T/rt.out" 2>"$T/rt.err" || rc=$?
if [ "$rc" = "1" ] && [ ! -s "$T/rt.out" ] && [ ! -s "$T/rt.err" ]; then
	paso "y ese fixture muere mudo de verdad: rc 1 con stdout y stderr vacios (la clase del gate)"
else
	malo "el fixture del señuelo no muere mudo: rc=$rc out=$(wc -c <"$T/rt.out") err=$(wc -c <"$T/rt.err")"
fi

# ── 16 · MAS SEÑUELOS: comentario al final de linea, cadenas citadas, anidado, `|| echo` ──
# Ninguno es un sufijo ejecutable pegado al cierre de ESA asignacion; los cinco se acusan.
SENUELOS='#!/usr/bin/env bash
set -euo pipefail
a="$(grep -c x "$f")"   # || rc=$?
[ "${a:-0}" = "0" ] || fail "a"
b="$(grep -c '"'"'|| rc=$?'"'"' "$f")"
[ "${b:-0}" = "0" ] || fail "b"
c="$(grep -c "|| rc=$?" "$f")"
[ "${c:-0}" = "0" ] || fail "c"
d="$( y=$(grep -c a "$f") || inner=$?; grep -c b "$f" )"
[ "${d:-0}" = "0" ] || fail "d"
e="$(grep -c x "$f")" || echo "count failed"
[ "${e:-0}" = "0" ] || fail "e"
'
R16="$(arbol senuelos)"; sembrar "$R16" a.sh "$SENUELOS"
printf '# vacia\n' > "$R16/ci/mute-pipefail-baseline.txt"
rc="$(correr "$R16")"
faltan=""
for v in a b c d e; do command grep -q "variable \`$v\`" "$T/err" || faltan="$faltan $v"; done
if [ "$rc" = "1" ] && [ -z "$faltan" ]; then
	paso "comentario inline, cadena simple, cadena doble, sufijo anidado y || echo: los cinco acusados"
else
	malo "señuelos: rc=$rc (esperado 1) faltan:[$faltan] — $(head -c 300 "$T/err")"
fi

# ── 17 · EL CASO REAL que motivo la excepcion sigue aceptado ─────────────────────
# Las lineas EXACTAS de check-ver-01-build-tree.sh (el contador `act` y sus dos guardas), copiadas
# del fuente vivo: si ese bloque cambia de forma, este caso lo sigue.
# export-closure: hub-only scripts/check-ver-01-build-tree.sh — specimen of a guarded assignment; SCRIPTS_BLOCK, so case 17 is SCOPED in the public tree
_ver01="$RAIZ/scripts/check-ver-01-build-tree.sh"
if [ -f "$_ver01" ]; then
	BLOQUE_VER01="$(sed -n '/^\t\t_grc=0$/,/the record says community is not BASE"/p' "$_ver01")"
	# La guarda lee el bloque ya capturado: grep puede cerrar pronto sin cortar un productor.
	if command grep -q 'act="\$(' <<<"$BLOQUE_VER01" && command grep -q '|| _grc=\$?' <<<"$BLOQUE_VER01"; then
		VER01="$(printf '#!/usr/bin/env bash\nset -euo pipefail\n%s\n' "$BLOQUE_VER01")"
		R17="$(arbol ver01)"; sembrar "$R17" a.sh "$VER01"
		printf '# vacia\n' > "$R17/ci/mute-pipefail-baseline.txt"
		rc="$(correr "$R17")"
		[ "$rc" = "0" ] && paso "el bloque real de check-ver-01-build-tree.sh (act, || _grc=\$?) sigue aceptado" \
			|| malo "ver01 real: rc=$rc (esperado 0) — $(head -c 200 "$T/err")"
	else
		malo "no encontre el bloque real de check-ver-01-build-tree.sh (act / || _grc=\$?): el caso no puede montarse"
	fi
else
	_cls=""
	if [ -f "$RAIZ/scripts/hub-leg.sh" ]; then
		_cls="$(bash "$RAIZ/scripts/hub-leg.sh" --classify --root "$RAIZ" 2>/dev/null || true)"
	fi
	if [ "$_cls" = "public" ]; then
		paso "SCOPED — specimen scripts/check-ver-01-build-tree.sh is hub-only; case 17 has no subject in the public tree"
	else
		malo "no encontre el bloque real de check-ver-01-build-tree.sh (act / || _grc=\$?): el caso no puede montarse"
	fi
fi

# ── 18 · DISCRIMINADOR: el regex ANTERIOR acepta el señuelo del comentario; el gate no ──
# Se muta el gate de vuelta a su primera version —la busqueda sobre el texto crudo— y se le da
# el arbol del caso 15. Tiene que salir 0 (la burla que la revision midio) mientras el gate real
# sale 1. Sin este contraste, el caso 15 podria estar verde por cualquier otra razon.
M18="$T/sut-regex-anterior.sh"
python3 - "$SUT" "$M18" <<'MUT'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
v = """rc_guardado_ejecutable(cuerpo, m.end(), l[m.end() - 3] == '"')"""
if v not in s:
    sys.exit(3)
open(sys.argv[2], "w", encoding="utf-8").write(s.replace(v, r"""re.search(r'\|\|\s*[A-Za-z_]\w*=\$\?', cuerpo)""", 1))
MUT
if [ ! -s "$M18" ] || cmp -s "$SUT" "$M18"; then
	malo "NO se pudo construir el mutante del regex anterior: sin artefacto no hay juicio"
else
	rc=0
	MUTE_PIPEFAIL_ROOT="$R15" MUTE_PIPEFAIL_BASELINE="$R15/ci/mute-pipefail-baseline.txt" bash "$M18" >/dev/null 2>"$T/err" || rc=$?
	[ "$rc" = "0" ] && paso "discriminador: el regex anterior sobre texto crudo deja pasar el comentario (0); el gate real lo acusa (1)" \
		|| malo "el mutante del regex anterior no reproduce la burla: rc=$rc (esperado 0) — $(head -c 160 "$T/err")"
fi

# ── 19 · MUTANTE SIN EXCEPCION: el caso 11 se acusa solo porque la excepcion esta viva ──
M19="$T/sut-sin-excepcion.sh"
python3 - "$SUT" "$M19" <<'MUT'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
v = """rc_guardado_ejecutable(cuerpo, m.end(), l[m.end() - 3] == '"')"""
if v not in s:
    sys.exit(3)
open(sys.argv[2], "w", encoding="utf-8").write(s.replace(v, "False", 1))
MUT
if [ ! -s "$M19" ] || cmp -s "$SUT" "$M19"; then
	malo "NO se pudo construir el mutante sin excepcion: sin artefacto no hay juicio"
else
	rc=0
	MUTE_PIPEFAIL_ROOT="$R11" MUTE_PIPEFAIL_BASELINE="$R11/ci/mute-pipefail-baseline.txt" bash "$M19" >/dev/null 2>"$T/err" || rc=$?
	if [ "$rc" = "1" ] && command grep -q 'scripts/a.sh' "$T/err"; then
		paso "mutante: sin la excepcion, el gate mutado acusa al caso 11 (1): la excepcion es lo que decide"
	else
		malo "mutante sin excepcion: rc=$rc (esperado 1 acusando a.sh) — $(head -c 160 "$T/err")"
	fi
fi

# ── 20 · ANSI-C: la burla de la raiz (`$'it\'s )" || read_rc=$?'`) se acusa ─────────────
# Fixture literal de la revision de la raiz (root-review/fixtures/ansi_quote_decoy). En bash
# `$'…'` cierra en la PRIMERA comilla sin escapar y `\'` es una comilla escapada, asi que la cadena
# entera es un argumento de printf; el `)` que lleva dentro cortaba el cuerpo antes del `grep` y
# el gate decia 0 mientras el guion moria con 1 y sin mensaje. Se acusa, nombrando `matches`.
ANSI_SENUELO='#!/usr/bin/env bash
set -euo pipefail
fail() { echo "OWN GUARD: $*" >&2; exit 1; }
input="$1"
read_rc=0
matches="$(
  printf '"'"'%s'"'"' $'"'"'it\'"'"'s )" || read_rc=$?'"'"'
  grep -c x "$input"
)"
[ "${matches:-0}" = "0" ] || fail "no match count"
echo SCRIPT_REACHED_END
'
R20="$(arbol ansi)"; sembrar "$R20" probe.sh "$ANSI_SENUELO"
printf '# vacia\n' > "$R20/ci/mute-pipefail-baseline.txt"
bash -n "$R20/scripts/probe.sh" 2>/dev/null && d=ok || d=NO
[ "$d" = "ok" ] && paso "el fixture ANSI-C es bash valido (bash -n)" || malo "el fixture ANSI-C no es bash valido: el caso mediria otra cosa"
rc="$(correr "$R20")"
if [ "$rc" = "1" ] && command grep -q 'scripts/probe.sh' "$T/err" && command grep -q 'matches' "$T/err"; then
	paso "ANSI-C: la comilla escapada no cierra la cadena, el cuerpo llega al grep y se acusa (1, matches)"
else
	malo "ANSI-C señuelo: rc=$rc (esperado 1 nombrando matches) — $(head -c 200 "$T/err")"
fi
: > "$T/vacio20.txt"; rc=0
bash "$R20/scripts/probe.sh" "$T/vacio20.txt" >"$T/rt20.out" 2>"$T/rt20.err" || rc=$?
if [ "$rc" = "1" ] && [ ! -s "$T/rt20.out" ] && [ ! -s "$T/rt20.err" ]; then
	paso "y ese fixture muere mudo de verdad: rc 1 con stdout y stderr vacios"
else
	malo "el fixture ANSI-C no muere mudo: rc=$rc out=$(wc -c <"$T/rt20.out") err=$(wc -c <"$T/rt20.err")"
fi

# ── 21 · ANSI-C, variante en la que un sufijo mal leido SI casaria ────────────────────
# Con un espacio antes de la comilla final, el texto que queda tras una comilla escapada mal
# cerrada seria exactamente `)" || read_rc=$? ` — un sufijo con su delimitador. Sigue siendo
# cadena, y sigue acusandose.
ANSI_SENUELO2="$(printf '%s\n' "$ANSI_SENUELO" | sed 's/read_rc=\$?'"'"'$/read_rc=$? '"'"'/')"
# Es una guarda ejecutable del banco, no texto del fixture: conserva el patron sin tuberia.
command grep -q 'read_rc=\$? '"'"'$' <<<"$ANSI_SENUELO2" || malo "no pude construir la variante ANSI-C con espacio final"
R21="$(arbol ansi2)"; sembrar "$R21" probe.sh "$ANSI_SENUELO2"
printf '# vacia\n' > "$R21/ci/mute-pipefail-baseline.txt"
rc="$(correr "$R21")"
if [ "$rc" = "1" ] && command grep -q 'matches' "$T/err"; then
	paso "ANSI-C con espacio final (el sufijo falso llevaria delimitador): tambien se acusa"
else
	malo "ANSI-C variante: rc=$rc (esperado 1) — $(head -c 200 "$T/err")"
fi

# ── 22 · ANSI-C y `${…}` en POSITIVO: la forma valida con esas cadenas dentro se acepta ──
# Sin esto, «reconocer ANSI-C» podria ser «rehusar todo lo que lleve $'»: un positivo con la
# comilla escapada dentro y el sufijo real fuera, y otro con un `)` dentro de `${…}`.
ANSI_POSITIVO='#!/usr/bin/env bash
set -euo pipefail
rc=0
x="$(printf '"'"'%s\n'"'"' $'"'"'it\'"'"'s'"'"' | grep -c x)" || rc=$?
[ "$rc" -le 1 ] || cannot "reader failed ($rc)"
[ "${x:-0}" = "0" ] || fail "x"
y="$(grep -c "${f//)/}" "$f")" || rc=$?
[ "$rc" -le 1 ] || cannot "reader failed ($rc)"
[ "${y:-0}" = "0" ] || fail "y"
'
R22="$(arbol ansipos)"; sembrar "$R22" a.sh "$ANSI_POSITIVO"
printf '# vacia\n' > "$R22/ci/mute-pipefail-baseline.txt"
bash -n "$R22/scripts/a.sh" 2>/dev/null || malo "el positivo ANSI-C no es bash valido"
rc="$(correr "$R22")"
[ "$rc" = "0" ] && paso "positivos: sufijo real con \$'…' escapado dentro y con \${…} que lleva un ) se aceptan" \
	|| malo "positivo ANSI-C/llaves: rc=$rc (esperado 0) — $(head -c 200 "$T/err")"

# ── 23 · FORMAS QUE EL LEXICO NO ESTABLECE: no hay excepcion, y se dice ───────────────
# Un backtick o un heredoc dentro de la sustitucion devuelven «no se»; «no se» no concede,
# asi que el sufijo real que sigue NO cuenta y la asignacion se juzga como siempre (aqui, se
# acusa porque su guarda esta debajo). Es el limite declarado del lexico, no un fallo.
DESCONOCIDO='#!/usr/bin/env bash
set -euo pipefail
rc=0
p="$(grep -c `cat pat` "$f")" || rc=$?
[ -n "$p" ] || fail "p"
q="$(grep -c x <<AQUI
line)
AQUI
)" || rc=$?
[ -n "$q" ] || fail "q"
'
R23="$(arbol desconocido)"; sembrar "$R23" a.sh "$DESCONOCIDO"
printf '# vacia\n' > "$R23/ci/mute-pipefail-baseline.txt"
rc="$(correr "$R23")"
if [ "$rc" = "1" ] && command grep -q 'variable `p`' "$T/err" && command grep -q 'variable `q`' "$T/err"; then
	paso "backtick y heredoc dentro de la sustitucion: forma no establecida, sin excepcion (p y q acusadas)"
else
	malo "formas no establecidas: rc=$rc (esperado 1 acusando p y q) — $(head -c 200 "$T/err")"
fi

# ── 24 · MUTANTE DEL LEXICO ANTERIOR: sin ANSI-C, la burla de la raiz vuelve a pasar ──
# Se quita la rama `$'` del lexico (las dos, en la sustitucion y en `${…}`): la comilla
# escapada vuelve a cerrar la cadena, el `)` interior corta el cuerpo antes del grep y el gate
# mutado dice 0 sobre el arbol del caso 20 mientras el real dice 1.
M24="$T/sut-sin-ansi.sh"
python3 - "$SUT" "$M24" <<'MUT'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
v = """        if s.startswith("$'", i):
            j = _cierre_ansi(s, i + 2)"""
if s.count(v) < 2:
    sys.exit(3)
open(sys.argv[2], "w", encoding="utf-8").write(s.replace(v, """        if False:
            j = _cierre_ansi(s, i + 2)"""))
MUT
if [ ! -s "$M24" ] || cmp -s "$SUT" "$M24"; then
	malo "NO se pudo construir el mutante sin ANSI-C: sin artefacto no hay juicio"
else
	rc=0
	MUTE_PIPEFAIL_ROOT="$R20" MUTE_PIPEFAIL_BASELINE="$R20/ci/mute-pipefail-baseline.txt" bash "$M24" >/dev/null 2>"$T/err" || rc=$?
	[ "$rc" = "0" ] && paso "mutante sin ANSI-C: la burla de la raiz vuelve a pasar (0); el gate real la acusa (1)" \
		|| malo "el mutante sin ANSI-C no reproduce la burla: rc=$rc (esperado 0) — $(head -c 160 "$T/err")"
fi

# ── 25 · MUTANTE DEL CUERPO: con la cuenta de parentesis de siempre, la burla tambien pasa ──
# Es la otra mitad del defecto: aunque el lexico sepa ANSI-C, si el cuerpo se cortara en el
# primer `)` el grep quedaria fuera y el filtro de sujeto saltaria la asignacion.
M25="$T/sut-cuenta-parentesis.sh"
python3 - "$SUT" "$M25" <<'MUT'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
v = r"""        _k = _cierre_sustitucion("\n".join(ls[i:]), m.end())"""
if v not in s:
    sys.exit(3)
open(sys.argv[2], "w", encoding="utf-8").write(s.replace(v, "        _k = None", 1))
MUT
if [ ! -s "$M25" ] || cmp -s "$SUT" "$M25"; then
	malo "NO se pudo construir el mutante de la cuenta de parentesis: sin artefacto no hay juicio"
else
	rc=0
	MUTE_PIPEFAIL_ROOT="$R20" MUTE_PIPEFAIL_BASELINE="$R20/ci/mute-pipefail-baseline.txt" bash "$M25" >/dev/null 2>"$T/err" || rc=$?
	[ "$rc" = "0" ] && paso "mutante con la cuenta de parentesis: el cuerpo se corta antes del grep y la burla pasa (0)" \
		|| malo "el mutante de la cuenta no reproduce la burla: rc=$rc (esperado 0) — $(head -c 160 "$T/err")"
fi

# ── 14 · LOCALE: el gate no hereda la colacion de quien lo lanza ──────────────
# Medido el 2026-09-05: bajo LC_ALL=es_ES.UTF-8 `comm` avisaba «not in sorted order» sobre listas
# ordenadas en C. Se corre bajo un locale instalado distinto de C, si hay alguno; si no lo hay
# se DICE, y no cuenta como pasado.
# Se prefiere es_ES, que es el locale con el que se midio el aviso; si no esta, cualquier otro
# instalado distinto de C sirve para ejercer la colacion.
LOC="$(locale -a 2>/dev/null | command grep -iE '^es_ES\.(utf8|UTF-8)$' | head -1 || true)"
[ -n "$LOC" ] || LOC="$(locale -a 2>/dev/null | command grep -iE '^(en_US|de_DE|fr_FR)\.(utf8|UTF-8)$' | head -1 || true)"
if [ -n "$LOC" ]; then
	rc=0
	LC_ALL="$LOC" MUTE_PIPEFAIL_ROOT="$R11" MUTE_PIPEFAIL_BASELINE="$R11/ci/mute-pipefail-baseline.txt" bash "$SUT" >/dev/null 2>"$T/err" || rc=$?
	if [ "$rc" = "0" ] && ! command grep -q 'not in sorted order' "$T/err"; then
		paso "bajo LC_ALL=$LOC el gate compara igual que en C: sin avisos de orden y rc 0"
	else
		malo "locale $LOC: rc=$rc — $(head -c 200 "$T/err")"
	fi
else
	OMITIDO=1
	echo "SKIP no hay un locale distinto de C instalado: el caso de colacion NO se ha ejercido"
fi

echo "check-mute-pipefail selftest: $OK passed, $MAL failed${OMITIDO:+, 1 skipped (locale)}"
[ "$MAL" -eq 0 ]
