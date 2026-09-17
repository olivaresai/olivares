#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Bateria de scripts/check-baseline-shrink.sh — el META-GATE, el que protege contra SILENCIAR otros
# gates. Su trabajo es que un commit que quita entradas de una linea base de trinquete haya tocado
# tambien algo de lo que quita, porque «un commit que SOLO borra lineas es indistinguible de silenciar
# el gate».
#
# ⛔ POR QUE EXISTE ESTA BATERIA. El sujeto lleva su propio incidente en la cabecera (2026-08-18: una
#    rama commiteo «tighten the drift baseline from 75 to 35» — un fichero, 40 lineas menos, CERO
#    traducciones tocadas— y al integrar, `main` enrojecio por ficheros que nadie habia tocado, con
#    coste de jornada para los cinco carriles). Es una pata VIVA del gancho y hasta hoy no tenia
#    NINGUNA prueba propia: ni bateria, ni `--selftest`. La leccion estaba escrita y estaba indefensa.
#
# ⛔ Y LO CARO ES CUANDO SE NOTA, que el propio guion explica: en la rama el gate esta VERDE —base y
#    arbol concuerdan ahi— y enrojece al ATERRIZAR, cuando ya bloquea a todos.
#
# SIETE FILAS, SEIS ACREDITADAS POR MUTACION — cada una muere con su mutante y SOLO con el suyo:
#   · quitar SIN tocar el sujeto ⇒ 1        mutante: aceptar siempre            (FALSO VERDE)
#   · quitar TOCANDO el sujeto ⇒ 0          mutante: no ver nunca trabajo       (sobre-disparo)
#   · esconderlo tras comentarios ⇒ 1       mutante: contar el fichero CRUDO    (FALSO VERDE)
#   · sin tronco ⇒ 2                        mutante: devolver 1
#   · fuera de un arbol git ⇒ 2             mutante: devolver 1
#   · GIT_DIR envenenado no cambia nada     mutante: quitarle el saneo de entorno
# La septima —«crecer no es encoger»— NO esta acreditada y lo dice en su sitio: sobrevivio a dos
# mutantes porque la propiedad esta protegida dos veces. Verde no acredita; decirlo es el remedio.
#
# CONTROL DE VIDA, medido: sustituyendo el sujeto por un `exit 0`, la bateria enrojece
#   (5 filas caen). ⚠ PERO 2 SOBREVIVEN, y no es un defecto que se pueda quitar: son las
#   que aseveran «limpio», y un señuelo que no hace nada tambien sale 0. ⇒ El poder discriminante
#   de esta bateria vive en las filas que esperan 1 o 2; las de rc 0 valen como control positivo
#   —sin ellas los rojos no probarian nada— pero NO distinguen un sujeto vivo de uno muerto.
#
# HERMETICA: construye repositorios de usar y tirar y le pasa al sujeto su tronco y su lista de bases
# por entorno (`OLIVARES_BASELINE_TRUNK`, `OLIVARES_BASELINES`). No toca el repositorio vivo.
set -uo pipefail

_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# ⛔ AISLAMIENTO DE GIT ANTES DE NADA: `GIT_DIR` gana a `-C`, y una bateria que monta repos de usar y
#    tirar SIN aislar opera sobre el repositorio vivo. El sujeto ya lo hace; su bateria tambien.
. "$_env" || { echo "FATAL: no puedo cargar $_env" >&2; exit 2; }
unset _env

GATE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/check-baseline-shrink.sh"
[ -f "$GATE" ] || { echo "FATAL: no encuentro el sujeto en $GATE" >&2; exit 2; }
WORK="$(mktemp -d "${TMPDIR:-/tmp}/shrink-bat.XXXXXX")" || exit 2
trap 'rm -rf "$WORK"' EXIT INT TERM
pass=0; fail=0

repo() { # repo <nombre> <lineas-base-en-el-tronco...>  -> imprime la ruta
	local d="$WORK/$1"; shift
	mkdir -p "$d/docs" "$d/scripts"
	git -C "$d" init -q -b trunk
	git -C "$d" config user.email t@e; git -C "$d" config user.name t
	printf '%s\n' "$@" >"$d/docs/base.txt"
	printf 'x\n' >"$d/scripts/sujeto-uno.sh"
	printf 'y\n' >"$d/scripts/sujeto-dos.sh"
	git -C "$d" add -A . >/dev/null; git -C "$d" commit -qm base
	git -C "$d" checkout -q -b rama
	printf '%s' "$d"
}

corre() { # corre <dir> -> rc, salida en $OUT
	OUT="$(cd "$1" && OLIVARES_BASELINE_TRUNK=trunk OLIVARES_BASELINES=docs/base.txt bash "$GATE" 2>&1)"
}

caso() { # caso <nombre> <rc-esperado> <subcadena> <dir>
	local n="$1" want="$2" sub="$3" d="$4" rc=0
	corre "$d" || rc=$?
	if [ "$rc" -ne "$want" ]; then
		printf 'FAIL  %s: rc=%s, esperaba %s\n' "$n" "$rc" "$want"
		printf '%s\n' "$OUT" | head -4 | sed 's/^/        /'; fail=$((fail + 1)); return
	fi
	case "$OUT" in *"$sub"*) : ;; *)
		printf 'FAIL  %s: rc correcto (%s) pero no dijo %s\n' "$n" "$rc" "$sub"
		printf '%s\n' "$OUT" | head -4 | sed 's/^/        /'; fail=$((fail + 1)); return ;;
	esac
	printf 'ok    %s (rc %s)\n' "$n" "$rc"; pass=$((pass + 1))
}

# --- 1 · QUITAR SIN TOCAR SU SUJETO = HALLAZGO. Es el defecto EXACTO del incidente de 2026-08-18.
d="$(repo quita-sin-tocar scripts/sujeto-uno.sh scripts/sujeto-dos.sh)"
printf 'scripts/sujeto-uno.sh\n' >"$d/docs/base.txt"
git -C "$d" add -A . >/dev/null; git -C "$d" commit -qm 'encoge y nada mas' >/dev/null
caso "quitar una entrada SIN tocar su sujeto es hallazgo" 1 "sujeto-dos.sh" "$d"

# --- 2 · QUITAR TOCANDO SU SUJETO = LIMPIO. Sobre-disparar aqui bloquea retiradas legitimas, que es
#         el unico camino sano para bajar una deuda de trinquete.
d="$(repo quita-tocando scripts/sujeto-uno.sh scripts/sujeto-dos.sh)"
printf 'scripts/sujeto-uno.sh\n' >"$d/docs/base.txt"
printf 'y arreglada\n' >"$d/scripts/sujeto-dos.sh"
git -C "$d" add -A . >/dev/null; git -C "$d" commit -qm 'encoge Y toca su sujeto' >/dev/null
caso "quitar una entrada TOCANDO su sujeto sale limpio" 0 "" "$d"

# --- 3 · CRECER NO ES ENCOGER. Un trinquete que sube no tiene nada que justificar.
#
#         ⚠ ESTA FILA NO ESTA ACREDITADA POR MUTACION, y se dice aqui para que nadie la lea como si
#           lo estuviera. Se le probaron DOS mutantes y sobrevivio a los dos:
#             · `-lt` -> `-ne` (cualquier cambio se lee como encogimiento): 0 muertes.
#             · `-gt 0` -> `-ge 0` en la guarda de `n_quit`: 0 muertes.
#           El motivo es que la propiedad esta protegida DOS VECES: aunque el primer filtro deje pasar
#           un crecimiento, `n_quit` sale 0 —no se ha quitado nada— y el bucle no llega a juzgar. Es
#           decir, el `-lt` es una OPTIMIZACION, no la decision.
#         ⇒ Se conserva como guarda de regresion —si alguien rehace la logica, esta fila lo nota— pero
#           NO cuenta como cobertura. Una fila verde que no mata a ningun mutante es un adorno, y el
#           unico remedio honesto es decirlo en su sitio.
d="$(repo crece scripts/sujeto-uno.sh)"
printf 'scripts/sujeto-uno.sh\nscripts/sujeto-dos.sh\n' >"$d/docs/base.txt"
git -C "$d" add -A . >/dev/null; git -C "$d" commit -qm 'crece' >/dev/null
caso "una linea base que CRECE no es un encogimiento" 0 "" "$d"

# --- 4 · UNA ENTRADA QUITADA NO SE ESCONDE DETRAS DE COMENTARIOS ANADIDOS. La cuenta se hace sobre
#         lineas UTILES: si se midiera el fichero CRUDO, quitar una entrada y anadir dos comentarios
#         dejaria el fichero MAS LARGO y el encogimiento pasaria inadvertido — FALSO VERDE, que es la
#         direccion cara. La primera version de esta fila solo anadia comentarios y NO mataba a ningun
#         mutante: pasaba sin vigilar nada.
d="$(repo esconder-tras-comentarios scripts/sujeto-uno.sh scripts/sujeto-dos.sh)"
printf '# nota una\n# nota dos\n\nscripts/sujeto-uno.sh\n' >"$d/docs/base.txt"
git -C "$d" add -A . >/dev/null; git -C "$d" commit -qm 'quita una y anade comentarios' >/dev/null
caso "una entrada quitada no se esconde tras comentarios anadidos" 1 "sujeto-dos.sh" "$d"

# --- 5 · SIN TRONCO = NO HE PODIDO MIRAR (2), NUNCA 1 NI 0. Colapsar «no pude» en «hay fallo» o en
#         «esta limpio» es el fallo que esta casa ha visto mas veces.
d="$(repo sin-tronco scripts/sujeto-uno.sh)"
OUT="$(cd "$d" && OLIVARES_BASELINE_TRUNK=no-existe OLIVARES_BASELINES=docs/base.txt bash "$GATE" 2>&1)"; rc=$?
if [ "$rc" -eq 2 ] && case "$OUT" in *"NO HE PODIDO MIRAR"*) true ;; *) false ;; esac; then
	printf 'ok    sin tronco contra el que comparar es 2, no 1 ni 0 (rc 2)\n'; pass=$((pass + 1))
else
	printf 'FAIL  sin tronco: rc=%s, esperaba 2 con NO HE PODIDO MIRAR\n' "$rc"
	printf '%s\n' "$OUT" | head -3 | sed 's/^/        /'; fail=$((fail + 1))
fi

# --- 6 · FUERA DE UN ARBOL GIT = 2. Mismo motivo: la tercera respuesta tiene su clase.
OUT="$(cd "$WORK" && OLIVARES_BASELINE_TRUNK=trunk bash "$GATE" 2>&1)"; rc=$?
if [ "$rc" -eq 2 ]; then printf 'ok    fuera de un arbol git es 2 (rc 2)\n'; pass=$((pass + 1))
else printf 'FAIL  fuera de un arbol git: rc=%s, esperaba 2\n' "$rc"; fail=$((fail + 1)); fi

# --- 7 · EL ENTORNO DE GIT VA AISLADO. `GIT_DIR` GANA a `-C` y a `cd`: si el sujeto no sanea el
#         entorno, un `GIT_DIR` envenenado lo hace medir OTRO repositorio —el vivo— mientras cree que
#         mide el que tiene delante. Este gate COMPARA ARBOLES, asi que ese fallo no da error: da un
#         veredicto sobre el arbol equivocado, que es peor.
#
#         El caso: mismo repo de usar y tirar, misma retirada sin tocar el sujeto, pero con `GIT_DIR`
#         apuntando a otro sitio. Si el saneo funciona, el veredicto NO cambia: sigue siendo 1 y sigue
#         nombrando `sujeto-dos.sh`.
d="$(repo git-dir-envenenado scripts/sujeto-uno.sh scripts/sujeto-dos.sh)"
printf 'scripts/sujeto-uno.sh\n' >"$d/docs/base.txt"
git -C "$d" add -A . >/dev/null; git -C "$d" commit -qm 'encoge y nada mas' >/dev/null
_veneno="$WORK/veneno.git"; git init -q --bare "$_veneno" 2>/dev/null
OUT="$(cd "$d" && GIT_DIR="$_veneno" GIT_WORK_TREE="$WORK" \
	OLIVARES_BASELINE_TRUNK=trunk OLIVARES_BASELINES=docs/base.txt bash "$GATE" 2>&1)"; rc=$?
if [ "$rc" -eq 1 ] && case "$OUT" in *sujeto-dos.sh*) true ;; *) false ;; esac; then
	printf 'ok    un GIT_DIR envenenado NO cambia el veredicto: el entorno va saneado (rc 1)\n'; pass=$((pass + 1))
else
	printf 'FAIL  GIT_DIR envenenado: rc=%s, esperaba 1 nombrando sujeto-dos.sh\n' "$rc"
	printf '%s\n' "$OUT" | head -3 | sed 's/^/        /'; fail=$((fail + 1))
fi

printf '\ntest-check-baseline-shrink: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
