#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# purge-scratch.sh [--apply] <directorio-de-scratch>
#
# ⛔ POR QUE EXISTE, y es una medida, no una precaucion. El 2026-08-31 a las 01:2xZ purgue mi
# scratch por encargo (9,2 GB -> 131 MB) y me lleve por delante `cc-socks/`: el socket del bus de
# mi propia sesion. Mi proceso siguio vivo y mi vigia tambien, pero **deje de existir para la
# flota** — fuera de `ListAgents`, y todo mensaje hacia mi rebotando. Otro carril tenia el mismo
# socket expuesto y NO se corto, con la misma orden. La diferencia no fue el cuidado: fue la FORMA.
#
# Mi purga llevaba SEIS guardas —ruta no vacia, dentro del scratch, nunca la raiz, nunca
# `claude-1000`, `cwd` por /proc para directorios y descriptores abiertos para ficheros— y
# `cc-socks/` **no falla ninguna**: ningun proceso tiene ahi su `cwd` ni un fd sobre el directorio.
#
#   Un socket unix es un NOMBRE en el sistema de ficheros para un listener que vive en el kernel.
#   Borrar el nombre no rompe ningun uso en curso: rompe la POSIBILIDAD DE SER ENCONTRADO.
#   ⇒ una guarda de «¿esta en uso?» no puede proteger de un borrado que no rompe ningun uso.
#
# Las dos propiedades que este guion tiene y mi purga no:
#
#   1 · ENUMERA LO QUE BORRA, NUNCA LO QUE CONSERVA. Una lista de exclusion («todo menos X») borra
#       todo lo que no se te ocurrio pensar; una de inclusion borra solo lo que nombraste. Mis seis
#       guardas eran profundas sobre la pregunta equivocada mientras la FORMA ya habia decidido.
#   2 · MIRA EL TIPO ANTES DE `rm -rf`. `rm -rf <dir>` es CIEGO AL TIPO: no inspecciona lo de
#       dentro. Aqui, un directorio candidato que contenga un socket o un FIFO se RECHAZA con su
#       ruta, aunque nadie lo tenga abierto — son justo las cosas que no se detectan por uso.
#
# Por defecto NO borra: imprime. Hace falta `--apply`, porque un barrido destructivo cuyo modo por
# defecto es actuar se ejecuta por accidente exactamente una vez.
set -u -o pipefail

APLICA=no
case "${1:-}" in --apply) APLICA=si; shift;; esac
DIR="${1:-${OLIVARES_SCRATCH_DIR:-}}"

# ⛔ Un argumento que no se honra vale 2, y el mensaje dice que SI se honra.
if [ -z "$DIR" ]; then
	echo "purge-scratch: NO HE PODIDO MIRAR: falta el directorio." >&2
	echo "               uso: purge-scratch.sh [--apply] <dir>   (o OLIVARES_SCRATCH_DIR)" >&2
	exit 2
fi
[ -d "$DIR" ] || { echo "purge-scratch: NO HE PODIDO MIRAR: '$DIR' no es un directorio." >&2; exit 2; }

# ⛔ CANONICALIZAR ES LO PRIMERO, Y ANTES DE TODA GUARDA. Lo levanto el lector 47 y lo reproduje:
# pasandole una ruta RELATIVA, las guardas de uso **no cortaban** y borro un fichero con descriptor
# abierto y un directorio que era el cwd de un proceso, con rc 0.
#
# El mecanismo: `/proc/<pid>/cwd` y `/proc/<pid>/fd/*` resuelven SIEMPRE a rutas absolutas, y yo
# comparaba contra la ruta **tal como llegaba**. Una relativa no casa con una absoluta, asi que el
# conjunto EN_USO no reconocia nada y todo salia como libre. La guarda existia, estaba probada... y
# solo la habia ejercitado con rutas de `mktemp -d`, que son absolutas: **nunca varie la FORMA de la
# entrada**, que es exactamente lo que hizo el lector.
#
# Y arregla de paso un segundo agujero de la misma causa: la negativa sobre raices peligrosas
# (`/`, `/workspace`, `/tmp`, `$HOME`) comparaba texto, asi que un `.` estando EN `/workspace` no
# casaba con `/workspace` y la negativa no disparaba.
if command -v realpath >/dev/null 2>&1; then
	DIR=$(realpath -m -- "$DIR") || { echo "purge-scratch: NO HE PODIDO MIRAR: no canonicalizo '$DIR'." >&2; exit 2; }
else
	DIR=$(cd -- "$DIR" 2>/dev/null && pwd -P) || { echo "purge-scratch: NO HE PODIDO MIRAR: no canonicalizo el directorio." >&2; exit 2; }
fi
case "$DIR" in /*) : ;; *) echo "purge-scratch: NO HE PODIDO MIRAR: la ruta canonica no es absoluta." >&2; exit 2;; esac
# ⛔ Y CANONICALIZAR UNA MITAD DEJA LA GUARDA IGUAL DE CIEGA. Lo levanto el lector 47 sobre la v3,
# que ya canonicalizaba el ARGUMENTO: esta comparacion enfrentaba el `$DIR` canonico contra `$HOME`
# **crudo**, asi que con HOME siendo un enlace al scratch la negativa no disparaba y la purga borro
# con rc 0 y cero rechazos. **Las referencias tambien viajan sin canonicalizar**, y una comparacion
# sirve de tan poco como su lado mas descuidado.
#
# Se canonicalizan las DOS mitades, y todas las raices, no solo la que fallo: `/tmp` puede ser un
# enlace en otra maquina y el defecto seria identico. Curar solo `$HOME` seria tratar la instancia.
_canon(){ realpath -m -- "$1" 2>/dev/null || printf '%s' "$1"; }
# ⛔ EL CENTINELA Y SU PORQUE SON DE (c4332c6b2, aterrizada en main). Mi bucle sustituye al
# `case` que ellos curaron —canonicaliza las DOS mitades, que su forma no hacia— pero su
# razonamiento vale igual y se conserva literal, porque explica por que el centinela no puede
# ser la cadena vacia. Resolver un conflicto quedandose con el propio lado NO autoriza a tirar
# el porque del otro:
# ${HOME:-/dev/null/sin-home}, y NO ${HOME:-}: bajo `set -u` un HOME sin definir mataba el guion
# (fail-closed); con ${HOME:-} el brazo se vuelve un patron VACIO que no casa con ningun
# directorio y la guarda del home desaparece EN SILENCIO (fail-open) en una herramienta que
# BORRA. El centinela no puede ser un directorio: sin HOME, el brazo simplemente no aplica y los
# otros tres siguen enteros. (2026-08-31 03:45Z, verificado en cuatro direcciones.)
for _raiz in / /workspace /tmp "${HOME:-/dev/null/sin-home}"; do
	[ -n "$_raiz" ] || continue
	if [ "$DIR" = "$(_canon "$_raiz")" ]; then
		echo "purge-scratch: ⛔ me niego: '$DIR' es (o resuelve a) '$_raiz', que no es un scratch." >&2
		exit 2
	fi
done
unset _raiz

# NOMBRES INTOCABLES: valen por ser ENCONTRABLES, no por estar abiertos. Ninguna sonda de uso los
# protege, asi que se protegen por nombre. `cc-socks` es el socket del bus; `claude-1000`, el
# scratch del arnes. Cualquier cosa cuyo valor sea que otro la halle va aqui.
INTOCABLES=(cc-socks claude-1000)

# LO QUE SE BORRA, nombrado. Nada mas se toca, pase lo que pase.
# Las clases se pueden ampliar por entorno (una caja tiene sus propios restos). Se hace ASI y no
# con una lista de exclusion editable: ampliar lo que se BORRA obliga a nombrarlo; ampliar lo que
# se conserva deja fuera lo que no se te ocurrio. Y es lo que hace comprobable la lista de nombres
# intocables: con las clases abiertas a `*`, la unica cosa que salva a `cc-socks` es su NOMBRE.
read -r -a CLASES_FICHERO <<< "${OLIVARES_PURGE_FILE_CLASSES:-tmp.*}"
read -r -a CLASES_DIR     <<< "${OLIVARES_PURGE_DIR_CLASSES:-wt-* arbol-* main-* shallow.* ec-* gp-*}"

# ⛔ GUARDAS DE USO — Y ESTAN AQUI PORQUE LA v1 NO LAS TENIA Y LA CABECERA HABLABA DE ELLAS.
# El lector 47 lo probo y lo reprodujo: un fichero con un DESCRIPTOR ABIERTO y un directorio que era
# el CWD de un proceso se borraron con rc 0. La cabecera narraba «las seis guardas» de la purga
# manual que dio origen a esto —cwd por /proc, descriptores abiertos— y un lector razonable entiende
# que la herramienta las trae. NO LAS TRAIA. Una cabecera que describe un control que el codigo no
# implementa es peor que no documentar nada: convierte una revision en una confirmacion.
#
# El conjunto de rutas EN USO se construye de una pasada y cada candidato se prueba contra el. Un
# fichero esta en uso si algun proceso lo tiene abierto; un directorio, si es el cwd de alguien o si
# cuelga de el un cwd o un fichero abierto. Y ojo: esto NO sustituye a la proteccion por NOMBRE de
# la seccion de arriba — un socket no falla ninguna prueba de uso, que es como empezo todo esto.
EN_USO=$(
	{ for l in /proc/[0-9]*/cwd; do readlink "$l" 2>/dev/null; done
	  for l in /proc/[0-9]*/fd/*; do readlink "$l" 2>/dev/null; done; } | sort -u
)
_en_uso(){
	# el candidato tambien se canonicaliza: `find` lo devuelve colgando de $DIR —ya canonico—, pero
	# una llamada futura desde otro sitio no tiene por que, y esta guarda no debe depender de eso.
	local r u
	r=$(realpath -m -- "$1" 2>/dev/null) || r="$1"
	while IFS= read -r u; do
		[ -n "$u" ] || continue
		case "$u" in "$r"|"$r"/*) return 0;; esac
	done <<< "$EN_USO"
	return 1
}

BORRA=""; RECHAZA=""
_intocable(){ local n; for n in "${INTOCABLES[@]}"; do [ "$1" = "$n" ] && return 0; done; return 1; }

_pat_ficheros(){ local pat; for pat in "${CLASES_FICHERO[@]}"; do find "$DIR" -mindepth 1 -maxdepth 1 -name "$pat" 2>/dev/null; done; }
_pat_dirs(){ local pat; for pat in "${CLASES_DIR[@]}"; do find "$DIR" -mindepth 1 -maxdepth 1 -type d -name "$pat" 2>/dev/null; done; }

while IFS= read -r f; do
	[ -n "$f" ] || continue
	b=$(basename "$f")
	if _intocable "$b"; then RECHAZA="${RECHAZA}${f}	nombre intocable"$'\n'; continue; fi
	# El tipo manda: un socket o un FIFO nunca se borran, los nombre quien los nombre.
	#
	# ⛔ ESTA COMPROBACION ES REDUNDANTE PARA EL BORRADO, Y NO SOBRA. Lo destapo su propio mutante:
	# neutralizarla NO borra el socket, porque el `[ -f "$f" ]` de dos lineas mas abajo ya lo deja
	# fuera (un socket no es un fichero regular). O sea que la guarda que yo habia NOMBRADO no era
	# la que actuaba — mi caso pasaba por otra via, que es la forma de tener un banco en verde sin
	# cubrir nada. Lo que esta linea aporta de verdad es el DIAGNOSTICO: sin ella el socket se cae
	# de la lista en SILENCIO, y un barrido que no dice por que no toco algo no ensena nada al que
	# lo lee. Por eso el mutante de esta rama se juzga por su MENSAJE, no por si el fichero
	# sobrevive. Si alguien la borra por «codigo muerto», el banco lo caza.
	if [ -S "$f" ] || [ -p "$f" ]; then RECHAZA="${RECHAZA}${f}	es socket/FIFO: vale por ser encontrable"$'\n'; continue; fi
	if _en_uso "$f"; then RECHAZA="${RECHAZA}${f}	EN USO: algun proceso lo tiene abierto"$'\n'; continue; fi
	[ -f "$f" ] && BORRA="${BORRA}${f}"$'\n'
done < <(_pat_ficheros)

while IFS= read -r d; do
	[ -n "$d" ] || continue
	b=$(basename "$d")
	if _intocable "$b"; then RECHAZA="${RECHAZA}${d}	nombre intocable"$'\n'; continue; fi
	# ⛔ AQUI ESTA LA GUARDA QUE ME FALTO: `rm -rf` no mira dentro, asi que miro yo ANTES.
	dentro=$(find "$d" \( -type s -o -type p \) 2>/dev/null | head -3)
	if [ -n "$dentro" ]; then
		RECHAZA="${RECHAZA}${d}	contiene socket/FIFO: $(printf '%s' "$dentro" | tr '\n' ' ')"$'\n'; continue
	fi
	if _en_uso "$d"; then RECHAZA="${RECHAZA}${d}	EN USO: es el cwd de un proceso o cuelga de el algo abierto"$'\n'; continue; fi
	[ -d "$d" ] && BORRA="${BORRA}${d}"$'\n'
done < <(_pat_dirs)

nb=$(printf '%s' "$BORRA" | sed '/^$/d' | wc -l)
nr=$(printf '%s' "$RECHAZA" | sed '/^$/d' | wc -l)
[ "$nr" -gt 0 ] && { echo "purge-scratch: RECHAZADOS ($nr):"; printf '%s' "$RECHAZA" | sed '/^$/d' | sed 's/^/      /'; }
echo "purge-scratch: a borrar: $nb entrada(s) de las clases nombradas."
[ "$APLICA" = no ] && { echo "purge-scratch: ENSAYO. Nada borrado; usa --apply."; exit 0; }

n=0
while IFS= read -r p; do
	[ -n "$p" ] || continue
	case "$p" in "$DIR"/*) : ;; *) echo "purge-scratch: ⛔ fuera del scratch, no toco: $p" >&2; continue;; esac
	rm -rf -- "$p" && n=$((n+1))
done < <(printf '%s' "$BORRA" | sed '/^$/d')
echo "purge-scratch: borradas $n entrada(s); $nr rechazada(s)."
