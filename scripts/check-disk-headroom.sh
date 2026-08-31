#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-disk-headroom.sh — refuse to start a gate that the disk cannot finish.
#
# CENSUS-SUBJECT: external
#   Su sujeto es el DISCO, no el repositorio: pasar sobre un árbol vacío es CORRECTO, no un verde a
#   ciegas. Lo declara aquí y no en una lista del censo, porque una lista que enumera miembros a
#   mano caduca en silencio — el censo la lee de cada gate y por tanto no puede quedarse vieja.
#
# WHY THIS EXISTS, measured 2026-08-10. The eleven-module build died with
#
#   link: mapping output file failed: no space left on device
#
# and that reads EXACTLY like a compiler error. The tree was innocent: /workspace was at
# 100% with 888G used and 1.3G free. The cause was 31 single-use GOCACHE directories, one
# per finished Codex run (gocache-codex-k1-rest, gocache-k1-eventing, …), several GB each.
# Removing the dead ones took free space from 2G to 22G and the same build returned 0.
#
# So this is not housekeeping: it is the THREE ANSWERS applied to the machine. Without it a
# gate cannot tell "the code is broken" from "I could not look", and the default reading —
# the one a human makes at speed — is the wrong one.
#
# SECOND SUBJECT, added 2026-08-19: /tmp is a DIFFERENT filesystem and it was unmeasured.
# This gate watched /workspace (a large disk) with a 15G floor and never looked at the tmp
# filesystem. Measured on this container the same day: /tmp is a 2048M **tmpfs** — that is
# RAM — at 65% used, 733M free. The biggest single consumer is 964M of accumulated agent
# scratchpads under /tmp/claude-1000, and there were 47.520 leaked `olivares-prepush-refs.*`
# files, one per push, from a missing trap in our own pre-push hook (fixed the same day).
#
# The stale `mktemp -d` residue is 60M across 753 directories — censused as 0 dirty trees
# and 0 unpublished commits, so residue and not lost work. It is written here as 60M and
# NOT as the 704M first claimed, because that figure was `du -sm` rounding each of 753
# directories up to one megabyte — 11,7x the real figure: an artefact of the measurement, in exactly the shape this
# script's own tmp_hogs() documents. It is corrected here rather than quietly dropped.
#
# It matters because a 15G floor is not just wrong for a 2G filesystem, it is UNREACHABLE:
# pointing the old gate at /tmp would have answered BROKEN forever. So the floor here is
# sized to the filesystem, and the honest label on it is PROVISIONAL — the 15G figure came
# from a measured build, this one has no equivalent measurement behind it. What is measured
# is the ACCUMULATION rate, and that is what the floor is for.
#
# And /tmp is not incidental to a gate: it is where `mktemp -d` lands by default, where Go
# and Node put scratch, and where unix sockets are bound. An ENOSPC there surfaces as a
# compiler error, a test failure or a `bind: invalid argument` — never as "the disk is full",
# which is the exact misreading this gate was written to stop.
#
#   CLEAN       enough headroom to run a full gate
#   BROKEN      below the floor; a heavy gate WILL die and blame the code (exit 1)
#   UNVERIFIED  df could not be read; nothing was measured (exit 2)
#
# The floor is 15G because that is the measured cost of one full local gate: the heavy lane
# links eleven modules plus the connector plugin binaries. The warning band is 30G so a lane
# is told BEFORE the next agent's cache pushes it over.
set -euo pipefail

# SELF-TEST. A gate nobody proved can go red is a decoration, and this one is a single
# comparison — exactly the shape that rots into "always green" without anybody noticing.
# Three legs, one per answer, each forcing the condition rather than asserting the current
# state of the disk (which would make the battery depend on how full the machine happens to
# be — a test that passes for the wrong reason).
if [ "${1:-}" = "--selftest" ]; then
	skipped=0
	fail=0
	out="$(OLIVARES_DISK_FLOOR_GB=999999 bash "$0" /workspace 2>&1)" && rc=0 || rc=$?
	if [ "$rc" = "1" ] && grep -q '^check-disk-headroom: BROKEN' <<<"$out"; then
		echo "  ok    an impossible floor is BROKEN, and it says so"
	else
		echo "  FAIL  an impossible floor did not go red (rc=$rc)"; fail=1
	fi
	out="$(bash "$0" /nonexistent-path-for-selftest 2>&1)" && rc=0 || rc=$?
	if [ "$rc" = "2" ] && grep -q 'UNVERIFIED' <<<"$out"; then
		echo "  ok    an unreadable target is UNVERIFIED, not CLEAN"
	else
		echo "  FAIL  an unreadable target did not answer UNVERIFIED (rc=$rc)"; fail=1
	fi
	out="$(OLIVARES_DISK_FLOOR_GB=0 OLIVARES_DISK_WARN_GB=0 bash "$0" /workspace 2>&1)" && rc=0 || rc=$?
	if [ "$rc" = "0" ] && grep -q '^check-disk-headroom: CLEAN' <<<"$out"; then
		echo "  ok    a floor of zero is CLEAN (the non-firing direction)"
	else
		echo "  FAIL  a floor of zero did not stay green (rc=$rc)"; fail=1
	fi
	# Las tres de arriba pasan un objetivo EXPLICITO, asi que apagan la pata del tmpfs. Estas
	# tres la ejercitan, y la primera comprueba ademas que el rojo NOMBRA el sistema de
	# ficheros correcto: un BROKEN que senale a /workspace cuando el que se ha llenado es
	# /tmp manda a arreglar la maquina equivocada.
	out="$(OLIVARES_TMP_FLOOR_MB=99999999 OLIVARES_DISK_FLOOR_GB=0 OLIVARES_DISK_WARN_GB=0 bash "$0" 2>&1)" && rc=0 || rc=$?
	if [ "$rc" = "1" ] && grep -q 'BROKEN' <<<"$out" && grep -q "${TMPDIR:-/tmp}" <<<"$out"; then
		echo "  ok    un suelo imposible en el tmpfs es BROKEN, y nombra ESE punto de montaje"
	else
		echo "  FAIL  el suelo del tmpfs no se puso rojo o senalo al disco equivocado (rc=$rc)"; fail=1
	fi
	out="$(OLIVARES_TMP_TARGET=/nonexistent-tmp-for-selftest OLIVARES_DISK_FLOOR_GB=0 OLIVARES_DISK_WARN_GB=0 bash "$0" 2>&1)" && rc=0 || rc=$?
	if [ "$rc" = "2" ] && grep -q 'UNVERIFIED' <<<"$out"; then
		echo "  ok    un tmpfs ilegible es UNVERIFIED, no CLEAN"
	else
		echo "  FAIL  un tmpfs ilegible no respondio UNVERIFIED (rc=$rc)"; fail=1
	fi
	out="$(OLIVARES_TMP_FLOOR_MB=0 OLIVARES_TMP_WARN_MB=0 OLIVARES_DISK_FLOOR_GB=0 OLIVARES_DISK_WARN_GB=0 bash "$0" 2>&1)" && rc=0 || rc=$?
	if [ "$rc" = "0" ] && grep -q '^check-disk-headroom: CLEAN' <<<"$out"; then
		echo "  ok    un suelo de cero en el tmpfs sigue verde (la direccion que no dispara)"
	else
		echo "  FAIL  el suelo de cero del tmpfs no se quedo verde (rc=$rc)"; fail=1
	fi
	# Las dos del censo acotado. La PRIMERA prueba que el corte se anuncia; la SEGUNDA, que
	# el corte no toca el veredicto — que es lo unico que hace aceptable acotar un diagnostico.
	# ⛔ EL FIXTURE LO CONSTRUYE EL TEST, no la caja. Antes este caso exigia la cadena `censo
	# CORTADO`, que solo aparece si existen directorios `gocache-*`: sin ellos el glob no casa, `du`
	# sale rc=1 y NUNCA llega a 124. Pasaba aqui porque esta caja tiene residuo y FALLABA en la del
	# integrador, que hoy dejo de tenerlo. Un test que depende de la suciedad del entorno no prueba
	# el codigo: prueba la caja.
	# ⛔ LA RAIZ DEL FIXTURE SE PRUEBA, NO SE SUPONE. Aqui decia «el shim va bajo $TMPDIR
	# (=/workspace/...), no bajo /tmp, que esta montado NOEXEC» — y eso es una SUPOSICION sobre una
	# variable que este guion no fija y que `.githooks/pre-push` tampoco: el hook solo la CONSUME,
	# siempre como `${TMPDIR:-/tmp}` (:141, :200, :220, :462). Medido el 2026-08-23 sobre esta misma
	# rama: `TMPDIR=/workspace/... --selftest` da 13/13 y `env -u TMPDIR --selftest` da FAILED,
	# porque el shim aterriza en /tmp NOEXEC, no se ejecuta, `du` no se hace lento y el censo no se
	# corta. Es el MISMO defecto que este commit vino a arreglar —un caso que depende del entorno—
	# con otra variable: antes exigia una caja sucia, ahora exigia un TMPDIR concreto.
	#
	# Se prueba EJECUTANDO: se crea un guion de un byte y se corre. Un `-x` no vale, porque NOEXEC
	# es del montaje y `chmod` miente sobre el.
	FIXROOT=""
	for _cand in "${TMPDIR:-}" /workspace/.olivares-tmptest/tmp /workspace/.olivares-tmptest .; do
		[ -n "$_cand" ] && [ -d "$_cand" ] || continue
		_probe="$(mktemp -d "$_cand/hogsprobe.XXXXXX" 2>/dev/null)" || continue
		printf '#!/usr/bin/env bash\nexit 0\n' > "$_probe/x" 2>/dev/null
		chmod +x "$_probe/x" 2>/dev/null
		if "$_probe/x" >/dev/null 2>&1; then FIXROOT="$_cand"; rm -rf "$_probe"; break; fi
		rm -rf "$_probe"
	done
	if [ -z "$FIXROOT" ]; then
		# NO se cuenta como aprobado ni como fallo: se dice. Un caso que no puede montarse y se
		# escribe igual que un `ok` es un pase silencioso, y este guion ya pago ese precio una vez.
		echo "  skip  el corte del censo: NO MEDIBLE, no hay ninguna raiz donde ejecutar el shim"
		skipped=$((skipped + 1))
		FIX=""
	else
	FIX="$(mktemp -d "$FIXROOT/hogsfix.XXXXXX")" || { echo "  FAIL  no pude crear el fixture"; fail=1; }
	trap 'rm -rf "${FIX:-}"' EXIT
	mkdir -p "$FIX/gocache-selftest" "$FIX/otro" "$FIX/bin"
	# ⛔ NO SE FUERZA EL CORTE CON TAMANO: eso es una CARRERA. Mi primera version llenaba el
	# fixture con 8 directorios de 4 KiB y `du` terminaba en 68 KiB antes de que la cota de 1 ms
	# mordiera — el caso pasaba o fallaba segun la maquina, que es la clase de test que aprueba
	# por suerte. Se fuerza con un `du` LENTO en el PATH: el corte deja de depender del reloj.
	#
	# El shim va bajo $FIXROOT, que es una raiz PROBADA ejecutable arriba — no bajo una supuesta.
	printf '#!/usr/bin/env bash\nsleep 5\n' > "$FIX/bin/du"
	chmod +x "$FIX/bin/du"
	out="$(PATH="$FIX/bin:$PATH" OLIVARES_HOGS_ROOT="$FIX" OLIVARES_HOGS_TIMEOUT_S=0.001 OLIVARES_DISK_FLOOR_GB=999999 bash "$0" /workspace 2>&1)" && rc=0 || rc=$?
	# Se exigen LOS DOS censos por su nombre, y no es celo: con `grep 'censo CORTADO'` a
	# secas, quitarle el timeout al censo CARO —el de 11.353 entradas, el unico que costaba
	# seis minutos— dejaba la prueba en VERDE, porque el barato seguia cortandose y la
	# satisfacia el solo. Un mutante lo enseño. Con dos guardas capaces de disparar la misma
	# asercion, mides la que salta primero y no la que te importa.
	if grep -q 'gocache: censo CORTADO' <<<"$out" && grep -q 'tmp entries: censo CORTADO' <<<"$out"; then
		echo "  ok    LOS DOS censos anuncian su corte, cada uno con su nombre"
	else
		echo "  FAIL  algun censo se corto en silencio, o no se corto (rc=$rc)"; fail=1
	fi
	if [ "$rc" = "1" ] && grep -q '^check-disk-headroom: BROKEN' <<<"$out"; then
		echo "  ok    y el veredicto sigue siendo BROKEN: el censo es diagnostico, no decision"
	else
		echo "  FAIL  cortar el censo cambio el veredicto (rc=$rc)"; fail=1
	fi
	fi
	# LA COTA NO SE PUEDE DESACTIVAR CON UN VALOR HOSTIL. `timeout 0` en GNU coreutils significa
	# SIN LIMITE, `--version` sale 0 imprimiendo su version —que se ordenaba y se imprimia como si
	# fueran datos del censo— y `abc`/`-1` salen 125 y saltaban el censo en silencio. Se prueban
	# los tres porque fallan de TRES maneras distintas: sin cota, con datos falsos, y sin censo.
	for hostil in 0 --version abc; do
		out="$(OLIVARES_HOGS_TIMEOUT_S="$hostil" OLIVARES_DISK_FLOOR_GB=999999 bash "$0" /workspace 2>&1)" && rc=0 || rc=$?
		if grep -q 'no es un numero de segundos' <<<"$out" &&
			! grep -qi 'GNU coreutils\|Free Software Foundation' <<<"$out"; then
			echo "  ok    OLIVARES_HOGS_TIMEOUT_S=$hostil: se rechaza y se DICE, sin fabricar salida"
		else
			echo "  FAIL  OLIVARES_HOGS_TIMEOUT_S=$hostil paso sin aviso o colo texto ajeno (rc=$rc)"; fail=1
		fi
	done

	# EL CONTROL INERTE, que es el que da valor a los tres de arriba: un valor VALIDO no debe
	# disparar el aviso. Sin este caso, un validador que rechazara SIEMPRE los pasaria todos y
	# dejaria la cota fija en 5s ignorando lo que pida quien la configura.
	out="$(OLIVARES_HOGS_TIMEOUT_S=0.001 OLIVARES_DISK_FLOOR_GB=999999 bash "$0" /workspace 2>&1)" && rc=0 || rc=$?
	if grep -q 'no es un numero de segundos' <<<"$out"; then
		echo "  FAIL  un valor VALIDO (0.001) disparo el aviso: el validador rechaza de mas"; fail=1
	else
		echo "  ok    un valor valido NO dispara el aviso (control inerte)"
	fi

	# LA RUTA DE PRODUCCION NO SE REDIRIGE EN SILENCIO. La raiz es configurable para que el caso
	# del corte pueda construir su entrada; el precio es que alguien podria cambiar el DEFECTO y
	# dejar el censo mirando otro sitio sin que nada chille. Se fija aqui, por forma.
	if grep -q 'HOGS_ROOT="${OLIVARES_HOGS_ROOT:-/workspace/.olivares-tmptest}"' "$0"; then
		echo "  ok    la raiz por defecto sigue siendo la de produccion"
	else
		echo "  FAIL  el censo por defecto ya no apunta a /workspace/.olivares-tmptest" >&2; fail=1
	fi
	# El recuento nombra los saltados. Un caso que no pudo montarse y desaparece del total se lee
	# como uno que paso, y esa es la forma mas barata de una bateria que miente hacia arriba.
	[ "$fail" = "0" ] && { echo "check-disk-headroom selftest: $((13 - skipped)) passed, 0 failed, $skipped no medible(s) en esta caja"; exit 0; }
	echo "check-disk-headroom selftest: FAILED"; exit 1
fi

TARGET="${1:-${OLIVARES_DISK_TARGET:-/workspace}}"
FLOOR_GB="${OLIVARES_DISK_FLOOR_GB:-15}"
WARN_GB="${OLIVARES_DISK_WARN_GB:-30}"

# El sistema de ficheros temporal, en MEGAS: es otro dispositivo, suele ser tmpfs (RAM) y es
# dos ordenes de magnitud mas pequeno, asi que un suelo en gigas no se le puede aplicar.
TMP_TARGET="${OLIVARES_TMP_TARGET:-${TMPDIR:-/tmp}}"
TMP_FLOOR_MB="${OLIVARES_TMP_FLOOR_MB:-512}"
TMP_WARN_MB="${OLIVARES_TMP_WARN_MB:-1024}"

# Se salta SOLO cuando quien llama pide un objetivo concreto (el autotest y las llamadas con
# argumento), porque entonces la pregunta es sobre ESE punto de montaje y nada mas.
CHECK_TMP=1
[ -n "${1:-}" ] && CHECK_TMP=0
[ -n "${OLIVARES_DISK_TARGET:-}" ] && CHECK_TMP=0

avail_gb="$(df -BG --output=avail "$TARGET" 2>/dev/null | tail -1 | tr -dc '0-9')" || true
if [ -z "${avail_gb:-}" ]; then
	echo "check-disk-headroom: UNVERIFIED — df could not read ${TARGET}; nothing was measured." >&2
	exit 2
fi
pct="$(df --output=pcent "$TARGET" 2>/dev/null | tail -1 | tr -dc '0-9')"

# hogs_presupuesto — el segundero de la cota, VALIDADO antes de dárselo a `timeout`.
#
# ⛔ MEDIDO por el contraste the model del 2026-08-22 sobre esta misma rama: el valor se pasaba
# tal cual, y eso abria tres agujeros distintos, los tres SILENCIOSOS:
#
#   OLIVARES_HOGS_TIMEOUT_S=0     -> en GNU coreutils CERO significa SIN LIMITE, asi que
#                                    desactivaba exactamente la cota que esta rama añade.
#   =-1  o  =abc                  -> `timeout` sale 125 y el censo se saltaba sin decir nada.
#   =--version                    -> `timeout` sale 0 imprimiendo su VERSION, y ese texto se
#                                    ordenaba y se imprimia COMO SI FUERAN DATOS DEL CENSO.
#
# El tercero es el peor: no es un hueco, es salida FABRICADA. Por eso se valida la FORMA —un numero
# de segundos positivo y acotado— en vez de filtrar valores conocidos: una lista negra deja fuera el
# siguiente que a nadie se le ocurrio.
#
# Aviso y valor por defecto, NO rechazo: el censo es diagnostico y corre DESPUES de que el veredicto
# este tomado, asi que tumbar un push por una variable mal escrita costaria mas de lo que protege.
# Lo que no se tolera es el silencio.
# ⛔ LA RAIZ DEL CENSO ES CONFIGURABLE, y no es una comodidad: sin esto el selftest NO PUEDE
# construir su entrada. Lo midio el integrador el 2026-08-22 sobre una caja LIMPIA — la suya, que
# hoy dejo de tener residuo: mi caso del corte exigia la cadena `censo CORTADO`, que solo aparece si
# hay directorios `gocache-*`; sin ellos el glob no casa, `du` sale rc=1 y **nunca llega a 124**.
# O sea: **un test que solo pasaba en una caja SUCIA**, y aqui pasaba porque esta caja tiene dos.
#
# El valor por defecto es EXACTAMENTE la ruta de produccion, y hay un caso que lo fija: redirigir el
# censo de produccion en silencio seria peor que el defecto que esto arregla.
HOGS_ROOT="${OLIVARES_HOGS_ROOT:-/workspace/.olivares-tmptest}"

hogs_presupuesto() {
	local crudo="${OLIVARES_HOGS_TIMEOUT_S:-5}"
	# Entero o decimal, sin signo ni sufijo de unidad, y con tope: `1e100` e `inf` los acepta
	# `timeout` y dejan la cota inservible.
	if [[ "$crudo" =~ ^([0-9]+|[0-9]*\.[0-9]+)$ ]] &&
		[[ ! "$crudo" =~ ^0*\.?0*$ ]] &&
		[ "${crudo%%.*}" -le 600 ] 2>/dev/null; then
		printf '%s' "$crudo"
		return 0
	fi
	printf 'check-disk-headroom: OLIVARES_HOGS_TIMEOUT_S=%s no es un numero de segundos entre 0 y 600;\n' "$crudo" >&2
	printf '  uso 5s. Un valor invalido NO desactiva la cota en silencio.\n' >&2
	printf '5'
}

hogs() {
	# Named, not guessed: the two shapes that actually filled it.
	#
	# ⛔ AQUI DECIA «Bounded depth so this stays instant — a gate pre-check must never itself
	# be the slow thing». Las dos mitades eran falsas, y la segunda describia exactamente el
	# defecto que este codigo tenia: **`du -sh X/` NO acota la profundidad.** `-s` es un
	# RESUMEN, o sea recursion completa de cada entrada; no hay `--max-depth` en ninguna parte.
	#
	# MEDIDO el 2026-08-21 en esta caja: el glob `/workspace/.olivares-tmptest/*/` casa
	# **11.353 directorios** y el censo tardo **~6 min** dentro del carril rapido de un push.
	#
	# Y el filo que lo convierte en impuesto de todos: **no lo paga quien tiene el disco
	# lleno.** `--selftest` fuerza a proposito la rama BROKEN para comprobar que se pone roja,
	# asi que el censo corre **en cada push**, con 115G libres y sin que nadie lo haya pedido.
	# Un pre-check de seis minutos es justo lo que su propio comentario prohibia.
	#
	# ⇒ El censo se ACOTA en tiempo y, cuando se corta, SE DICE. Y se dice con el numero de
	# entradas, que es la cifra que de verdad diagnostica «esto se ha llenado de directorios»
	# y cuesta un `ls`. Callarse el corte seria peor que no censar: una lista corta se lee
	# como «no hay mas consumidores».
	#
	# EL VEREDICTO NO DEPENDE DE ESTO. hogs() es diagnostico: corre DESPUES de que la decision
	# este tomada e impresa, y un censo cortado no puede convertir un BROKEN en CLEAN.
	local t crudo rc n
	t="$(hogs_presupuesto)"
	# `crudo="$(...)"; rc=$?` NO vale con `set -e`: el fallo de la sustitucion aborta la
	# funcion antes de llegar al `if`. La forma de abajo es la que ya usa el --selftest.
	crudo="$(timeout "$t" du -shx "$HOGS_ROOT"/gocache-* 2>/dev/null)" && rc=0 || rc=$?
	if [ "$rc" -eq 124 ]; then
		echo "gocache: censo CORTADO a los ${t}s — no medido"
	elif [ "$rc" -ne 0 ]; then
		# NI limpio NI cortado: NO PUDE MIRAR. Antes caia en el `elif [ -n "$crudo" ]` de abajo
		# y, con la salida vacia, no se imprimia NADA — indistinguible de «no hay consumidores».
		# `timeout` sale 125 si su primer argumento no le vale, 126/127 si no puede ejecutar `du`,
		# y 128+N si murio por señal.
		echo "gocache: censo NO EJECUTADO (rc=${rc}) — no medido, y esto NO es «no hay nada»"
	elif [ -n "$crudo" ]; then
		# awk como consumidor final, no `head`: cerrar la tuberia pronto manda SIGPIPE a
		# `sort` y bajo `pipefail` la funcion saldria 141 habiendo funcionado. Es la misma
		# razon que tmp_hogs() ya documenta mas abajo.
		sort -rh <<<"$crudo" | awk 'NR <= 5'
	fi
	crudo="$(timeout "$t" du -shx "$HOGS_ROOT"/*/ 2>/dev/null)" && rc=0 || rc=$?
	if [ "$rc" -eq 124 ]; then
		n="$(find "$HOGS_ROOT" -maxdepth 1 -mindepth 1 -type d 2>/dev/null | wc -l)"
		echo "tmp entries: censo CORTADO a los ${t}s sobre ${n} directorios — no medido."
		echo "  Ese numero ES el diagnostico: con tantas entradas el reparto por tamaño no cabe"
		echo "  en el carril rapido, y el consumidor casi seguro es la CANTIDAD, no una grande."
	elif [ "$rc" -ne 0 ]; then
		echo "tmp entries: censo NO EJECUTADO (rc=${rc}) — no medido, y esto NO es «no hay nada»"
	elif [ -n "$crudo" ]; then
		sort -rh <<<"$crudo" | awk 'NR <= 5'
	fi
}

if [ "$avail_gb" -lt "$FLOOR_GB" ]; then
	echo "check-disk-headroom: BROKEN — ${avail_gb}G free on ${TARGET} (${pct}% used), floor is ${FLOOR_GB}G."
	echo "  A heavy gate needs ~15G to link eleven modules and the connector plugins. Starting one"
	echo "  now produces 'no space left on device', which reads as a COMPILER error and is not one."
	echo "  Largest consumers:"
	hogs | sed 's/^/    /'
	echo "  Usual cause: one GOCACHE per agent run. Reuse a shared cache; do not mint a new one."
	echo "  NEVER 'go clean -cache': it is shared by three containers and kills the other lanes."
	exit 1
fi

if [ "$avail_gb" -lt "$WARN_GB" ]; then
	echo "check-disk-headroom: CLEAN — ${avail_gb}G free (${pct}% used), above the ${FLOOR_GB}G floor,"
	echo "  but under the ${WARN_GB}G warning band. Largest consumers:"
	hogs | sed 's/^/    /'
	exit 0
fi

if [ "$CHECK_TMP" = "1" ]; then
	tmp_avail_mb="$(df -BM --output=avail "$TMP_TARGET" 2>/dev/null | tail -1 | tr -dc '0-9')" || true
	if [ -z "${tmp_avail_mb:-}" ]; then
		echo "check-disk-headroom: UNVERIFIED — df no pudo leer ${TMP_TARGET}; el sistema de" >&2
		echo "  ficheros temporal NO se ha medido, y eso no es lo mismo que estar sano." >&2
		exit 2
	fi
	tmp_size_mb="$(df -BM --output=size "$TMP_TARGET" 2>/dev/null | tail -1 | tr -dc '0-9')"
	tmp_pct="$(df --output=pcent "$TMP_TARGET" 2>/dev/null | tail -1 | tr -dc '0-9')"
	tmp_fs="$(df --output=source "$TMP_TARGET" 2>/dev/null | tail -1 | tr -d ' ')"

	tmp_hogs() {
		# NO borra nada: en esta caja conviven varias sesiones y el arbol de trabajo de otro
		# carril no se tira por higiene propia.
		#
		# Tres trampas, las tres pisadas al escribir esto, las tres del mismo genero — un
		# numero o una lista que salen con aplomo y son falsos:
		#
		# 1. `du -sm "$DIR"/*` murio con «Argument list too long» (52.608 entradas) y el
		#    2>/dev/null convirtio el error en lista VACIA: el gate anunciaba «Consumidores:»
		#    y no enseñaba ninguno. De ahi find -print0 | xargs -0.
		# 2. Sumar `du -sh` da basura: awk lee "512K" como 512 y "1.2M" como 1.2, asi que
		#    0,7G de restos se imprimieron como 0.1G.
		# 3. Y sumar `du -sm` TAMPOCO vale aqui: redondea CADA entrada a 1M como minimo, y
		#    con 49.933 ficheros sueltos invento 52,3G «en uso» dentro de un tmpfs de 2G.
		#    Por eso se mide en KILOS y el total ocupado se toma de df, que es la fuente.
		local listado usado_mb
		usado_mb=$(( tmp_size_mb - tmp_avail_mb ))
		# El `|| true` no es descuido: xargs sale 123 en cuanto UNA entrada es de otro uid y
		# du no puede leerla, y bajo `set -e` eso abortaria la funcion dejando, otra vez, la
		# lista vacia. Enumerar de menos es aceptable en un diagnostico; callarse entero, no.
		listado="$(find "$TMP_TARGET" -maxdepth 1 -mindepth 1 -print0 2>/dev/null |
			xargs -0 -r du -sk 2>/dev/null | sort -rn -k1,1 || true)"
		if [ -z "$listado" ]; then
			echo "NO HE PODIDO MIRAR: ni find ni du enumeraron ${TMP_TARGET}. La cifra de df es"
			echo "buena; el reparto por consumidor NO — no lo leas como «no hay consumidores»."
			return 0
		fi
		# Todo en UN awk: ni `head` ni `sed` detras, porque cerrar la tuberia manda SIGPIPE al
		# printf y bajo `pipefail` la funcion sale 141 habiendo funcionado.
		printf '%s\n' "$listado" | awk -v usado="$usado_mb" '
			{ if ($2 ~ /\/tmp\.[^\/]*$/) { resto += $1; n++ }
			  if (NR <= 5) { top[NR] = $0 } ; ultima = NR }
			END {
				if (n > 0) printf "%d directorios tipo mktemp sin recoger: %.1fG de los %.1fG ocupados\n", n, resto/1048576, usado/1024
				for (i = 1; i <= 5 && i <= ultima; i++) {
					split(top[i], c, "\t")
					printf "%.0fM\t%s\n", c[1]/1024, c[2]
				}
			}'
	}


	if [ "$tmp_avail_mb" -lt "$TMP_FLOOR_MB" ]; then
		echo "check-disk-headroom: BROKEN — ${tmp_avail_mb}M libres en ${TMP_TARGET} (${tmp_fs}, ${tmp_size_mb}M, ${tmp_pct}% usado),"
		echo "  suelo ${TMP_FLOOR_MB}M. NO es el mismo disco que ${TARGET}, que esta bien."
		echo "  Ahi caen 'mktemp -d', el scratch de Go y de Node y los sockets unix. Quedarse sin"
		echo "  sitio ahi NO dice 'disco lleno': dice error de compilador, test roto o"
		echo "  'bind: invalid argument'. Consumidores:"
		tmp_hogs | sed 's/^/    /'
		echo "  Si es tmpfs, ademas es RAM: lo que se acumule ahi se lo quita al gate."
		exit 1
	fi
	if [ "$tmp_avail_mb" -lt "$TMP_WARN_MB" ]; then
		echo "check-disk-headroom: CLEAN — ${tmp_avail_mb}M libres en ${TMP_TARGET} (${tmp_fs}, ${tmp_pct}% usado),"
		echo "  por encima del suelo de ${TMP_FLOOR_MB}M pero dentro de la banda de aviso. Consumidores:"
		tmp_hogs | sed 's/^/    /'
	fi
fi

echo "check-disk-headroom: CLEAN — ${avail_gb}G free on ${TARGET} (${pct}% used)."
