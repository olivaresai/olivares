#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# publica-buzon.sh <fichero-mensaje> <commit-msg> <BUZÓN>...
#
# Publica un mensaje en uno o varios buzones por plumbing, y NO empuja si algo falla.
#
# Existe porque el 2026-08-20 escribí el nombre del buzón de comercio a medias, dos veces,
# cuando el fichero lleva otro. No fue un typo con coste cosmético: creé un buzón que nadie lee —dos
# mensajes entregados a nadie, uno de ellos un informe entero— y puse `lint:inbox` ROJO en main
# las dos veces. El integrador lo tuvo que mover a mano dos veces.
#
# Las dos guardas que lo cierran, y ninguna depende de que yo me acuerde:
#   1 · el nombre del buzón se COMPRUEBA contra los que existen en origin/main; si no está, sale 2
#       y NO escribe nada. Un buzón nuevo se crea a propósito, nunca por un dedo.
#   2 · el gate corre ANTES del push y su rojo lo IMPIDE. Ver el rojo y empujar igual es el mismo
#       defecto que no mirarlo.
set -u -o pipefail

# ⛔ EL ENTORNO GIT AMBIENTAL GANA AL `cd`, y aquí eso publicaría en el repo equivocado.
#
# Este guion hace `cd "$OLIVARES_PUB_DIR"` y a partir de ahí opera con git — pero un `GIT_DIR`
# heredado **manda sobre el directorio de trabajo**, y git lo exporta a todo hook `pre-push`, o
# sea desde cualquier sesión en paralelo. Sin sanear, un `publish-inbox.sh` lanzado desde un hook
# leería el árbol de OTRO repositorio y empujaría desde él. Y además crea un repo desechable con
# `mktemp -d` para correr el gate: ése iría al repositorio VIVO.
#
# No es teórico en esta casa: el mismo descuido dejó en 2026-08-06 la rama del PR #526 apuntando
# a un commit de fixture. **Lo cazó `lint:git-env` en la primera versión de este fichero**, que es
# justo para lo que existe.
#
# Fail-closed: un saneador que no se puede cargar es «no he podido aislar», nunca «no hacía falta».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "publica-buzon: FATAL: no puedo cargar $_olivares_git_env (aislamiento git-env)" >&2
	exit 2
}
unset _olivares_git_env
MSG="$1"; CMSG="$2"; shift 2

# ⛔ EL SEGUNDO ARGUMENTO ES EL MENSAJE DEL COMMIT, NO UN FICHERO — y hasta hoy nadie lo miraba.
# El 2026-08-31 le pase la RUTA de mi fichero de mensaje y el asiento aterrizo en `main` con
# «/tmp/…/msg-asiento.txt» por asunto. Ningun control podia cazarlo: este guion publica por
# plumbing (`git commit-tree … -F`, mas abajo), asi que el hook `commit-msg` NO CORRE, y el guion
# validaba exhaustivamente el PRIMER argumento —cabeceras, topes, sello, buzon— y al segundo NO
# le pedia nada. La asimetria era el agujero.
#
# ⚠ Y la leccion que lo explica, por si alguien la necesita en otro guion: la regla «commit-tree
#   no dispara el hook» se me quedo pegada al COMANDO en vez de a la PROPIEDAD. La pregunta util
#   no es «¿uso commit-tree?» sino «¿QUIEN valida este mensaje?». Aqui la respuesta era: nadie.
_cmsg_rechaza() { echo "publica-buzon: ⛔ $1" >&2; exit 2; }
[ -n "${CMSG//[[:space:]]/}" ] || _cmsg_rechaza "el mensaje de commit esta vacio."
# La forma exacta del defecto medido: una RUTA donde iba el mensaje. Se nombra aparte porque el
# diagnostico «no es Conventional Commits» no le diria a nadie lo que de verdad paso.
case "$CMSG" in
	/*|./*|../*) _cmsg_rechaza "el mensaje de commit es una RUTA («${CMSG:0:60}»): el contrato es
                 <fichero-mensaje> <commit-msg>, y el segundo es EL MENSAJE, no un fichero." ;;
esac
# Fail-closed: si la sonda no puede correr, se rechaza. Un gate hereda los silencios de lo que
# envuelve, y este envuelve un `commit-tree` que no valida nada.
_cmsg_larga="$(printf '%s\n' "$CMSG" | awk 'length>100{n++} END{print n+0}')" ||
	_cmsg_rechaza "NO HE PODIDO MIRAR el mensaje de commit (awk fallo): no publico lo que no he medido."
case "$_cmsg_larga" in
	''|*[!0-9]*) _cmsg_rechaza "NO HE PODIDO MIRAR el mensaje de commit: la sonda devolvio «${_cmsg_larga:0:40}», que no es un numero." ;;
esac
[ "$_cmsg_larga" = 0 ] || _cmsg_rechaza "el mensaje de commit tiene $_cmsg_larga linea(s) de mas de 100 caracteres — el hook commit-msg las rechazaria, y aqui NO corre."
# ⛔ SIN TUBERIA, y no es estilo: `printf | head -1 | grep -q` bajo `pipefail` devuelve **141
# cuando el asunto es VALIDO** —`grep -q` casa, cierra la tuberia y `printf` muere con SIGPIPE—,
# asi que el `||` disparaba y **rechazaba un mensaje correcto**. Medido: con un cuerpo de 100 000
# bytes la tuberia da rc 141 y la forma sin tuberia da 0; con un asunto invalido, 1. Por debajo del
# bufer de la tuberia (~64 KiB) no se manifiesta, que es por que llevaba horas sin morder.
_cmsg_asunto="${CMSG%%$'\n'*}"
grep -qE '^[a-z]+(\([a-z0-9./_-]+\))?!?: .+' <<<"$_cmsg_asunto" ||
	_cmsg_rechaza "el asunto «${_cmsg_asunto:0:60}» no es Conventional Commits (tipo(ambito): resumen)."
[ -r "$MSG" ] || { echo "publica-buzon: NO HE PODIDO MIRAR: no leo $MSG" >&2; exit 2; }
# ⛔ EL MENSAJE TIENE QUE PARECER UNA ENTRADA DE BUZÓN, y esta guarda existe porque su ausencia
# me costó un commit basura en `main`. Probando las otras dos guardas lancé el guion contra un
# buzón REAL con un mensaje de un carácter, esperando que `check-inbox-headings.sh` lo rechazara.
# **No lo rechazó, y tenía razón**: ese gate valida las cabeceras que un buzón TIENE, y una «x»
# no tiene ninguna que validar. Desde su lado, una entrada que nunca se escribió y ninguna
# entrada se ven igual. La forma sólo la puede exigir quien escribe.
grep -q '^### ' -- "$MSG" || {
	echo "publica-buzon: ⛔ '$MSG' no contiene ninguna cabecera '### ' — eso no es una entrada de buzón." >&2
	echo "                 (el gate de buzones no puede cazarlo: valida las cabeceras que HAY)" >&2
	exit 2; }

# ── Guarda 4-bis · UNA ENTRADA NO ES UN BUZÓN ENTERO ─────────────────────────────────────────
# Medido el 2026-08-30 a las 13:58Z y 13:59Z: dos publicaciones recibieron como `<file>` la ruta
# de `an internal design note (not shipped) reviewer.md` —el buzón completo, 32 562 líneas, 1 328 cabeceras— y la
# guarda de arriba lo APROBÓ, porque mide FORMA (¿hay alguna cabecera?) y un buzón entero está
# lleno de cabeceras. Resultado: +97 686 líneas en TRES buzones, dos veces; uno de ellos pasó de
# 189 239 a 254 463 líneas y los vigías de la caja re-emitieron cabeceras de cuatro días antes.
# Y sobre ficheros `merge=union` un `git revert` se fusiona a NADA: la retirada fue quirúrgica.
# Esta guarda mide PERTENENCIA: un mensaje empieza por su cabecera, no por el rótulo de un buzón,
# y trae pocas cabeceras y pocas líneas. Los topes son generosos a propósito (una entrada larga
# de hoy tiene 1-3 cabeceras y <200 líneas); un buzón real los rebasa por más de un orden.
# v2 (the reviewer, 15:12Z): `wc -l` contaba 600 en un fichero de 601 líneas SIN LF final — el tope se eludía;
# y leer tres veces era coste y podía dar tres versiones. UNA lectura con awk: NR cuenta también la
# última línea sin LF; la primera línea y las cabeceras salen de la misma pasada.
# v4 (the reviewer, 2026-08-30 18:37Z, F-01): el rc de awk se OBSERVA. Este guion va con `set -u -o pipefail`
# y SIN `-e`, asi que un awk que imprimia a medias y salia 2 dejaba «1<TAB>1<TAB>### asiento parcial»
# en la variable y la guarda seguia como si hubiera medido: un fallo de la SONDA se leia como «entrada
# pequena». Un fallo de lectura es NO HE PODIDO MIRAR (rc 2), nunca «limpio». Y la FORMA de lo leido
# se comprueba tambien: dos enteros y un tabulador, o no se ha medido nada.
_pb_stats=$(awk 'NR==1{first=$0} /^### /{h++} END{printf "%d\t%d\t%s", NR, h+0, first}' < "$MSG") || {
	echo "publica-buzon: ⛔ NO HE PODIDO MIRAR '$MSG' (awk rc=$?): no publico lo que no he podido medir." >&2
	exit 2; }
_pb_re=$'^[0-9]+\t[0-9]+\t'
[[ "$_pb_stats" =~ $_pb_re ]] || {
	echo "publica-buzon: ⛔ NO HE PODIDO MIRAR '$MSG': la sonda devolvio «${_pb_stats:0:60}», que no es «lineas<TAB>cabeceras<TAB>primera»." >&2
	exit 2; }
_pb_lines=${_pb_stats%%$'\t'*}; _pb_rest=${_pb_stats#*$'\t'}
_pb_heads=${_pb_rest%%$'\t'*}; _pb_first=${_pb_rest#*$'\t'}
case "$_pb_first" in
	'# Buzón'*|'# Buzon'*)
		echo "publica-buzon: ⛔ '$MSG' empieza por «$_pb_first»: eso es el rótulo de un BUZÓN, no una entrada." >&2
		echo "                 (¿le has pasado la ruta del buzón en vez de la de tu asiento?)" >&2
		exit 2;;
esac
[ "$_pb_heads" -le 40 ] || {
	echo "publica-buzon: ⛔ '$MSG' trae $_pb_heads cabeceras '### ' (tope 40): eso es un buzón, no una entrada." >&2
	exit 2; }
[ "$_pb_lines" -le 600 ] || {
	echo "publica-buzon: ⛔ '$MSG' tiene $_pb_lines líneas (tope 600): eso no es una entrada de buzón." >&2
	exit 2; }
unset _pb_first _pb_heads _pb_lines _pb_re

# ── Guarda 5 · EL SELLO NO PUEDE VENIR DEL FUTURO ────────────────────────────────────────────
# Medido el 2026-08-20 con el reloj delante: **52 entradas con sello futuro repartidas en cuatro
# buzones, y las 52 eran mías** — cero de los otros cuatro carriles. No fue un sello suelto: era
# una secuencia MONÓTONA escrita a mano (00:35, 01:20, 02:15 … 08:25) que se lee igual que un reloj
# real, así que nadie la iba a cuestionar. Y cada una iba rotulada «(Hora de `date`.)», que es una
# afirmación de PROCEDENCIA falsa: el sello salía de mi cabeza, no del comando.
#
# El coste no es cosmético. Un buzón se lee en orden y se cita por su sello:
#   · mis entradas ordenan por delante de las de todos, para siempre;
#   · un acuse fechado DESPUÉS del mensaje que acusa invierte quién contestó a quién;
#   · y toda duración que yo publique («corrió 31 min») queda sin forma de comprobarse.
#
# Por eso la guarda mira el reloj y no mi buena voluntad: si el sello va por delante del reloj del
# contenedor más allá de la tolerancia, REHÚSA y escribe el sello correcto para que copiarlo sea más
# fácil que inventarlo. Hacia atrás no se toca: un mensaje redactado hace rato es legítimo.
# ── Guarda 6 · ESTA COPIA PUEDE SER MAS VIEJA QUE LAS GUARDAS QUE CREES TENER ─────────────────
# Lo levanto otro carril el 2026-08-20, publicando una entrada SIN sello: la guarda 5 la habria
# rechazado, pero corrio la copia de SU worktree, creado antes de que la guarda aterrizara.
#
#   "Guarda 5" en su worktree ..... 0 apariciones
#   "Guarda 5" en origin/main ..... 1
#
# ⇒ **Una guarda enviada como GUION protege solo a los arboles que la han traido, y un worktree es
# justamente el sitio que deja de traer nada en el instante en que existe.** Las guardas de HOOK no
# tienen esa propiedad —el hook que corre es el del clon compartido— y por eso esta clase no se
# habia visto: es la primera guarda de GUION que cuesta algo.
#
# Y me aplica: de mis cinco worktrees, DOS llevaban la copia sin guardas. Publico desde uno que si
# las tiene, o sea que he estado bien por la ruta que elegi, no por diseno.
#
# AVISA Y NO REHUSA, a proposito: quien esta DESARROLLANDO este guion tiene que poder ejecutarlo, y
# un rehuse aqui haria imposible probar el siguiente cambio. Pero lo dice con las dos huellas
# delante, porque el operador no va a ir a compararlas por su cuenta.
FRESCURA_REF="${OLIVARES_INBOX_FRESH_REF:-origin/main}"
mia=$(git hash-object -- "$0" 2>/dev/null || true)
suya=$(git rev-parse -q --verify "${FRESCURA_REF}:scripts/publish-inbox.sh" 2>/dev/null || true)
if [ -n "$mia" ] && [ -n "$suya" ] && [ "$mia" != "$suya" ]; then
	{
		echo "publica-buzon: ⚠ ESTA COPIA NO ES LA DE ${FRESCURA_REF}."
		echo "                 corriendo: ${mia}"
		echo "                 publicada: ${suya}"
		echo "                 Si tu arbol es anterior a una guarda, esa guarda NO te esta protegiendo:"
		echo "                 un guion viaja con el arbol, y un worktree deja de traer nada al crearse."
		echo "                 Sincroniza, o invoca la copia del clon compartido."
	} >&2
elif [ -z "$suya" ]; then
	echo "publica-buzon: ⚠ no he podido leer ${FRESCURA_REF}:scripts/publish-inbox.sh para comparar mi frescura." >&2
fi

TOLERANCIA_S=${OLIVARES_INBOX_SKEW_S:-900}
AHORA_S=$(date +%s)
sello=$(grep -m1 '^### ' -- "$MSG" | grep -oE '20[0-9]{2}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}' | head -1)
if [ -n "$sello" ]; then
	# ⛔ `-u`, Y NO ES COSMETICO: SIN EL LA GUARDA ES INERTE PARA SU PROPIA CLASE.
	# Medido el 2026-08-23 por otro carril, reproducido aqui. Las cabeceras llevan sello UTC
	# (`Z`), pero `date -d` sin `-u` los interpreta en la zona LOCAL, y estas cajas son CEST
	# (UTC+2). Con el reloj a las 01:54 UTC / 03:54 local:
	#
	#     sello CORRECTO en UTC   "01:54"  ->  sin -u: -120 min (ACEPTA)  ·  con -u:   +0 min (ACEPTA)
	#     sello MALO, local con Z "03:54"  ->  sin -u:   +0 min (ACEPTA)  ·  con -u: +119 min (RECHAZA)
	#
	# Los dos pasaban. Y hacia atras no se rechaza —con razon: un mensaje redactado hace rato es
	# legitimo—, asi que el unico error que esta guarda existe para cazar, el de rotular la hora
	# LOCAL como Z, era justo el que colaba. La guarda nacio de 52 sellos futuros y no podia ver
	# el suyo.
	sello_s=$(date -u -d "${sello/T/ }" +%s 2>/dev/null) || sello_s=""
	if [ -z "$sello_s" ]; then
		echo "publica-buzon: ⛔ NO HE PODIDO LEER el sello «$sello» con date — no publico a ciegas." >&2
		exit 2
	fi
	if [ "$sello_s" -gt "$((AHORA_S + TOLERANCIA_S))" ]; then
		echo "publica-buzon: ⛔ el sello «$sello» va $(( (sello_s - AHORA_S) / 60 )) min POR DELANTE del reloj." >&2
		echo "                 reloj del contenedor: $(date '+%Y-%m-%dT%H:%M%z')" >&2
		echo "                 usa ese, no uno inventado. (Tolerancia: ${TOLERANCIA_S}s.)" >&2
		exit 2
	fi
else
	echo "publica-buzon: ⛔ la cabecera no lleva sello AAAA-MM-DDThh:mm — un buzón se ordena por él." >&2
	exit 2
fi

[ $# -ge 1 ] || { echo "publica-buzon: sin buzones" >&2; exit 2; }
cd "${OLIVARES_PUB_DIR:?define OLIVARES_PUB_DIR con un worktree limpio}" || exit 2
git fetch -q origin main || { echo "publica-buzon: NO HE PODIDO MIRAR: fetch falló" >&2; exit 2; }
base=$(git rev-parse origin/main) || exit 2
EXIST=$(git ls-tree --name-only "$base" sessions/status/inbox/) || exit 2
for b in "$@"; do
  # ⛔ SIN TUBERÍA, y no es estilo. `printf … | grep -qxF` bajo `set -o pipefail` devuelve **141
  # CUANDO ENCUENTRA**: grep sale al primer casamiento, cierra su extremo, el productor recibe
  # SIGPIPE y pipefail propaga ese 141 — la comprobación falla justo cuando acierta, y de forma
  # intermitente. El integrador midió que esta clase es el 50 % de lo que rechaza, y yo la
  # escribí AQUÍ, en el guion que existe para no equivocarme. Lo cazó `lint:sigpipe-booleans`.
  case $'\n'"$EXIST"$'\n' in
  *$'\n'"sessions/status/inbox/${b}.md"$'\n'*) ;;
  *) {
    echo "publica-buzon: ⛔ '$b' NO es un buzón. Los que hay:" >&2
    printf '%s\n' "$EXIST" | sed 's|sessions/status/inbox/|    |;s|\.md$||' >&2
    exit 2; } ;;
  esac
done
T=$(mktemp -d "${TMPDIR:-/tmp}/pub.XXXXXX") || exit 2
[ -d "$T" ] || { echo "publica-buzon: mktemp no devolvió directorio" >&2; exit 2; }
# ⛔ Y SE BORRA AL SALIR. Sin esto el guion FUGA su propio temporal en cada invocación, y no es
# poco: monta un repo desechable para correr el gate de buzones, así que cada mensaje publicado
# deja ~15 MB. Medido el 2026-08-21 tras una jornada: **27 directorios `pub.*`, ~360 MB**, todos
# míos y todos residuo. La herramienta que existe para no cometer descuidos cometía éste.
#
# El trap va DESPUÉS de comprobar que `$T` es un directorio usable, no antes: un `rm -rf` sobre
# una variable vacía es la clase de arreglo que causa el problema que pretende evitar.
trap 'rm -rf "$T"' EXIT
export GIT_INDEX_FILE="$T/idx"; git read-tree "$base" || exit 2
for b in "$@"; do
  git cat-file -p "${base}:sessions/status/inbox/${b}.md" > "$T/cur" || exit 2
  cat "$T/cur" "$MSG" > "$T/new" || exit 2
  blob=$(git hash-object -w "$T/new") || exit 2
  git update-index --add --cacheinfo "100644,${blob},sessions/status/inbox/${b}.md" || exit 2
done
tree=$(git write-tree) || exit 2
# EL GATE, SOBRE EL ÁRBOL QUE VOY A PUBLICAR, Y ANTES DE PUBLICARLO.
#
# `check-inbox-headings.sh` se orienta con `git rev-parse --show-toplevel`, así que necesita un
# repo: se monta uno desechable con TODOS los buzones tal y como quedarían —no sólo los que toco—
# porque el gate los lee todos y un rojo ajeno también bloquea el push.
SUT="$PWD/scripts/check-inbox-headings.sh"
if [ -f "$SUT" ]; then
	G="$T/gate"; mkdir -p "$G/sessions/status/inbox" || exit 2
	git -c init.defaultBranch=main init -q "$G" || exit 2
	while IFS= read -r f; do
		[ -n "$f" ] || continue
		git cat-file -p "${tree}:${f}" > "$G/$f" || exit 2
	done <<EOF
$(git ls-tree --name-only "$tree" sessions/status/inbox/)
EOF
	if ! out=$( (cd "$G" && bash "$SUT") 2>&1 ); then
		echo "publica-buzon: ⛔ el gate de buzones RECHAZA este mensaje — NO publico." >&2
		printf '%s\n' "$out" | tail -8 >&2
		exit 1
	fi
	echo "publica-buzon: gate de buzones VERDE sobre el árbol que se va a publicar."
else
	echo "publica-buzon: ⛔ NO HE PODIDO MIRAR: no encuentro $SUT" >&2
	exit 2
fi
commit=$(git commit-tree "$tree" -p "$base" -F <(printf '%s\n' "$CMSG")) || exit 2

# ESTE PUSH NO REINTENTABA, Y CON CINCO CARRILES PUBLICANDO LA CARRERA ES ORDINARIA.
#
# Medido el 2026-08-21: un push de buzón falló porque `origin/main` se movió entre que este
# guion resolvió la base y entregó. El mensaje no se publicó y hubo que relanzarlo a mano.
# Ahora se reintenta reconstruyendo sobre el `main` nuevo — lo que vuelve a pasar por el gate
# de buzones, porque un reintento que se lo saltara sería peor que no reintentar.
#
# ⛔ Y AQUÍ IBA UN PÁRRAFO QUE DECÍA QUE LA TUBERÍA `| tail -1` HACÍA QUE EL GUION SALIERA 0
# AL FALLAR. **ES FALSO Y LO RETIRO.** Este fichero declara `set -u -o pipefail` en la línea 19,
# así que el estado del `git push` SÍ se propagaba: el viejo devolvía 1 con un remoto que
# rechazaba, medido contra un remoto de prueba con un `pre-receive` que rechaza siempre.
#
# Lo escribí leyendo el patrón en vez de ejecutándolo, y estuve a punto de acusar de un defecto
# grave a una herramienta que usan los cinco carriles. La forma explícita se queda igualmente
# —hace visible el código sin depender de que `pipefail` siga puesto mañana— pero **no arregla
# ningún fallo vivo, y decir lo contrario habría sido el fallo.**
if git push --no-verify origin "${commit}:refs/heads/main" > "$T/push.out" 2>&1; then
	tail -1 "$T/push.out"
	exit 0
fi
tail -3 "$T/push.out" >&2

# ANTES DE REINTENTAR, COMPROBAR QUE DE VERDAD NO LLEGÓ. Un fallo de red puede haber entregado
# igual, y un reintento a ciegas duplicaría el mensaje en los cinco buzones.
if git fetch -q origin main 2>/dev/null && git merge-base --is-ancestor "$commit" origin/main 2>/dev/null; then
	echo "publica-buzon: el push devolvió error pero el commit SÍ está en origin/main — no reintento." >&2
	exit 0
fi

intento="${OLIVARES_PUB_INTENTO:-0}"
if [ "$intento" -lt 2 ]; then
	echo "publica-buzon: carrera con otro carril; reconstruyo sobre el main nuevo (intento $((intento + 2)) de 3)." >&2
	# `exec` REEMPLAZA el proceso, asi que el `trap … EXIT` de arriba NO se dispara y el
	# temporal quedaria huerfano en cada reintento. Se limpia a mano justo antes.
	rm -rf -- "$T"
	OLIVARES_PUB_INTENTO=$((intento + 1)) exec bash "$0" "$MSG" "$CMSG" "$@"
fi
echo "publica-buzon: ⛔ NO PUBLICADO tras 3 intentos. El mensaje sigue en $MSG." >&2
exit 1
