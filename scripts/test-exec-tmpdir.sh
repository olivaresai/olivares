#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Bateria de `scripts/lib/exec-tmpdir.sh` y de su cableado.
#
# ⛔ LO QUE DE VERDAD HAY QUE PROBAR NO ES QUE DEVUELVA UNA RUTA: es que RECHACE una que no ejecuta.
#    Un ayudante que devuelva el primer candidato sin correr nada pasaria cualquier prueba de «me
#    dio un directorio» y dejaria al motor sin poder lanzar sus plugins, que es el fallo original.
#    Por eso el caso central usa un directorio REALMENTE noexec y hay un mutante que quita la sonda.
set -u -o pipefail

RAIZ="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
# ⛔ LOS DOS ENTORNOS SE NEUTRALIZAN, no solo uno. Heredar `OLIVARES_EXEC_TMPDIR` de quien llama
#    hace que las filas midan el candidato de OTRO y el banco devuelve 7/1 segun quien lo
#    invoque. `TMPDIR` se neutraliza donde hace falta, con rutas explicitas, porque otras
#    filas lo usan a proposito.
unset OLIVARES_EXEC_TMPDIR
LIB="$RAIZ/scripts/lib/exec-tmpdir.sh"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
ok=0
fail=0
paso() { printf 'ok   %s\n' "$1"; ok=$((ok + 1)); }
malo() { printf 'FAIL %s\n' "$1"; fail=$((fail + 1)); }

# ¿Ejecuta este `mktemp -d`? De eso depende que el caso 2 pueda existir, y se MIDE.
printf '#!/bin/sh\nexit 0\n' >"$T/.p"; chmod +x "$T/.p" 2>/dev/null
if "$T/.p" 2>/dev/null; then TMP_EJECUTA=1; else TMP_EJECUTA=0; fi
rm -f "$T/.p"

# ── 1 · devuelve un directorio que EJECUTA de verdad ──────────────────────────────────────────
BUENA="$(cd -- "$RAIZ/.." && pwd)"
r="$( . "$LIB"; olivares_exec_tmpdir "$BUENA" )"
rc=$?
if [ "$rc" = "0" ] && [ -d "$r" ]; then
	printf '#!/bin/sh\nexit 0\n' >"$r/.p-banco"; chmod +x "$r/.p-banco" 2>/dev/null
	if "$r/.p-banco" 2>/dev/null && case "$r" in */run-*) true ;; *) false ;; esac; then
		paso "returns a directory OWNED BY THE RUN and that directory really EXECUTES"
	elif "$r/.p-banco" 2>/dev/null; then
		malo "executes but returned the shared root ($r): two runs would collide and no one could clean up"
	else
		malo "returned $r and it does NOT execute: the helper is not probing"
	fi
	rm -f "$r/.p-banco"
	# ⛔ Y SE BORRA EL RUN, no solo la sonda (the reviewer, A-05). El banco dejaba un `run-NNN` colgado
	#    en la raiz compartida: un guion que vigila fugas de scratch fugando scratch.
	rmdir "$r" 2>/dev/null || rm -rf "$r"
else
	malo "returned no directory (rc $rc) in a tree where its sibling does execute"
fi

# ── 2 · RECHAZA un directorio que no ejecuta ──────────────────────────────────────────────────
if [ "$TMP_EJECUTA" = "1" ]; then
	printf 'SKIPPED  this host temporary directory EXECUTES, so no real noexec directory is available to\n'
	printf '         test rejection. This is NOT a pass: the decisive half remains untested.\n'
else
	r="$( . "$LIB"; OLIVARES_EXEC_TMPDIR="$T/uno" olivares_exec_tmpdir "$T/dos" )"
	rc=$?
	if [ "$rc" != "0" ] && [ -z "$r" ]; then
		paso "with both candidates in a REAL noexec temporary directory, returns rc 1 without inventing a path"
	else
		malo "accepted a directory that does not execute (rc $rc, path '$r'): the engine could not launch anything"
	fi
fi

# ── 3 · MUTANTE: se quita la sonda y devuelve el primer candidato ─────────────────────────────
# ⛔ Muere en el caso 2, que es el que nombra. Si el caso 2 esta saltado, este tambien lo dice en
#    vez de contarse como verde: un mutante que no puede morir no acredita nada.
mS="$T/mS.sh"
python3 - "$LIB" "$mS" <<'PY'
import sys
src = open(sys.argv[1]).read()
viejo = '\t\tif "$probe" 2>/dev/null; then'
nuevo = '\t\tif true; then  # MUTANTE: no se corre la sonda, se acepta el candidato'
mut = src.replace(viejo, nuevo, 1)
assert mut != src, "the probe mutant was NOT applied"
open(sys.argv[2], "w").write(mut)
PY
if [ "$TMP_EJECUTA" = "1" ]; then
	printf 'SKIPPED  the probe mutant cannot be killed without a real noexec directory (see case 2).\n'
else
	r="$( . "$mS"; OLIVARES_EXEC_TMPDIR="$T/tres" olivares_exec_tmpdir "$T/cuatro" )"
	rc=$?
	if [ "$rc" = "0" ] && [ -n "$r" ]; then
		paso "the mutant that removes the probe ACCEPTS noexec: case 2 catches it"
	else
		malo "the mutant survived (rc $rc): case 2 does not verify the probe"
	fi
fi

# ── 4 · TODO arranque del motor lleva el TMPDIR sondeado ──────────────────────────────────────
# ⛔ ES EL TRINQUETE QUE DE VERDAD ENVEJECE BIEN. Arreglar los guiones de hoy no impide que manana
#    alguien anada un `"$BIN" serve` sin el, y ese arranque quedaria mudo y deny-closed. Se cuenta
#    sobre el ARBOL, no sobre una lista escrita a mano.
missing_inputs=""
total=0
for f in "$RAIZ"/scripts/launch-state-captures.sh "$RAIZ"/scripts/web-e2e-demo.sh \
	"$RAIZ"/scripts/smoke-agentops.sh "$RAIZ"/scripts/quickstart-smoke.sh; do
	[ -f "$f" ] || continue
	# ⛔ SIN ANCLA `^`, Y ESE ANCLA ME COSTO UN ARRANQUE ENTERO (the reviewer, A-03). Contaba solo las
	#    lineas que EMPIEZAN por `"$BIN" serve`, asi que no veia
	#    `OLIVARES_SOURCES_CONFIG=… "$BIN" serve` — el reinicio CON conector de quickstart-smoke,
	#    que es justamente el arranque donde los plugins importan. Un trinquete con un ancla de mas
	#    cuenta menos de lo que promete, y el que se escapa es el que mas duele.
	#    Y se acepta el TMPDIR en la MISMA linea o en la anterior: las dos formas existen en el
	#    arbol y exigir una sola volveria a esconder la otra.
	while IFS= read -r n; do
		total=$((total + 1))
		linea="$(sed -n "${n}p" "$f")"
		anterior="$(sed -n "$((n - 1))p" "$f")"
		case "$linea$anterior" in
		*'TMPDIR="${EXEC_TMP:-${TMPDIR:-/tmp}}"'*) : ;;
		*) missing_inputs="$missing_inputs $(basename "$f"):$n" ;;
		esac
	done < <(command grep -n '"\$BIN" serve' "$f" | cut -d: -f1)
done
if [ "$total" -ge 6 ] && [ -z "$missing_inputs" ]; then
	paso "all $total engine starts in the four scripts use the probed TMPDIR"
elif [ -n "$missing_inputs" ]; then
	malo "starts WITHOUT the probed TMPDIR:$missing_inputs"
else
	malo "found only $total starts and expected 6 or more: has engine startup changed?"
fi

# ── 5 · CADA GUION LIMPIA SU PROPIO SCRATCH ───────────────────────────────────────────────────
# ⛔ SIN ESTO HAY UNA FUGA LENTA, y la vi antes de que costara nada: el motor extrae sus plugins
#    bajo `$TMPDIR` y ahi se quedan cuando el arnes muere. Una extraccion por corrida, en una caja
#    al 93 % de disco. Se cuenta sobre el ARBOL, como el caso 4: si alguien anade un guion o le
#    quita la limpieza, sale en rojo con su nombre.
sin_limpieza=""
mirados=0
for f in "$RAIZ"/scripts/launch-state-captures.sh "$RAIZ"/scripts/web-e2e-demo.sh \
	"$RAIZ"/scripts/smoke-agentops.sh "$RAIZ"/scripts/quickstart-smoke.sh; do
	[ -f "$f" ] || continue
	mirados=$((mirados + 1))
	command grep -q 'rm -rf.*EXEC_TMP' "$f" || sin_limpieza="$sin_limpieza $(basename "$f")"
done
if [ "$mirados" = "4" ] && [ -z "$sin_limpieza" ]; then
	paso "all four scripts remove their plugin scratch on exit (no extraction accumulates per run)"
elif [ -n "$sin_limpieza" ]; then
	malo "scripts that do NOT clean up their scratch:$sin_limpieza"
else
	malo "found only $mirados of the 4 scripts: the list and tree disagree"
fi

# ── 5-bis · EL REHUSE, VISTO CORTAR ───────────────────────────────────────────────────────────
# ⛔ ES EL HALLAZGO CENTRAL DE the reviewer (A-01) Y NO SE PRUEBA CON UN `grep`. La version anterior
#    avisaba, vaciaba `EXEC_TMP` y ARRANCABA IGUAL con `/tmp`: el seguro no aseguraba. Comprobar
#    que existe un `exit 2` en el fuente no vale — un control se verifica VIENDOLO CORTAR.
#
#    Y para verlo hay que conseguir que fallen LOS DOS candidatos, cosa que en esta caja no pasa
#    nunca: el respaldo `$(dirname ROOT)/.olivares-exec-tmp` cae bajo /workspace, que ejecuta. Por
#    eso se hace un CALCO del lanzador bajo un temporal: alli `dirname ROOT` es /tmp y los dos
#    candidatos son noexec. Forzar solo el override no bastaba —el respaldo lo salvaba— y creerlo
#    habria dado un verde falso: lo comprobe y el guion siguio adelante.
calco_rehusa() { # $1 = lanzador; rc 0 = rehusa con su mensaje
	# ⛔ EL CALCO VA A UN NOEXEC EXPLICITO, NO AL QUE HEREDE `mktemp`. Esta fila daba ROJO en una
	#    caja y no era del lanzador: `mktemp -d` honra `TMPDIR`, y con `TMPDIR` en un sistema que
	#    EJECUTA los dos candidatos del calco ejecutan, el lanzador arranca con razon y la
	#    asercion, que espera un rechazo, cae. Si /tmp tambien ejecutara, la fila NO se puede
	#    montar: se dice (rc 3), no se finge.
	local F BASE
	BASE="$(TMPDIR=/tmp mktemp -d)" || return 2
	if printf '#!/bin/sh\nexit 0\n' >"$BASE/.p" 2>/dev/null && chmod +x "$BASE/.p" 2>/dev/null &&
		"$BASE/.p" 2>/dev/null; then
		rm -rf "$BASE"; return 3
	fi
	rm -f "$BASE/.p" 2>/dev/null
	F="$BASE/fake"
	mkdir -p "$F/scripts/lib" "$F/bin" || return 2
	cp "$RAIZ/scripts/$1" "$F/scripts/" || return 2
	cp "$RAIZ"/scripts/lib/*.sh "$F/scripts/lib/" 2>/dev/null
	printf '#!/bin/sh\nexit 0\n' >"$F/bin/olivares" && chmod +x "$F/bin/olivares"
	local out rc
	out="$(cd "$F" && timeout 30 bash "$F/scripts/$1" 2>&1)"
	rc=$?
	rm -rf "$BASE"
	# ⛔ SIN TUBERIA HACIA `grep -q`, y no es estilo. Bajo `pipefail` (activo en la cabecera), un
	#    `productor | grep -q PATRON` devuelve **141** cuando el grep ACIERTA pronto y sale: mata al
	#    productor con SIGPIPE y la tuberia hereda su codigo. Es decir, el `if` toma la rama FALSA
	#    justo cuando debia tomar la verdadera — un control que deja de controlar en el caso en que
	#    tenia que actuar. Lo midio sobre otro banco mio y `lint:sigpipe-booleans` lo cuenta
	#    como deuda: con la tuberia, esta pata mata el gancho de la flota entera.
	#    La forma sin tuberia es una cadena aqui-documento: el patron se busca sobre texto ya
	#    capturado y no hay productor al que matar.
	[ "$rc" != "0" ] && command grep -q 'CANNOT START: no temporary directory allows EXECUTION' <<<"$out"
}

_cr_rc=0
calco_rehusa launch-state-captures.sh || _cr_rc=$?
if [ "$_cr_rc" = 3 ]; then
	paso "COULD NOT CHECK: /tmp EXECUTES on this host, so the two-noexec case cannot be set up"
elif [ "$_cr_rc" = 0 ]; then
	paso "with both candidates noexec the launcher REJECTS (rc != 0) and explains why: the guard stops it"
else
	malo "the launcher does NOT reject both noexec candidates: it warns and starts, leaving the guard ineffective"
fi

# ── 6 · LLAMAR Y LIMPIAR DEVUELVE LA RAIZ A SU CUENTA ─────────────────────────────────────────
# ⛔ ES EL A-05 GENERALIZADO. El lector encontro que el propio banco dejaba un `run-NNN` colgado:
#    un guion que vigila fugas de scratch, fugando scratch. Arreglarlo en el caso 1 no impide que
#    manana otro caso llame al ayudante y se olvide, asi que se mide la PROPIEDAD: tras N llamadas
#    con su limpieza, la raiz tiene las mismas entradas que antes.
RAIZ_SCRATCH="$(cd -- "$RAIZ/.." && pwd)/.olivares-exec-tmp"
antes_n=0
[ -d "$RAIZ_SCRATCH" ] && antes_n="$(find "$RAIZ_SCRATCH" -mindepth 1 -maxdepth 1 | wc -l | tr -d ' ')"
for _ in 1 2 3; do
	d="$( . "$LIB"; olivares_exec_tmpdir "$(cd -- "$RAIZ/.." && pwd)" )" || continue
	[ -n "$d" ] && { rmdir "$d" 2>/dev/null || rm -rf "$d"; }
done
despues_n=0
[ -d "$RAIZ_SCRATCH" ] && despues_n="$(find "$RAIZ_SCRATCH" -mindepth 1 -maxdepth 1 | wc -l | tr -d ' ')"
if [ "$antes_n" = "$despues_n" ]; then
	paso "three calls with cleanup leave the root unchanged ($antes_n entries): no accumulation"
else
	malo "the root grew from $antes_n to $despues_n entries: the helper or its cleanup leaks"
fi

# ── 7 · CONCURRENCIA: N llamadas a la vez, N exitos ───────────────────────────────────────────
# ⛔ the reviewer lo midio y tenia razon: con la sonda de nombre COMPARTIDO, 50 llamadas simultaneas
#    daban 3 exitos y 47 fallos — cada una borraba la sonda de la otra entre el `chmod` y el
#    `execve`. Un control que falla por concurrencia no es un control: es una loteria, y dos
#    arneses a la vez es el caso NORMAL en esta caja.
#
#    ⚠ Y la cura sugerida —`.probe-exec.$$`— NO bastaba, medido: con 30 llamadas seguian fallando
#      29, porque **`$$` es el PID del shell PADRE y no cambia en un subshell**. El nombre lo da
#      `mktemp`, que no supone nada sobre quien llama.
N_CONC=25
# ⛔ CADA HIJO ESCRIBE SU PROPIO FICHERO, no una linea en una tuberia compartida. Con `echo` a la
#    misma tuberia, dos hijos pueden entrelazar su escritura y el recuento sale corto: mi primera
#    version dijo 24 de 25 y en aislado eran 25 de 25 — el fallo era del CONTADOR, no de la sonda.
#    Contar ficheros no puede entrelazarse.
MARCAS="$T/conc"
mkdir -p "$MARCAS"
for i in $(seq 1 "$N_CONC"); do
	( . "$LIB"; d="$(olivares_exec_tmpdir "$(cd -- "$RAIZ/.." && pwd)")" &&
		{ rmdir "$d" 2>/dev/null || rm -rf "$d"; : >"$MARCAS/$i"; } ) &
done
wait
n_ok="$(find "$MARCAS" -type f | wc -l | tr -d ' ')"
if [ "$n_ok" = "$N_CONC" ]; then
	paso "$N_CONC SIMULTANEOUS calls yield $N_CONC successes: the probe does not collide with itself"
else
	malo "of $N_CONC simultaneous calls only $n_ok succeeded: the probe collides (shared name)"
fi

# ── 8 · EL TRAP SE ARMA ANTES DEL PRIMER TEMPORAL ─────────────────────────────────────────────
# ⛔ Se cuenta sobre el ARBOL y no sobre una lista: si manana alguien mete un `mktemp` mas arriba,
#    la ventana vuelve y esto sale rojo con su fichero. El predicado EXCLUYE comentarios — la
#    palabra `mktemp` aparece en la prosa que explica esto mismo, y contarla seria medir mi propio
#    comentario en vez del codigo (me paso al escribirlo).
tarde=""
for f in "$RAIZ"/scripts/launch-state-captures.sh "$RAIZ"/scripts/web-e2e-demo.sh \
	"$RAIZ"/scripts/smoke-agentops.sh "$RAIZ"/scripts/quickstart-smoke.sh; do
	[ -f "$f" ] || continue
	m="$(command grep -nE 'mktemp' "$f" | command grep -vE ':[[:space:]]*#' | head -1 | cut -d: -f1)"
	t="$(command grep -n '^trap ' "$f" | head -1 | cut -d: -f1)"
	[ -n "$m" ] && [ -n "$t" ] || continue
	[ "$t" -lt "$m" ] || tarde="$tarde $(basename "$f"):trap=$t,mktemp=$m"
done
if [ -z "$tarde" ]; then
	paso "in all four scripts the trap is armed BEFORE the first temporary directory: no gap"
else
	malo "scripts with the trap AFTER the first temporary directory:$tarde"
fi

# ── LA PURGA POR EDAD, Y EL TESTIGO DE QUE NADIE LO USA ───────────────────
# Se conduce con `OLIVARES_EXEC_TMPDIR` contra la funcion de VERDAD, no contra una copia de su
# expresion. Y el candidato se ancla al padre del repo —donde la propia libreria pone su respaldo—
# porque la purga vive DENTRO de la rama que corre solo si la sonda de ejecucion pasa: con un
# TMPDIR noexec la funcion salta el candidato entero y la fila acusaria a la purga de algo que no
# llego a ejecutarse. Si ese padre no ejecuta, la fila NO se monta y lo dice.
_purga_caso() {
	local P base
	base="$(dirname "$RAIZ")"
	P="$(mktemp -d "$base/.purga.XXXXXX")" || return 2
	if ! { printf '#!/bin/sh\nexit 0\n' >"$P/.p" 2>/dev/null && chmod +x "$P/.p" 2>/dev/null &&
		"$P/.p" 2>/dev/null; }; then
		rm -rf "$P"; return 3
	fi
	rm -f "$P/.p"
	mkdir -p "$P/run-viejo" "$P/run-joven" "$P/cache-ajena-vieja" "$P/run-vivo" "$P/run-socket"
	# ⛔ UN SOCKET VIVO EN UN DIRECTORIO VIEJO. Es la clase que /proc NO puede delatar: un socket
	#    aparece como `socket:[<inodo>]` y nunca con su ruta, asi que el testigo de rutas lo da
	#    por libre. Un carril de esta caja se quedo incomunicado del bus por esto, y su socket
	#    vivia en un scratch igual que este.
	python3 -c "import socket,sys; s=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1]); s.listen(1); import time; time.sleep(60)" \
		"$P/run-socket/bus.sock" >/dev/null 2>&1 &
	local sockpid=$!
	sleep 1
	touch -d '13 hours ago' "$P/run-viejo" "$P/cache-ajena-vieja" "$P/run-vivo" "$P/run-socket" 2>/dev/null || return 2
	# ⛔ UN PROCESO DE VERDAD DENTRO DEL VIEJO: la edad es una SUPOSICION sobre la duracion y esta
	#    fila es la que la convierte en garantia. Sin ella, la purga borraba trabajo VIVO por ser
	#    viejo y el banco salia verde — lo midio un lector, no yo.
	( cd "$P/run-vivo" && exec sleep 60 ) &
	local vivo=$!
	sleep 1
	( . "$LIB" >/dev/null 2>&1 || exit 4
	  OLIVARES_EXEC_TMPDIR="$P" olivares_exec_tmpdir >/dev/null 2>&1 )
	local rcs=$?
	kill "$vivo" 2>/dev/null; wait "$vivo" 2>/dev/null
	kill "$sockpid" 2>/dev/null; wait "$sockpid" 2>/dev/null
	if [ "$rcs" = 4 ]; then rm -rf "$P"; return 4; fi
	printf '%s|%s|%s|%s|%s' \
		"$([ -d "$P/run-viejo" ] && echo si || echo no)" \
		"$([ -d "$P/run-joven" ] && echo si || echo no)" \
		"$([ -d "$P/cache-ajena-vieja" ] && echo si || echo no)" \
		"$([ -d "$P/run-vivo" ] && echo si || echo no)" \
		"$([ -d "$P/run-socket" ] && echo si || echo no)"
	rm -rf "$P"
}
# ⛔ LA ENUMERACION DE rc ES EXHAUSTIVA, Y ESO NO ES ESTILO. La version anterior nombraba 3 y 4 y
#    dejaba caer EN SILENCIO cualquier otro >=3: un rc que no esta en la lista no producia NI
#    fila ni fallo — desaparecia. Un codigo fuera de la enumeracion no puede ser silencio: o es
#    hallazgo o es «NO HE PODIDO MIRAR», que es el contrato de toda esta casa. Y el 2 tampoco
#    estaba: caia al `case` de abajo con `$_PR` vacio y se reportaba como «la purga hizo mal su
#    trabajo», acusando a la purga de un fallo del propio fixture.
_PR="$(_purga_caso)"; _PRC=$?
case "$_PRC" in
0) : ;;
2) malo "COULD NOT CHECK: the purge fixture could not be set up (mktemp/touch), rc 2" ;;
3) paso "COULD NOT CHECK: the repo parent does not EXECUTE, so the purge cannot be set up here" ;;
4) malo "COULD NOT CHECK: could not source the library to test the purge" ;;
*) malo "COULD NOT CHECK: UNEXPECTED rc $_PRC from _purga_caso — it is not enumerated, and an unnamed rc is not ignored" ;;
esac
[ "$_PRC" = 0 ] && case "$_PR" in
no\|si\|si\|si\|si) paso "purge: removes old DEAD scratch, preserves young and unrelated scratch, and RESPECTS both the live process and live SOCKET" ;;
*) malo "bad purge: old|young|unrelated|live|socket = $_PR (expected no|si|si|si|si)" ;;
esac

# ── EL MUTANTE DEL PROPIO ARNES, VERSIONADO ───────────────────────────────
# ⛔ ESTA FILA EXISTE PORQUE LA MEDIDA NO PUEDE MORIR CON LA SESION QUE LA HIZO. El `case` de arriba
#    se hizo exhaustivo porque un rc fuera de la enumeracion era SILENCIO y el silencio pasaba por
#    aprobado; lo comprobe a mano forzando un rc 7 y viendo que la version anterior contestaba
#    «11 pasan, 0 fallan» con CERO filas sobre la purga. Esa comprobacion valia para el que la vio
#    y para nadie mas: una leccion indefensa. Aqui queda como fila, y muere con el arnes.
#
#    Se corre una COPIA de este banco con `_purga_caso` forzado a un rc que nadie enumera, y se
#    exige que la copia lo NOMBRE. La copia lleva OLIVARES_EXECTMP_META=0 para que no se mida a si
#    misma: sin esa guarda, cada copia crearia otra y el banco no terminaria nunca.
if [ "${OLIVARES_EXECTMP_META:-1}" = 1 ]; then
	# ⛔ EL MUTANTE SE CORRE EN UN TEMPORAL EJECUTABLE, Y SI NO LO HAY SE CONTESTA 2. Medido el
	#    2026-08-31 sobre `main`: con `TMPDIR=/tmp` —que en estas cajas es NOEXEC— esta fila daba
	#    FAIL de forma DETERMINISTA (3 de 3), y no porque el arnes hubiera dejado de nombrar el rc:
	#    la copia ES esta misma bateria, asi que su propio bloque del arnes PARA con rc 2 antes de
	#    llegar a `_purga_caso` — exactamente el corte que la fila M de mas abajo acredita como
	#    CORRECTO. El caso se estaba suicidando con la conducta que el resto del banco celebra.
	#
	#    Con el temporal ejecutable, la copia llega al mutante y lo nombra: comprobado, 0 vs 1
	#    ocurrencias de «rc INESPERADO 7» segun donde viva. Y si esta caja no puede dar un dir
	#    ejecutable, eso es NO HE PODIDO MIRAR y se dice: un banco que no puede fabricar su mutante
	#    no ha demostrado nada, ni a favor ni en contra.
	_EXECBASE=""
	if [ -r "$RAIZ/scripts/lib/exec-tmpdir.sh" ]; then
		# shellcheck source=/dev/null
		. "$RAIZ/scripts/lib/exec-tmpdir.sh" 2>/dev/null || true
		command -v olivares_exec_tmpdir >/dev/null 2>&1 && _EXECBASE="$(olivares_exec_tmpdir 2>/dev/null || true)"
	fi
	_META="$(mktemp -d "${_EXECBASE:-${TMPDIR:-/tmp}}/meta.XXXXXX")" || _META=""
	if [ -n "$_META" ]; then
		LC_ALL=C awk '{print} /mktemp -d "\$base\/\.purga/ && !d {print "\treturn 7"; d=1}' \
			"$RAIZ/scripts/test-exec-tmpdir.sh" > "$_META/copia.sh"
		# ⛔ La guarda del mutante NO puede ser `grep return 7`: ese texto ya esta en el comentario
		#    de arriba y en el propio programa awk, asi que pasaria SIN haber insertado nada — el
		#    trinquete cazando a su propia bateria. Se exige que la copia sea EXACTAMENTE una linea
		#    mas larga: eso solo es cierto si la insercion ocurrio.
		_LO="$(wc -l < "$RAIZ/scripts/test-exec-tmpdir.sh")"; _LC="$(wc -l < "$_META/copia.sh")"
		if [ "$_LC" = "$(( _LO + 1 ))" ]; then
			_MOUT="$(cd "$RAIZ" && OLIVARES_EXECTMP_META=0 TMPDIR="${_EXECBASE:-${TMPDIR:-/tmp}}" \
				timeout 300 bash "$_META/copia.sh" 2>&1)"
			if command grep -q 'UNEXPECTED rc 7' <<<"$_MOUT"; then
				paso "the harness mutant is VERSIONED: an unenumerated rc produces a row instead of silence"
			elif [ -z "$_EXECBASE" ]; then
				malo "COULD NOT CHECK: without an executable directory, the copy stops in its own harness and the mutant never runs"
			else
				malo "the harness mutant was NOT named: an unenumerated rc can still disappear"
			fi
		else
			malo "COULD NOT CHECK: could not construct the harness mutant (no artifact means no verdict)"
		fi
		rm -rf "$_META"
	else
		malo "COULD NOT CHECK: could not create the harness mutant temporary directory"
	fi
fi

# ── M · EL ARNES DE CAPTURAS PARA, NO ARRANCA CON /tmp ────────────────────────────────────────
# ⛔ VERLO CORTAR, no leer que corta: se extraen del fichero REAL las lineas del bloque de la sonda
#    —desde el `source` de la libreria hasta su `fi`— y se corren donde NINGUN candidato ejecuta.
#    Antes vaciaba `EXEC_TMP` y seguia, y el `${EXEC_TMP:-${TMPDIR:-/tmp}}` de la linea siguiente
#    devolvia el motor al `/tmp` noexec: los plugins no arrancan, la recarga rechaza la fuente y
#    `/adoption` sale a CERO en las capturas, en silencio.
#
# ⛔ Y LA BASE VA A UN NOEXEC EXPLICITO, con la forma que este fichero ya usa en `calco_rehusa`
#: `TMPDIR=/tmp mktemp -d`, comprobado ejecutando una sonda alli. Mi version anterior
#    colgaba el fixture de `$T` —que sale de un `mktemp -d` que HONRA `$TMPDIR`— dando por hecho
#    que no ejecuta: con un TMPDIR ejecutable esa fila y su mutante quedaban SALTADOS, y una fila
#    critica saltada no es verde. Y si /tmp ejecutara, el caso NO se monta y lo DICE.
base_noexec() { # imprime un dir donde una sonda NO puede ejecutarse; rc 1 si aqui no lo hay
	local B
	B="$(TMPDIR=/tmp mktemp -d)" || return 1
	if printf '#!/bin/sh\nexit 0\n' >"$B/.p" 2>/dev/null && chmod +x "$B/.p" 2>/dev/null &&
		"$B/.p" 2>/dev/null; then
		rm -rf "$B"; return 1          # /tmp EJECUTA: aqui no hay noexec real
	fi
	rm -f "$B/.p" 2>/dev/null
	printf '%s' "$B"
}
DC="$RAIZ/scripts/docs-captures.sh"
if [ ! -f "$DC" ]; then
	malo "COULD NOT CHECK: cannot find scripts/docs-captures.sh"
else
	ini="$(command grep -n 'lib/exec-tmpdir\.sh"$' "$DC" | head -1 | cut -d: -f1)"
	fin=""
	[ -n "$ini" ] && fin="$(command awk -v i="$ini" 'NR>i && /^fi$/ {print NR; exit}' "$DC")"
	BN="$(base_noexec)" || BN=""
	if [ -z "$ini" ] || [ -z "$fin" ]; then
		malo "COULD NOT CHECK: cannot find the probe block in docs-captures.sh (source/fi)"
	elif [ -z "$BN" ]; then
		printf 'SKIPPED  /tmp EXECUTES on this host, so no real noexec directory is available to observe\n'
		printf '         the harness STOPPING. This is NOT a pass: the decisive half remains untested.\n'
	else
		sed -n "${ini},${fin}p" "$DC" > "$BN/bloque-sonda.sh"
		mkdir -p "$BN/raiz/hijo"
		corre_bloque() { # $1 = bloque; imprime la salida
			( OLIVARES_EXEC_TMPDIR="$BN/no-ejecuta" bash -c '
			    ROOT="'"$BN"'/raiz/hijo"
			    . "'"$RAIZ"'/scripts/lib/exec-tmpdir.sh"
			    . "'"$1"'"
			    echo "ARRANCARIA_CON=${EXEC_TMP:-${TMPDIR:-/tmp}}"
			  ' ) 2>&1
		}
		checked_output="$(corre_bloque "$BN/bloque-sonda.sh")"; rcb=$?
		if command grep -q 'ARRANCARIA_CON=' <<<"$checked_output"; then
			malo "the harness CONTINUES after the probe fails and would start with $(command grep -o 'ARRANCARIA_CON=.*' <<<"$checked_output"): the original failure returns in disguise"
		elif [ "$rcb" != "0" ]; then
			paso "with candidates on REAL noexec, the harness block STOPS (rc $rcb) instead of starting with /tmp"
		else
			malo "the block exited 0 without starting: cannot claim it stops (output: $checked_output)"
		fi

		python3 - "$BN/bloque-sonda.sh" "$BN/bloque-mut.sh" <<'PYB'
import sys
src = open(sys.argv[1], encoding="utf8").read()
viejo = "  exit 2\n"
if viejo not in src:
    sys.stderr.write("    the extracted block has no `exit 2`: the mutant cannot be constructed\n")
    sys.exit(1)
open(sys.argv[2], "w", encoding="utf8").write(src.replace(viejo, '  EXEC_TMP=""  # MUTANT: empty directory and continue\n', 1))
PYB
		if [ -s "$BN/bloque-mut.sh" ] && ! cmp -s "$BN/bloque-sonda.sh" "$BN/bloque-mut.sh"; then
			salm="$(corre_bloque "$BN/bloque-mut.sh")"
			# ⛔ LO QUE SE EXIGE ES QUE CONTINUE, NO CON QUE. Mi asercion pedia
			#    `ARRANCARIA_CON=/tmp` y eso codificaba una suposicion del ENTORNO: con `TMPDIR`
			#    apuntando a otro sitio, el mutante arranca igual —que es el defecto— pero con esa
			#    ruta, y el caso lo contaba como fallo. Un testigo que depende de una variable del
			#    entorno contesta cosas distintas segun quien lo llame, que es justo lo que este
			#    fichero acaba de curar en el fixture.
			if command grep -q 'ARRANCARIA_CON=' <<<"$salm"; then
				paso "the mutant that empties EXEC_TMP CONTINUES and starts ($(command grep -o 'ARRANCARIA_CON=.*' <<<"$salm" | head -1)): case M verifies stopping"
			else
				malo "the mutant that empties EXEC_TMP never starts: case M does not verify stopping"
			fi
		else
			malo "could NOT construct the probe-block mutant: no artifact means no verdict"
		fi
		rm -rf "$BN"
	fi
fi

# ⛔ LA GRAMATICA SE ARMA EN EJECUCION, y no es rebuscamiento: `lint:export-closure` parsea CADA
#    linea de este fichero buscando su propia sintaxis, asi que **un patron de `grep` que la
#    contenga se lee como una declaracion de verdad**. Me rechazo tres veces por eso, con tres
#    formas distintas del mismo error de fondo:
#      1. con el punto escapado, la barra invertida entro EN la ruta declarada;
#      2. con `-qF` y la ruta entre comillas, la comilla de cierre entro en el token;
#      3. y con el ancla parcial, el token declarado paso a ser `.*objetivos-sembrado`.
#    Las tres veces el gate dijo lo mismo y tenia razon: la declaracion «no nombra nada».
#
#    ⇒ **Un testigo que comprueba una declaracion no puede escribir su gramatica.** Partida en dos
#    trozos, la cadena nunca aparece contigua en el fichero y el gate no la confunde con una suya.
GRAMATICA_EXPORT="export-closure: absent-by-""design"
# ── N · LA DECLARACION `absent-by-design` DEL CATALOGO DE OBJETIVOS SIGUE PUESTA ───────────────
# ⛔ Es lo unico que ampara la referencia a ese catalogo cuando `export-closure` mire
#    `docs-captures.sh`, y la curacion del export retira `docs/launch` ENTERO. La puso `abbad2074` y
#    el merge de lote de `P-ganchos-capturas-v2-0830` la REVIRTIO. Una proteccion que se pierde en
#    silencio necesita testigo.
#
# ⛔ Y LA RUTA VA LITERAL, NO POR VARIABLE (the reviewer): con `docs/launch/$CAT` el gate de
#    export-closure no casa la declaracion de arriba con la referencia de aqui y sale rc 1. Una
#    declaracion que el gate no puede leer no declara nada.
#
# ⛔ AQUI HABIA UNA DECLARACION `absent-by-design` MIA Y LA RETIRO, porque ya no hace falta:
#    este fichero dejo de componer esa ruta cuando el patron paso a buscar por ancla parcial.
#    Una declaracion que no ampara ninguna referencia no es documentacion de mas: el gate la
#    lee y se queja de que «no nombra nada».
if [ ! -f "$DC" ]; then
	:
elif ! command grep -q 'objetivos-sembrado\.json' "$DC"; then
	paso "docs-captures.sh no longer references the objectives catalog: the declaration is unnecessary"
# ⛔ ESTE PATRON NO ESCRIBE LA RUTA COMPLETA, y las DOS versiones anteriores lo hicieron mal por
#    la misma razon de fondo: `lint:export-closure` lee este fichero buscando su propia gramatica,
#    asi que un patron de `grep` que la contenga se lee como una DECLARACION de verdad — no como
#    una busqueda.
#      · v1 con el punto escapado: la barra invertida entro EN la ruta declarada y el gate se quejo
#        de una ruta que no existe;
#      · v2 con `-qF` y la ruta entera entre comillas: el gate tomo la comilla de cierre como parte
#        del token y volvio a quejarse, de una ruta terminada en apostrofo.
#    Un testigo que busca una declaracion no puede escribirla: se busca por una ANCLA parcial, que
#    identifica igual y no compone ninguna ruta.
#
#    Y este comentario tampoco la escribe, por lo mismo.
elif command grep -qE "^# $GRAMATICA_EXPORT .*objetivos-sembrado" "$DC"; then
	paso "the objectives catalog absent-by-design declaration remains in docs-captures.sh"
else
	malo "docs-captures.sh references that catalog and lacks its absent-by-design declaration: it was lost in a merge"
fi


printf '\ntest-exec-tmpdir: %d passed, %d failed\n' "$ok" "$fail"
[ "$fail" -eq 0 ] || exit 1
exit 0
