#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# test-disk-residue.sh - bateria de check-disk-residue.sh.
#
# Los casos 9-16 NO estaban en la primera version y existen porque el contraste `the model` max
# del 2026-09-02 los reprodujo como defectos: cada uno fija una direccion de error que el vigia
# cometia. Los dos mutantes que sobrevivieron a la bateria original -anular el peso de un
# directorio, y devolver una atribucion ficticia- mueren ahora con los casos 12 y 14.
#
# El 12-bis lo anadio la corrida 33965105298 del 2026-09-05: no fallaba el vigia, fallaba la
# PREMISA del caso 12, que daba por ilegible un `chmod 000` sin medir si el lector estaba negado.
# Ningun caso de aqui abajo SUPONE ya que su condicion se monto; la mide antes de afirmar nada.
#
# Los BLOQUES 17-27 -doce casos, porque el 23 lleva ademas su control- son de otro defecto, y este
# SI era del vigia y de produccion: el censo de PRIMER NIVEL se llevaba con un solo booleano, asi
# que una raiz sana borraba el fallo de otra, y el listado parcial de un `find` fallido se
# analizaba como si fuera completo. Lo reprodujo la revision independiente `the model` del
# 2026-09-05 sobre `0525eed8e1` (review/first-level-enumeration-followup.md) y cubren la tabla de
# aceptacion de ese brief, fila por fila. CINCO de ellos fallan sobre el vigia anterior y los
# demas son filas de NO-CAMBIO: 0/1 se conservan exactamente.
#
# La mitad que decide sigue siendo la que NO dispara: un vigia que acusa a un gate en curso hace
# que el carril deje de correrlo, y eso es peor que no tenerlo.
set -u
GATE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-disk-residue.sh"
casos=0
fallos=0
saltados=0

nuevo() { mktemp -d "${TMPDIR:-/tmp}/residue-test.XXXXXX"; }

# Un segmento de ruta largo, para cavar hondo con pocos `mkdir`. 200 < NAME_MAX (255) en tmpfs,
# ext4 y overlayfs, que son los tres sistemas donde corre esta bateria.
SEG="$(printf '%0200d' 0 | tr 0 p)"

# EL LECTOR DEL VIGIA, PREGUNTADO DIRECTAMENTE: ¿puede recorrer esta ruta ENTERA? Es el mismo
# `os.walk` con `onerror` que usa `peso()` en scripts/lib/disk-residue.py, asi que responde por el
# acceso REAL de este proceso en esta caja, no por unos bits de permiso.
#   0  se atasca (hay error de recorrido)   1  la recorre entera   2  no pude medir
lector_atascado() { # <ruta>
	local rc=0
	python3 - "$1" <<-'PY' || rc=$?
	import os, sys
	errs = []
	for _ in os.walk(sys.argv[1], onerror=lambda e: errs.append(e)):
	    pass
	sys.exit(0 if errs else 1)
	PY
	case "$rc" in
	0 | 1) return "$rc" ;;
	*) return 2 ;;
	esac
}

# Un descendiente que NINGUN lector puede recorrer, tenga los privilegios que tenga: la cadena se
# pasa de PATH_MAX y el kernel rechaza el nombre en `getname()` con ENAMETOOLONG, ANTES de mirar
# permisos o capacidades. Ahi no hay `root` que valga, que es justo lo que `chmod 000` no daba.
# Cava por bloques y RE-MIDE tras cada uno: la profundidad se DERIVA de PATH_MAX y luego se
# comprueba, en vez de fijar un numero magico y confiar en el.
cava_ilegible() { # <entrada> -> 0 si al terminar el lector no puede recorrerla
	local entrada="$1" pmax bloque ronda
	pmax="$(getconf PATH_MAX "$entrada" 2>/dev/null)"
	case "$pmax" in
	'' | *[!0-9]*) pmax=4096 ;;
	esac
	bloque=$((pmax / ${#SEG} + 6))
	for ronda in 1 2 3; do
		(
			cd "$entrada" 2>/dev/null || exit 1
			while cd "$SEG" 2>/dev/null; do :; done
			n=0
			while [ "$n" -lt "$bloque" ]; do
				mkdir "$SEG" 2>/dev/null && cd "$SEG" 2>/dev/null || exit 1
				n=$((n + 1))
			done
			head -c 4096 /dev/zero >blob 2>/dev/null || exit 1
		) || return 1
		lector_atascado "$entrada" && return 0
	done
	return 1
}

# El `find` REAL, en ruta ABSOLUTA. Lo usan las sondas de premisa y el shim de laboratorio.
FIND_REAL="$(command -v find)"

# ¿Enumera este lector la raiz ENTERA? Es la pregunta del censo de primer nivel, hecha con el
# mismo `find` que hace el vigia. Ningun caso de raices supone que su condicion se monto.
raiz_enumerable() { # <raiz>
	"$FIND_REAL" "$1" -maxdepth 1 -mindepth 1 -printf '%p\n' >/dev/null 2>&1
}

# Un `find` de laboratorio que emite registros VALIDOS y DESPUES falla — el contrato documentado
# de find: informa lo que pudo y sale != 0. Vive SOLO en esta bateria y se inyecta por PATH en la
# invocacion del caso: el guion de produccion no tiene, ni debe tener, ninguna variable para
# sustituir su `find`. Y sirve con y sin privilegios, que es lo que un `chmod 000` no da.
#
# ⛔ RUTA ABSOLUTA, Y SE COMPRUEBA. Un shim llamado `find` que delegue en `find` a secas se
# encuentra a SI MISMO por PATH. Medido hoy montando esta sonda: un `exec` recursivo, un proceso
# girando al 100 % de CPU sin terminar nunca. Un caso que cuelga la bateria es peor que uno que
# falla, asi que la premisa se verifica antes de escribir el shim.
#
# ⛔ Y EL SHIM NO PUEDE VIVIR EN CUALQUIER SITIO: `/tmp` y `/dev/shm` estan montados NOEXEC en
# esta caja, y ahi `chmod +x` pone el bit pero `access(X_OK)` sigue diciendo que no — asi que
# bash SALTA el shim en su busqueda de PATH y ejecuta el `find` de verdad SIN DECIR NADA. Medido
# el 2026-09-05 corriendo la bateria con TMPDIR=/tmp: tres casos dieron rc 1 en vez de 2 y el
# fallo parecia del vigia cuando era del MONTAJE. Por eso la base se elige EJECUTANDO una sonda,
# igual que hace el paso `tmpdir` de mainline-ci, y despues se comprueba el shim ejecutandolo.
SHIM_DIR=""
siembra_shim() { # -> 0 y deja el shim en $SHIM_DIR; 1 si no hay donde ejecutarlo
	SHIM_DIR=""
	case "$FIND_REAL" in
	/*) : ;;
	*) return 1 ;;
	esac
	local base dir probado
	for base in "${TMPDIR:-/tmp}" "${HOME:-}" /var/tmp; do
		[ -n "$base" ] && [ -d "$base" ] || continue
		dir="$(mktemp -d "$base/residue-shim.XXXXXX" 2>/dev/null)" || continue
		{
			printf '#!/usr/bin/env bash\n'
			# Test-only projection: keep default root discovery, but enumerate owned entries.
			# The real find and Python reader still inspect those entries and their descendants.
			printf 'root="${1:-}"\n'
			printf 'if [ -n "${RESIDUE_TEST_MAP_ROOT:-}" ] && [ "$root" = "$RESIDUE_TEST_MAP_ROOT" ]; then\n'
			printf '\tprintf "%%s\\n" "$root" >>"${RESIDUE_TEST_MAP_LOG:?}" || exit 74\n'
			printf '\tshift; set -- "${RESIDUE_TEST_MAP_TO:?}" "$@"\n'
			printf 'fi\n'
			printf 'if [ "$root" = "${FAULT_ROOT:?}" ]; then\n'
			printf '\t%s "$@"\n' "$FIND_REAL"
			printf '\texit 1\n'
			printf 'fi\n'
			printf 'exec %s "$@"\n' "$FIND_REAL"
		} >"$dir/find" 2>/dev/null || { rm -rf "$dir"; continue; }
		chmod +x "$dir/find" 2>/dev/null || { rm -rf "$dir"; continue; }
		# SE COMPRUEBA EJECUTANDO, no mirando el bit: en un montaje noexec el bit esta y la
		# ejecucion no. Se le pide que delegue sobre el propio directorio del shim.
		probado="$(FAULT_ROOT=/dev/null "$dir/find" "$dir" \
			-maxdepth 0 -printf x 2>/dev/null)"
		if [ "$probado" = x ]; then
			SHIM_DIR="$dir"
			return 0
		fi
		rm -rf "$dir"
	done
	return 1
}

# Un caso que NO puede montar su condicion se DECLARA saltado y se cuenta aparte: ni verde
# silencioso ni rojo sobre algo que no es el sujeto. El resumen final lo dice.
salta() { # <nombre> <motivo>
	saltados=$((saltados + 1))
	printf 'salta %s: %s\n' "$1" "$2"
}
# Como `espera`, pero ademas exige que un texto NO salga. Hace falta para el caso que prueba que
# una salida PARCIAL no se analiza: lo que demuestra que no se analizo es que su familia no se
# nombra en ninguna parte.
#
# ⛔ Y el texto prohibido es el NOMBRE DE LA FAMILIA, nunca la palabra «SOSPECHA»: el propio
# diagnostico del vigia dice «un CLEAN o una SOSPECHA aqui serian un veredicto categorico», asi
# que buscar esa palabra casa con su PROSA y no con su veredicto. Medido hoy: una primera version
# de esta sonda dio un falso FALLO por exactamente eso.
espera_y_nunca() { # <nombre> <dir> <rc> <texto-que-SI> <texto-que-NO> [VAR=val ...]
	local nombre="$1" dir="$2" want="$3" si="$4" no="$5"
	shift 5
	local out rc
	casos=$((casos + 1))
	out="$(env OLIVARES_RESIDUE_DIRS="$dir" "$@" bash "$GATE" 2>&1)"
	rc=$?
	if [ "$rc" != "$want" ]; then
		printf 'FALLO %s: esperaba rc %s, salio %s\n' "$nombre" "$want" "$rc"
		printf '%s\n' "$out" | sed 's/^/        /' | head -4
		fallos=$((fallos + 1))
		return
	fi
	case "$out" in
	*"$si"*) ;;
	*)
		printf 'FALLO %s: rc %s, pero nunca dijo: %s\n' "$nombre" "$rc" "$si"
		printf '%s\n' "$out" | sed 's/^/        /' | head -4
		fallos=$((fallos + 1))
		return
		;;
	esac
	case "$out" in
	*"$no"*)
		printf 'FALLO %s: rc %s, pero dijo lo que NO debia: %s\n' "$nombre" "$rc" "$no"
		printf '%s\n' "$out" | sed 's/^/        /' | head -4
		fallos=$((fallos + 1))
		;;
	*) printf 'ok    %s\n' "$nombre" ;;
	esac
}

espera() { # <nombre> <dir> <rc> <texto> [VAR=val ...]
	local nombre="$1" dir="$2" want="$3" txt="$4"
	shift 4
	local out rc
	casos=$((casos + 1))
	out="$(env OLIVARES_RESIDUE_DIRS="$dir" "$@" bash "$GATE" 2>&1)"
	rc=$?
	if [ "$rc" != "$want" ]; then
		printf 'FALLO %s: esperaba rc %s, salio %s\n' "$nombre" "$want" "$rc"
		printf '%s\n' "$out" | sed 's/^/        /' | head -4
		fallos=$((fallos + 1))
		return
	fi
	case "$out" in
	*"$txt"*) printf 'ok    %s\n' "$nombre" ;;
	*)
		printf 'FALLO %s: rc %s correcto, pero el veredicto nunca dijo: %s\n' "$nombre" "$rc" "$txt"
		printf '%s\n' "$out" | sed 's/^/        /' | head -5
		fallos=$((fallos + 1))
		;;
	esac
}

# Exercise the unchanged default discovery path: TMPDIR plus canonical /tmp.
# Only /tmp's find input is mapped to an owned directory. Its output names real
# fixture entries, so neither recursive readability nor the 0/1/2 reader is mocked.
espera_defecto() { # <name> <fixture> <rc> <text>
	local name="$1" fixture="$2" want="$3" text="$4" out rc
	casos=$((casos + 1))
	: >"$fixture/mapped.log"
	out="$(env -u OLIVARES_RESIDUE_DIRS TMPDIR="$fixture/scratch" \
		PATH="$SHIM_DIR:$PATH" FAULT_ROOT=/no-residue-test-fault \
		RESIDUE_TEST_MAP_ROOT="$traiz" RESIDUE_TEST_MAP_TO="$fixture/mapped" \
		RESIDUE_TEST_MAP_LOG="$fixture/mapped.log" bash "$GATE" 2>&1)"
	rc=$?
	if [ "$(cat "$fixture/mapped.log")" != "$traiz" ]; then
		printf 'FALLO %s: default /tmp was not enumerated exactly once through the fixture\n' "$name"
		fallos=$((fallos + 1))
	elif [ "$rc" != "$want" ] || [ "${out#*"$text"}" = "$out" ]; then
		printf 'FALLO %s: expected rc %s and %s, got rc %s\n' "$name" "$want" "$text" "$rc"
		printf '%s\n' "$out" | sed 's/^/        /' | head -4
		fallos=$((fallos + 1))
	else
		printf 'ok    %s\n' "$name"
	fi
}

# --- 1. Sospecha por los dos ejes.
d="$(nuevo)"
for i in $(seq 100 124); do head -c 3000000 /dev/zero >"$d/fuga.a${i}bcd"; done
espera "sospecha por recuento Y tamano" "$d" 1 "fuga.XXXXXX"
rm -rf "$d"

# --- 2. NO DISPARA: misma familia sin peso.
d="$(nuevo)"
for i in $(seq 100 124); do printf 'x' >"$d/pequena.a${i}bcd"; done
espera "misma familia sin peso: CLEAN" "$d" 0 "CLEAN"
rm -rf "$d"

# --- 3. Sospecha por RECUENTO solo. Es el caso `hubgate` medido: 425 ficheros y 3,9 MiB.
d="$(nuevo)"
for i in $(seq 100 219); do printf 'x' >"$d/contadas.a${i}bcd"; done
espera "sospecha por recuento aunque no pese" "$d" 1 "contadas.XXXXXX"
rm -rf "$d"

# --- 4. NO DISPARA: un cache grande de UNA entrada.
d="$(nuevo)"
mkdir -p "$d/un-cache-grande"
head -c 80000000 /dev/zero >"$d/un-cache-grande/blob"
espera "un cache grande de una entrada: CLEAN" "$d" 0 "CLEAN"
rm -rf "$d"

# --- 5. NO DISPARA: entrada con descriptor abierto.
d="$(nuevo)"
for i in $(seq 100 229); do printf 'x' >"$d/enuso.a${i}bcd"; done
sleep 25 <"$d/enuso.a100bcd" &
vivo=$!
sleep 1
espera "descriptor abierto: esa entrada no es residuo" "$d" 0 "CLEAN" \
	OLIVARES_RESIDUE_COUNT_ONLY=200
kill "$vivo" 2>/dev/null; wait "$vivo" 2>/dev/null
rm -rf "$d"

# --- 6. Tercera respuesta: sin TMPDIR legible.
espera "TMPDIR inexistente: NO PUDE MIRAR" "/no-existe-para-la-bateria" 2 "NO PUDE MIRAR"

# --- 7. Atribucion con fichero:linea.
# ⛔ EL PRODUCTOR DEL FIXTURE TIENE QUE VIAJAR CON EL ARBOL. Hasta el 2026-09-16 la familia era
#    `exportcheck.*`, cuyo productor (`check-export-closure.sh`) el export CURA: en el arbol
#    publicado la atribucion contestaba «SIN ATRIBUIR (ningun guion del arbol crea esa
#    plantilla)» y este caso —y el 14— salian rojos en `hook-only-legs` del espejo (mainline-ci
#    35127694552 en olivares.preprod, hospedado). La familia es ahora `residue-list.*`, que crea el
#    propio gate bajo prueba (`check-disk-residue.sh`), presente en el hub y en el export.
d="$(nuevo)"
for i in $(seq 100 219); do printf 'x' >"$d/residue-list.a${i}bcd"; done
espera "atribuye al productor con fichero:linea" "$d" 1 "check-disk-residue.sh:"
rm -rf "$d"

# --- 8. Y cuando no puede atribuir, lo dice.
d="$(nuevo)"
for i in $(seq 100 219); do printf 'x' >"$d/inventadoxyz.a${i}bcd"; done
espera "sin productor en el arbol: lo dice" "$d" 1 "esta FUERA del arbol"
rm -rf "$d"

# --- 9. F-02: UNA hermana viva NO absuelve a las huerfanas. El contraste creo 120, sostuvo una y
#        obtuvo CLEAN; al cerrar ese unico descriptor, FUGA, sin cambiar ningun fichero.
d="$(nuevo)"
for i in $(seq 100 219); do printf 'x' >"$d/f02.a${i}bcd"; done
sleep 25 <"$d/f02.a100bcd" &
vivo=$!
sleep 1
espera "una hermana viva no absuelve a las 119 huerfanas" "$d" 1 "f02.XXXXXX" \
	OLIVARES_RESIDUE_COUNT_ONLY=100
kill "$vivo" 2>/dev/null; wait "$vivo" 2>/dev/null
rm -rf "$d"

# --- 10. F-01: un `cwd` en un DESCENDIENTE sostiene la entrada que lo contiene.
d="$(nuevo)"
for i in $(seq 100 219); do mkdir -p "$d/f01.a${i}bcd"; done
mkdir -p "$d/f01.a100bcd/sub"
(cd "$d/f01.a100bcd/sub" && sleep 25) &
vivo=$!
sleep 1
espera "un cwd en un descendiente sostiene su entrada" "$d" 1 "119" OLIVARES_RESIDUE_COUNT_ONLY=100
kill "$vivo" 2>/dev/null; wait "$vivo" 2>/dev/null
rm -rf "$d"

# --- 11. F-03: la raiz duplicada NO se cuenta dos veces.
d="$(nuevo)"
for i in $(seq 100 159); do head -c 1000000 /dev/zero >"$d/f03.a${i}bcd"; done
espera "raiz repetida: no duplica el censo (CLEAN)" "$d $d" 0 "CLEAN" \
	OLIVARES_RESIDUE_MIN_COUNT=100 OLIVARES_RESIDUE_COUNT_ONLY=100
rm -rf "$d"

# --- 12. F-05 y MUTANTE M1: un descendiente que el lector NO PUEDE RECORRER no vale cero MiB; es
#         la tercera respuesta. Mata el mutante "peso(directorio) devuelve 0" y el mutante "el
#         error de os.walk se traga sin contarlo": los dos dan CLEAN donde esto exige rc 2.
#
# ⛔ HASTA EL 2026-09-05 ESTE CASO MONTABA SU CONDICION CON `chmod 000`, Y ESO NO ES ILEGIBILIDAD:
# es una negativa del DAC, y el DAC no se le aplica a un lector con CAP_DAC_OVERRIDE ni con
# CAP_DAC_READ_SEARCH. El servicio de estos runners corre como root -lo dice el propio workflow,
# `.github/workflows/mainline-ci.yml:641` y `:2292`: «el servicio del runner corre con HOME=/root»-,
# asi que en la corrida 33965105298 (job 101303747425, paso 23 `leg-disk-residue-selftest`) el vigia
# LEYO los 25 directorios, sumo 71,5 MiB y dijo SOSPECHA CON TODA LA RAZON: rc 1 donde el caso
# exigia 2. No fallo el vigia: fallo la premisa del CASO, que daba por denegado un permiso sin
# medirlo. Reproducido en las dos direcciones el 2026-09-05 sobre este arbol, con la misma
# plantilla y el mismo vigia: lector negado -> rc 2 y «25 entrada(s) ilegible(s)»; lector NO
# negado -> rc 1 y «25 x f05.XXXXXX  71.5 MiB», que es la linea exacta del log de aquella corrida.
#
# El montaje de ahora no depende de ningun privilegio -PATH_MAX lo corta el kernel antes de mirar
# permisos- y ademas se COMPRUEBA midiendo. El camino DAC no se pierde por eso: sigue vivo en el
# 12-bis, que ahora afirma algo en CADA rama en vez de saltarse la que no le toca.
d="$(nuevo)"
montadas=0
for i in $(seq 100 124); do
	mkdir -p "$d/f05.a${i}bcd"
	cava_ilegible "$d/f05.a${i}bcd" && montadas=$((montadas + 1))
done
if [ "$montadas" -ne 25 ]; then
	# Un caso que no puede montar su condicion NO se cuenta como aprobado: lo dice y suma fallo.
	casos=$((casos + 1))
	fallos=$((fallos + 1))
	printf 'FALLO descendientes ilegibles: no pude montar la condicion (%d de 25)\n' "$montadas"
else
	espera "descendientes ilegibles: NO PUDE MIRAR, no CLEAN" "$d" 2 "NO PUDE MIRAR"
fi
rm -rf "$d"

# --- 12-bis. EL CAMINO DAC, con su premisa MEDIDA. `chmod 000` niega al duenio sin privilegios y
#         NO niega a root, asi que la respuesta CORRECTA del vigia es distinta en cada caja. Las
#         dos se afirman aqui -un caso que se salta no mide nada, y era la mitad que faltaba:
#           . lector negado    -> rc 2: lo que no se pudo leer no se convierte en cero
#           . lector NO negado -> rc 0 CLEAN: leyo las 25 entradas y NO puede inventarse una
#             ilegibilidad a partir de unos bits de permiso que a el no le aplican
#         La segunda rama es la que la corrida 33965105298 necesitaba y no existia. Y el fixture
#         pesa 100 KiB en vez de 75 MB: el veredicto lo decide el recuento, no el tamano.
d="$(nuevo)"
for i in $(seq 100 124); do
	mkdir -p "$d/f05b.a${i}bcd"
	head -c 4096 /dev/zero >"$d/f05b.a${i}bcd/blob"
	chmod 000 "$d/f05b.a${i}bcd"
done
lector_atascado "$d/f05b.a100bcd"
visto=$?
case "$visto" in
0) espera "chmod 000 que SI niega al lector: NO PUDE MIRAR" "$d" 2 "NO PUDE MIRAR" ;;
1) espera "chmod 000 que NO niega al lector: no se inventa ilegibilidad" "$d" 0 "CLEAN" ;;
*)
	casos=$((casos + 1))
	fallos=$((fallos + 1))
	# Suponer la respuesta es EL defecto que este caso corrige: si no se puede medir, es fallo.
	printf 'FALLO chmod 000: no pude MEDIR si el lector queda fuera (rc %s)\n' "$visto"
	;;
esac
chmod -R u+rwX "$d" 2>/dev/null
rm -rf "$d"

# --- 13. Hard links: el mismo inodo no se cuenta dos veces.
d="$(nuevo)"
head -c 3000000 /dev/zero >"$d/base.dat"
for i in $(seq 100 124); do ln "$d/base.dat" "$d/f13.a${i}bcd"; done
espera "hard links al mismo inodo: no inflan el peso (CLEAN)" "$d" 0 "CLEAN" \
	OLIVARES_RESIDUE_COUNT_ONLY=100
rm -rf "$d"

# --- 14. MUTANTE M2: una atribucion ficticia no debe pasar. Se exige la LINEA real del arbol.
d="$(nuevo)"
for i in $(seq 100 219); do printf 'x' >"$d/residue-list.a${i}bcd"; done
out14="$(env OLIVARES_RESIDUE_DIRS="$d" bash "$GATE" 2>&1)"
casos=$((casos + 1))
if printf '%s' "$out14" | grep -q ':999999'; then
	printf 'FALLO CASO 14: la atribucion devolvio una linea ficticia\n'
	fallos=$((fallos + 1))
elif printf '%s' "$out14" | grep -qE 'check-disk-residue\.sh:[0-9]{1,4}\b'; then
	printf 'ok    la atribucion cita una linea real del arbol\n'
else
	printf 'FALLO CASO 14: no cito ninguna linea plausible\n'
	printf '%s\n' "$out14" | sed 's/^/        /' | head -4
	fallos=$((fallos + 1))
fi
rm -rf "$d"

# --- 15. Un umbral que no es numero es NO PUDE MIRAR, no un valor por defecto silencioso.
d="$(nuevo)"
printf 'x' >"$d/x.a100bcd"
espera "umbral no numerico: NO PUDE MIRAR" "$d" 2 "no es un entero" \
	OLIVARES_RESIDUE_MIN_COUNT=not-a-number
rm -rf "$d"

# =====================================================================================
# CONTRATO DEL CENSO DE PRIMER NIVEL (casos 17-27). Tabla de aceptacion del brief
# review/first-level-enumeration-followup.md, fila por fila.
#
# El defecto que regresionan, medido por `the model` sobre `0525eed8e1`: el censo llevaba UN
# BOOLEANO, asi que cualquier raiz que terminara 0 borraba el fallo de las demas — y el listado
# parcial que un `find` fallido ya habia escrito se analizaba como si fuera completo. Resultado:
# rc 0 CLEAN despues de omitir una raiz que se pidio.
#
# ⛔ LOS CASOS QUE DECIDEN SON 21-23, Y NO USAN PERMISOS. El mecanismo es un `find` de laboratorio
# que emite y falla: eso vale igual como root que sin privilegios. Los casos 19-20 conservan el
# camino DAC con su premisa MEDIDA y afirman en las dos ramas, como el 12-bis — bajo un lector no
# negado la respuesta correcta NO es 2, y exigirla seria inventarse un fallo.
# =====================================================================================

# --- 17. Fila 1: una raiz accesible y vacia es CLEAN. Un censo completo sobre cero entradas es
#         un veredicto, no una laguna.
d="$(nuevo)"
espera "raiz accesible y vacia: CLEAN" "$d" 0 "CLEAN"
rm -rf "$d"

# --- 18. Fila 2: una familia que cruza el umbral con censo COMPLETO sigue siendo 1.
d="$(nuevo)"
for i in $(seq 100 219); do printf 'x' >"$d/f18.a${i}bcd"; done
espera "familia sobre el umbral con censo completo: SOSPECHA" "$d" 1 "f18.XXXXXX"
rm -rf "$d"

# --- 19. Fila 3: una unica raiz que el lector no puede enumerar es 2. Premisa MEDIDA: `chmod 000`
#         no niega a root, asi que bajo un lector no negado lo correcto es leerla y decir CLEAN.
d="$(nuevo)"
mkdir -p "$d/f19"
printf 'x' >"$d/f19/dentro.a100bcd"
chmod 000 "$d/f19"
if raiz_enumerable "$d/f19"; then
	espera "raiz unica que el lector SI enumera: no se inventa fallo" "$d/f19" 0 "CLEAN"
else
	espera "raiz unica que el lector no enumera: NO PUDE MIRAR" "$d/f19" 2 "NO PUDE MIRAR"
fi
chmod -R u+rwX "$d" 2>/dev/null
rm -rf "$d"

# --- 20. Fila 4: raiz denegada + raiz accesible SIGUE siendo 2, nunca 0. Es la mitad del defecto
#         que se veia con permisos; la otra mitad, sin permisos y determinista, va en el 22.
d="$(nuevo)"
mkdir -p "$d/f20mala" "$d/f20buena"
printf 'x' >"$d/f20mala/dentro.a100bcd"
chmod 000 "$d/f20mala"
if raiz_enumerable "$d/f20mala"; then
	espera "denegada+accesible con lector no negado: CLEAN" "$d/f20mala $d/f20buena" 0 "CLEAN"
else
	espera "denegada+accesible: 2, la sana NO tapa a la otra" "$d/f20mala $d/f20buena" 2 \
		"NO PUDE MIRAR"
fi
chmod -R u+rwX "$d" 2>/dev/null
rm -rf "$d"

# --- 21. Fila 5, primera mitad: `find` emite registros validos y falla; esa raiz sola es 2 y su
#         salida NO se analiza. Determinista y sin permisos.
d="$(nuevo)"
for i in $(seq 100 219); do printf 'x' >"$d/parcial21.a${i}bcd"; done
if siembra_shim; then
	praiz="$(cd "$d" && pwd -P)"
	espera_y_nunca "find que emite y falla, raiz sola: NO PUDE MIRAR" "$praiz" 2 \
		"NO PUDE MIRAR" "parcial21.XXXXXX" \
		PATH="$SHIM_DIR:$PATH" FAULT_ROOT="$praiz"
	rm -rf "$SHIM_DIR"
else
	salta "find que emite y falla, raiz sola" "no hay base ejecutable donde poner el shim"
fi
rm -rf "$d"

# --- 22. Fila 5, segunda mitad, Y LA REGRESION DEL DEFECTO: `find` emite y falla en una raiz, otra
#         raiz enumera entera. Antes esto daba rc 0 CLEAN analizando el listado parcial. Que la
#         familia parcial NO se nombre es lo que demuestra que su salida se descarto.
d="$(nuevo)"
mkdir -p "$d/mala" "$d/buena"
for i in $(seq 100 219); do printf 'x' >"$d/mala/parcial22.a${i}bcd"; done
if siembra_shim; then
	praiz="$(cd "$d/mala" && pwd -P)"
	braiz="$(cd "$d/buena" && pwd -P)"
	espera_y_nunca "find que falla + raiz sana: 2, y la parcial NO se analiza" \
		"$praiz $braiz" 2 "NO PUDE MIRAR" "parcial22.XXXXXX" \
		PATH="$SHIM_DIR:$PATH" FAULT_ROOT="$praiz"
	rm -rf "$SHIM_DIR"
else
	salta "find que falla + raiz sana" "no hay base ejecutable donde poner el shim"
fi
rm -rf "$d"

# --- 23. Fila 6: raiz fallida + raiz con familia SOSPECHOSA. Manda el fallo: una sospecha sobre un
#         censo con agujeros no es un veredicto. La familia sospechosa no debe nombrarse.
d="$(nuevo)"
mkdir -p "$d/mala" "$d/buena"
printf 'x' >"$d/mala/algo.a100bcd"
for i in $(seq 100 219); do printf 'x' >"$d/buena/sospechosa23.a${i}bcd"; done
braiz="$(cd "$d/buena" && pwd -P)"
if siembra_shim; then
	praiz="$(cd "$d/mala" && pwd -P)"
	espera_y_nunca "fallo + sospecha: manda el fallo, no hay veredicto" \
		"$praiz $braiz" 2 "NO PUDE MIRAR" "sospechosa23.XXXXXX" \
		PATH="$SHIM_DIR:$PATH" FAULT_ROOT="$praiz"
	rm -rf "$SHIM_DIR"
else
	salta "fallo + sospecha" "no hay base ejecutable donde poner el shim"
fi
# El control que le da sentido, y corre SIEMPRE: esa MISMA raiz sana, sola y sin shim, SI dispara.
espera "control: la raiz sana sola SI dice SOSPECHA" "$braiz" 1 "sospechosa23.XXXXXX"
rm -rf "$d"

# --- 24. Fila 7: dos GRAFIAS distintas de la misma raiz son UNA raiz. Con 60 entradas y el umbral
#         en 100, duplicar el censo daria 120 y dispararia: que salga CLEAN es la prueba.
d="$(nuevo)"
for i in $(seq 100 159); do printf 'x' >"$d/f24.a${i}bcd"; done
ln -s "$d" "$d.alias"
espera "dos alias de la misma raiz: una sola obligacion, sin duplicar" "$d/. $d.alias" 0 "CLEAN" \
	OLIVARES_RESIDUE_MIN_COUNT=100 OLIVARES_RESIDUE_COUNT_ONLY=100
rm -f "$d.alias"
rm -rf "$d"

# --- 25. MODO POR DEFECTO: un candidato que EXISTE y no se deja enumerar es un fallo, no una
#         omision silenciosa. Sin OLIVARES_RESIDUE_DIRS las raices son TMPDIR y /tmp; el shim
#         hace fallar la de /tmp y el veredicto tiene que ser 2.
d="$(nuevo)"
if siembra_shim; then
	traiz="$(cd /tmp && pwd -P)"
	casos=$((casos + 1))
	out25="$(env -u OLIVARES_RESIDUE_DIRS TMPDIR="$d" PATH="$SHIM_DIR:$PATH" \
		FAULT_ROOT="$traiz" bash "$GATE" 2>&1)"
	rc25=$?
	rm -rf "$SHIM_DIR"
	if [ "$rc25" = 2 ] && [ "${out25#*NO PUDE MIRAR}" != "$out25" ]; then
		printf 'ok    modo por defecto: un candidato que no enumera NO desaparece\n'
	else
		printf 'FALLO por defecto: esperaba 2 y NO PUDE MIRAR, salio %s\n' "$rc25"
		printf '%s\n' "$out25" | sed 's/^/        /' | head -4
		fallos=$((fallos + 1))
	fi
else
	salta "por defecto: candidato que no enumera" "sin base ejecutable para el shim"
fi
rm -rf "$d"

# --- 26. Default discovery must use a fixture, not the host's shared /tmp.
# First-level enumeration does not prove descendant readability: publication-23
# reproduced a correct reader rc 2 on unreadable host entries while the old case
# demanded 0/1. Map only the /tmp enumeration to real owned entries; require that
# it actually happened, and exercise the real reader's clean, suspicion and partial
# results. Neither production discovery nor the checker has a test-only override.
d="$(nuevo)"
mkdir -p "$d/scratch" "$d/mapped"
if siembra_shim; then
	traiz="$(cd /tmp && pwd -P)"
	espera_defecto "default owned roots: CLEAN" "$d" 0 "CLEAN"
	for i in $(seq 100 219); do printf 'x' >"$d/mapped/default26.a${i}bcd"; done
	espera_defecto "default /tmp owned family: SOSPECHA" "$d" 1 "default26.XXXXXX"
	rm -f "$d/mapped"/default26.*
	for location in mapped scratch; do
		mkdir -p "$d/$location/unreadable"
		if cava_ilegible "$d/$location/unreadable"; then
			espera_defecto "default $location unreadable descendant: NO PUDE MIRAR" \
				"$d" 2 "entrada(s) ilegible(s)"
		else
			casos=$((casos + 1))
			fallos=$((fallos + 1))
			printf 'FALLO default %s: could not establish an unreadable descendant\n' "$location"
		fi
		rm -rf "$d/$location/unreadable"
	done
	rm -rf "$SHIM_DIR"
else
	salta "default discovery and reader mapping" "no executable base for the fixture shim"
fi
rm -rf "$d"

# --- 27. EL CONTRATO, dicho en la direccion que lo separa: una raiz SOLICITADA que no existe es 2
#         y se nombra como solicitada. Un candidato POR DEFECTO que no existe no lo seria — pero
#         esa rama NO se puede montar aqui y no se finge: el vigia crea sus propios temporales en
#         `${TMPDIR:-/tmp}`, asi que un TMPDIR inexistente lo mata antes de censar, y `/tmp`
#         existe. Se deja escrito en el informe en vez de fabricar la condicion.
espera "raiz SOLICITADA inexistente: 2, y se dice que se pidio" \
	"/no-existe-para-la-bateria" 2 "SOLICITADA y no existe"

# A completed find is insufficient if reading or appending its census fails.
# Isolate all checker temporaries: the append fixture changes only its own list.
for fault in reader append; do
	d="$(nuevo)"
	mkdir -p "$d/root" "$d/scratch"
	for i in $(seq 100 219); do printf 'x' >"$d/root/appendfail.a${i}bcd"; done
	if siembra_shim; then
		rm -f "$SHIM_DIR/find"
		if [ "$fault" = reader ]; then
			cat >"$SHIM_DIR/cat" <<-'SH'
			#!/usr/bin/env bash
			case "${1:-}" in "${TMPDIR:?}"/residue-root.*) exit 74 ;; esac
			exec "${RESIDUE_TEST_CAT:?}" "$@"
			SH
			chmod +x "$SHIM_DIR/cat"
		else
			cat >"$SHIM_DIR/find" <<-'SH'
			#!/usr/bin/env bash
			"${RESIDUE_TEST_FIND:?}" "$@" || exit $?
			if [ "${1:-}" = "${FAULT_ROOT:?}" ]; then
				for list in "${TMPDIR:?}"/residue-list.*; do
					[ -f "$list" ] || continue
					mv "$list" "$list.original" && mkdir "$list" || exit 75
				done
			fi
			SH
			chmod +x "$SHIM_DIR/find"
		fi
		espera_y_nunca "censo completo pero $fault falla: no hay veredicto" \
			"$d/root" 2 "no pude incorporar completo" "appendfail.XXXXXX" \
			TMPDIR="$d/scratch" PATH="$SHIM_DIR:$PATH" FAULT_ROOT="$d/root" \
			RESIDUE_TEST_CAT="$(type -P cat)" RESIDUE_TEST_FIND="$FIND_REAL"
		rm -rf "$SHIM_DIR"
	else
		salta "incorporacion del censo ($fault)" "sin base ejecutable para el shim"
	fi
	rm -rf "$d"
done

echo
if [ "$casos" -eq 0 ]; then
	echo "test-disk-residue: NINGUN CASO CORRIO - eso no es un aprobado." >&2
	exit 2
fi
resumen=""
if [ "$saltados" -ne 0 ]; then
	# Una bateria incompleta debe fallar tambien para quien solo observa su codigo de salida.
	resumen=" y $saltados saltado(s), que no son aprobados"
fi
if [ "$fallos" -ne 0 ]; then
	echo "test-disk-residue: FALLO ($fallos de $casos caso(s)$resumen)" >&2
	exit 1
fi
if [ "$saltados" -ne 0 ]; then
	echo "test-disk-residue: INCOMPLETA ($casos caso(s)$resumen)" >&2
	exit 2
fi
echo "test-disk-residue: $casos caso(s), todos se comportaron"
