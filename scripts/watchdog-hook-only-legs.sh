#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# watchdog-hook-only-legs.sh — a repository gate. Las patas que el gancho corre y `mainline-ci` NO ve.
#
# ⛔ POR QUÉ EXISTE, con la medida del 2026-08-29 delante. La paridad gancho<->CI corre en los DOS
#    sentidos y el sentido caro no estaba mapeado: **149 patas viven sólo en el gancho**. Una que
#    se pone roja sobre `main` no la ve NINGÚN job: la descubre el siguiente carril que empuja,
#    horas dentro de su gate, y la descubren TODOS a la vez porque el gancho corre las mismas
#    patas en todas las cajas. Ese día pasó cuatro veces.
#
# ⛔ POR QUÉ NO ES UN GATE, y es la mitad del diseño. Colgar 149 patas del gancho sería cobrarle a
#    cada push lo que ya paga; meterlas en un job de `mainline-ci` sería **reconstruir el gate
#    pesado dentro de CI, a mano**. El job `hook-only-legs` cubre CUATRO —las que ese día
#    enrojecieron `main`— y ésas se ganaron el sitio una a una. Las otras 145 no caben en esa
#    forma. Un centinela post-merge sí: corre sobre `main` limpio DESPUÉS del lote, informa y no
#    bloquea a nadie. Mismo razonamiento que `watchdog-unpublished-work.sh` (a repository gate).
#
# ⛔ Y LA DIFERENCIA ENTERA CON LO QUE YA SE HACE A MANO ES DE DÓNDE SALE LA LISTA.
#    Desde las 18:02Z del 08-29 el integrador corre a mano TRES patas ELEGIDAS. Este guion no
#    elige: **CONSUME** la lista que `scripts/check-gate-parity.sh --print` deriva en cada corrida.
#    Una lista elegida envejece con el criterio de quien la eligió; una derivada envejece con el
#    árbol, que es lo único que queremos que la mueva.
#
#    ⚠ Y por eso NO acepta un fichero con la lista dentro, ni siquiera como caché. Copiar la
#    salida de `--print` a un fichero y leer el fichero es **una lista escrita a mano con otro
#    nombre** — la forma de gate que este repositorio ha encontrado rota más veces (el censo de
#    rutas, el mapa canon<->paquete, el allowlist por ruta, la tabla BRAND_DARK que el 08-29 tumbó
#    el paso 14 del job `web`). La única entrada es el comando.
#
# LAS TRES FORMAS DE QUE UN CENTINELA NAZCA INÚTIL, y lo que hace éste contra cada una:
#
#   1. su lista se congela        -> se deriva EN CADA CORRIDA; sin `--print` sale 2, nunca lista
#   2. su silencio parece éxito   -> `2` es «no he podido mirar» y NO es verde (regla dura 5)
#   3. mide un `main` que ya no existe -> publica el SHA que midió, y REHÚSA si ese SHA no está
#                                    publicado: `main` avanza ~1 commit cada 3,4 min, así que un
#                                    veredicto sin sujeto identificable es un veredicto sobre nada
#
# SALIDAS: 0 = todas las patas medidas salieron limpias · 1 = al menos una roja (se nombra, con su
#          rc) · 2 = NO HE PODIDO MIRAR. La tercera nunca se confunde con la primera: un centinela
#          que calla cuando no puede medir es peor que no tenerlo, porque su silencio se lee como
#          verde en el sitio donde nadie va a volver a mirar.
#
# ⛔ LO QUE ESTE GUION **NO** HACE, declarado tras el contraste `sol max` porque el nombre promete
#    mas que el mecanismo — y un centinela que certifica de mas es el fallo que existe para cortar:
#
#   · NO acota cada pata en el tiempo, y la decision esta TOMADA Y DECLARADA en vez de improvisada
#     (2026-08-30): no hay `timeout` por pata. El motivo es que el corte cambia el CONTRATO —una
#     pata cortada no es un hallazgo (1) sino un «no he podido mirar» (2), porque no termino de
#     medir— y elegir el numero con 149 patas de duraciones desconocidas seria inventarlo. ⇒ una
#     pata colgada cuelga la corrida, y eso se ve: el centinela no publica nada. Se cierra cuando
#     exista UNA corrida real de la que sacar las duraciones; hasta entonces es un limite con
#     fecha, no un olvido.
#   · `OLIVARES_WATCHDOG_SKIP_PUBLISHED=1` DESACTIVA la comprobacion del sujeto. Existe para el
#     banco; en produccion su uso invalida la mitad del contrato, y nada aqui lo impide.
#   · `OLIVARES_WATCHDOG_MIN_LEGS`, `OLIVARES_WATCHDOG_MIN_TASKS` y `OLIVARES_WATCHDOG_MAX_BEHIND`
#     son alterables desde el entorno: quien los baje puede aflojar los tres controles positivos,
#     y el guion no lo sabra. Existen asi porque el banco hermetico tiene 7 tareas y el arbol real
#     719: un umbral fijo o cegaria al banco o no protegeria al arbol.
#   · `OLIVARES_PARITY_CMD` puede venir de FUERA del arbol medido. No se prohibe —el banco lo
#     necesita— pero se PUBLICA en el veredicto.
#   · NADIE lo invoca todavia: no esta en el gancho ni en ningun workflow. Un control que nadie
#     ejecuta calla, y callar se lee como verde. Las dos formas que caben y su coste estan en la
#     ficha de sesion; elegir cual es decision de cadencia y no de este guion.
#
# USO:  watchdog-hook-only-legs.sh [--limit N] [--only <pata>[,<pata>...]] [--dry]
#         --limit N   mide sólo las N primeras de la lista derivada (la lista sigue siendo la
#                     derivada: se ACOTA, no se elige). Sin él, las mide todas.
#         --only      mide sólo esas, y REHÚSA si alguna no está en la lista derivada — así no se
#                     puede colar una pata que la derivación no contiene.
#         --dry       deriva y enumera, sin ejecutar ninguna pata.
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || echo "")}"
[ -n "$RAIZ" ] || { echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: no estoy en un repositorio." >&2; exit 2; }
cd "$RAIZ" || { echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: no puedo entrar en '$RAIZ'." >&2; exit 2; }

PARIDAD="${OLIVARES_PARITY_CMD:-scripts/check-gate-parity.sh}"
LIMITE=0
SOLO=""
SOLO_DADO=0
DRY=0
while [ $# -gt 0 ]; do
	case "$1" in
	--limit) LIMITE="${2:-0}"; shift 2 ;;
	--only) SOLO="${2:-}"; SOLO_DADO=1; shift 2 ;;
	--dry) DRY=1; shift ;;
	*) echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: opción desconocida '$1'." >&2; exit 2 ;;
	esac
done

# --- 1 · EL SUJETO: qué árbol se está midiendo, y si se puede nombrar --------------------------
# Un veredicto sin sujeto publicable no vale: `main` avanza ~1 commit cada 3,4 min (medido por r25
# el 2026-08-26), así que «las patas están verdes» sin decir SOBRE QUÉ es una frase sobre nada.
SHA="$(git rev-parse HEAD 2>/dev/null)" || {
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: no hay HEAD." >&2; exit 2; }
# ⛔ UN `git status` QUE FALLA NO ES UN ARBOL LIMPIO. La version anterior hacia
# `git status --porcelain 2>/dev/null | grep -c . || true` y eso convierte un fallo SIN stdout en
# **SUCIO=0**, es decir en «limpio»: el `|| true` se traga el rc y `grep -c` cuenta cero lineas de
# una salida vacia. Lo aislo el contraste `sol max` (A-03) con una sonda que movia SOLO esa
# dimension. Es la tercera respuesta otra vez: no poder mirar el arbol no es que el arbol este
# bien.
if ! ESTADO="$(git status --porcelain 2>/dev/null)"; then
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: \`git status --porcelain\` fallo." >&2
	echo "                          Sin poder leer el estado del arbol no se puede afirmar que" >&2
	echo "                          este limpio, y un fallo sin salida se cuenta como cero." >&2
	exit 2
fi
SUCIO="$(printf '%s\n' "$ESTADO" | grep -c . || true)"
if [ "${SUCIO:-1}" -ne 0 ]; then
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: el árbol tiene $SUCIO cambio(s) sin commitear." >&2
	echo "                          El centinela mide un ÁRBOL PUBLICADO. Con el árbol sucio, un rojo" >&2
	echo "                          puede ser de lo que hay encima y no de lo que se publicó." >&2
	exit 2
fi

# ¿está PUBLICADO ese SHA? Sin remoto alcanzable no se adivina: se sale con 2.
if [ "${OLIVARES_WATCHDOG_SKIP_PUBLISHED:-0}" != "1" ]; then
	if ! git fetch -q origin main 2>/dev/null; then
		echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: el fetch de origin/main falló." >&2
		echo "                          Medir contra un origin congelado convierte «no he podido" >&2
		echo "                          mirar» en «no hay nada», que es el defecto que este guion" >&2
		echo "                          existe para no cometer." >&2
		exit 2
	fi
	if ! git merge-base --is-ancestor "$SHA" origin/main 2>/dev/null; then
		echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: $SHA no está en origin/main." >&2
		echo "                          Un centinela post-merge mide lo que YA aterrizó. Sobre un" >&2
		echo "                          SHA sin publicar, su veredicto no tiene sujeto que nadie" >&2
		echo "                          pueda comprobar después." >&2
		exit 2
	fi
	# ⛔ SER ANCESTRO NO BASTA, Y ESTE ERA UN SUJETO INCOMPLETO (contraste `sol max`): con sólo
	# `--is-ancestor`, un `main` de hace CIEN commits pasaba y el veredicto salía verde sobre un
	# árbol que ya no es el de nadie. Un centinela POST-MERGE mide lo que acaba de aterrizar.
	# Se exige la PUNTA, y si se acepta ir por detrás **la distancia se declara y se publica**:
	# el número va en el veredicto, no en la confianza de quien lo lee.
	DETRAS="$(git rev-list --count "$SHA..origin/main" 2>/dev/null || echo "")"
	if [ -z "$DETRAS" ]; then
		echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: no puedo contar la distancia a origin/main." >&2
		exit 2
	fi
	MAX_DETRAS="${OLIVARES_WATCHDOG_MAX_BEHIND:-0}"
	if [ "$DETRAS" -gt 0 ] && [ "$DETRAS" -le "$MAX_DETRAS" ]; then
		# ⛔ SI SE ACEPTA IR POR DETRAS, LA DISTANCIA SE PUBLICA — siempre, no sólo al rechazar.
		# El contraste `sol max` (v2) lo midió: con MAX_BEHIND=1 el guion aceptaba un ancestro y
		# el VERDE no decía cuánto. Un veredicto que acepta medir un árbol anterior y no dice
		# cuál, describe un sujeto que el lector no puede reconstruir.
		echo "watchdog-hook-only-legs: ⚠ mido un ANCESTRO: HEAD va $DETRAS commit(s) por detrás de origin/main (permitido: $MAX_DETRAS)"
	fi
	if [ "$DETRAS" -gt "$MAX_DETRAS" ]; then
		echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: HEAD va $DETRAS commit(s) por detrás de origin/main (máximo $MAX_DETRAS)." >&2
		echo "                          Ser ANCESTRO no basta: un \`main\` viejo también lo es, y su" >&2
		echo "                          veredicto describiría un árbol que ya no existe. Sube" >&2
		echo "                          OLIVARES_WATCHDOG_MAX_BEHIND si quieres medir a sabiendas" >&2
		echo "                          uno anterior — pero entonces la distancia va en el asiento." >&2
		exit 2
	fi
fi

# --- 2 · LA LISTA: derivada en esta corrida, nunca leída de un fichero -------------------------
command -v task >/dev/null 2>&1 || {
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: no encuentro \`task\`; sin él no hay patas." >&2
	exit 2; }
[ -r "$PARIDAD" ] || {
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: no se lee $PARIDAD." >&2
	echo "                          Ese comando ES la entrada: la lista se DERIVA en cada corrida." >&2
	echo "                          Sin él no hay lista, y una lista guardada de otra corrida sería" >&2
	echo "                          una lista escrita a mano con otro nombre." >&2
	exit 2; }

SALIDA_PARIDAD="$(bash "$PARIDAD" --print 2>&1)"
RC_PARIDAD=$?
if [ "$RC_PARIDAD" -ne 0 ]; then
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: '$PARIDAD --print' salió $RC_PARIDAD." >&2
	printf '%s\n' "$SALIDA_PARIDAD" | sed 's/^/                          /' | tail -6 >&2
	exit 2
fi

# El bloque SOLO-GANCHO de `--print`. Se pela el rótulo «(informa, no bloquea)» que la salida pega
# al nombre de las patas no bloqueantes: sin pelarlo entra como parte del nombre y produce una
# «tarea» que go-task no conoce — lo avisó una revisión, que casi lo publica como hallazgo.
BLOQUE="$(printf '%s\n' "$SALIDA_PARIDAD" | sed -n '/^--- SOLO-GANCHO/,/^--- /p')"
# Candidatas: todo lo que hay DENTRO del bloque y no es cabecera ni linea en blanco. Se pela el
# rotulo «(informa, no bloquea)» que `--print` pega al nombre de las no bloqueantes — aviso de
# una revisión, que casi lo publica como «tarea que go-task no conoce».
CANDIDATAS="$(printf '%s\n' "$BLOQUE" | grep -v '^--- ' \
	| sed -e 's/([^)]*)[[:space:]]*$//' -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' | grep -c . || true)"
LISTA="$(printf '%s\n' "$BLOQUE" | grep -v '^--- ' \
	| sed -e 's/([^)]*)[[:space:]]*$//' -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' \
	| grep -E '^[a-z0-9][a-z0-9:._-]*$' | sort -u)"
N_LISTA="$(printf '%s\n' "$LISTA" | grep -c . || true)"


# ⛔ LO QUE NO PARSEA **REHUSA**, NO SE DESCARTA EN SILENCIO. Encontrado releyendo este guion
# contra su propia lista de limites: una pata con MAYUSCULA (`Beta:Selftest`) la tiraba el
# `grep -E '^[a-z0-9]…'` **sin decir nada**, y el centinela habria publicado «medi N de N» con la
# N ya recortada. Un descarte silencioso es peor que un rechazo: el rechazo se ve.
#
# Hoy no es alcanzable —todos los nombres de tarea del arbol son minusculas— y por eso no cambia
# ningun veredicto de hoy. Manana lo es en cuanto `--print` gane una sub-cabecera, una nota o un
# nombre con otra forma. La doctrina del canon es la misma que en el clasificador del gancho: lo
# que no se reconoce cae al lado ESTRICTO. Puede costar una revision de mas; nunca una pata menos.
if [ "${CANDIDATAS:-0}" -ne "${N_LISTA:-0}" ]; then
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: el bloque SOLO-GANCHO trae $CANDIDATAS linea(s) con contenido y solo $N_LISTA parsean como nombre de pata." >&2
	echo "                          Las que no parsean:" >&2
	printf '%s\n' "$BLOQUE" | grep -v '^--- ' \
		| sed -e 's/([^)]*)[[:space:]]*$//' -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' \
		| grep -v -E '^[a-z0-9][a-z0-9:._-]*$' | grep . | sed 's/^/                            /' >&2
	echo "                          No las descarto: si la derivacion trae algo que no entiendo," >&2
	echo "                          medir el resto seria publicar un subconjunto como si fuera el todo." >&2
	exit 2
fi

# El cardinal del productor se comprueba DESPUES de la forma, y el orden importa: si una linea
# no parsea, la causa util es ESA —con su texto— y no «el productor dice N y el cuerpo M», que es
# su consecuencia. Un diagnostico que nombra la consecuencia manda a mirar el sitio equivocado.
# ⛔ EL PRODUCTOR PUBLICA SU CARDINAL Y HAY QUE LEERLO — A-01 del contraste `sol max`, y era un
# hueco REAL y de los caros. `--print` escribe el numero en su propia cabecera
# («--- SOLO-GANCHO (N): … ---»), y este consumidor lo IGNORABA: contaba el cuerpo y despues
# comparaba ese conteo con el numero de patas medidas. **Las dos cifras nacian del MISMO cuerpo**,
# asi que coincidian siempre y no probaban nada. Mutante que lo aisla: cabecera «4» con tres patas
# validas ⇒ el guion salia 0 diciendo «derive 3, midi 3», sin ver que faltaba una.
#
# La cadena que ata el veredicto es de TRES eslabones, no de dos:
#     cardinal del PRODUCTOR  ==  cuerpo PARSEADO  ==  patas MEDIDAS
# Si cualquiera de los tres difiere, es 2 y se dice cual. Una cifra que se compara consigo misma
# no es un control: es un espejo.
CARDINAL="$(printf '%s\n' "$BLOQUE" | sed -n '1s/^--- SOLO-GANCHO (\([0-9][0-9]*\)).*/\1/p')"
if [ -z "$CARDINAL" ]; then
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: la cabecera de SOLO-GANCHO no publica su cardinal." >&2
	echo "                          Sin el numero del PRODUCTOR, lo unico que puedo comparar es el" >&2
	echo "                          cuerpo consigo mismo, y eso no distingue una lista completa de" >&2
	echo "                          una recortada." >&2
	exit 2
fi
if [ "$CARDINAL" -ne "${N_LISTA:-0}" ]; then
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: el productor dice $CARDINAL pata(s) y el cuerpo trae $N_LISTA." >&2
	echo "                          No mido el subconjunto: publicar la parte como si fuera el todo" >&2
	echo "                          es exactamente lo que este centinela existe para no hacer." >&2
	exit 2
fi

# CONTROL POSITIVO. Una lista vacía haría que «ninguna pata roja» fuese cierto y vacío a la vez —
# el mismo cero que ya nos engañó dos veces hoy («64 de 64», «0 de 0»). Un centinela que no puede
# enumerar su sujeto no está limpio: no ha mirado.
MINIMO="${OLIVARES_WATCHDOG_MIN_LEGS:-20}"
if [ "${N_LISTA:-0}" -lt "$MINIMO" ]; then
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: la lista derivada trae $N_LISTA pata(s) (mínimo $MINIMO)." >&2
	echo "                          Eso no es «no hay patas sólo-gancho»: es que la derivación no" >&2
	echo "                          casó. Mira el formato de '$PARIDAD --print' antes de creerte" >&2
	echo "                          un verde de este guion." >&2
	exit 2
fi

# --- 3 · ACOTAR sin ELEGIR --------------------------------------------------------------------
# `--only` no es una lista alternativa: es un FILTRO sobre la derivada, y rehúsa lo que la
# derivación no contiene. Si se aceptara un nombre de fuera, el guion volvería a ser «una lista
# elegida», que es justo lo que no debe ser.
SUJETOS="$LISTA"
# ⛔ AUSENTE y VACÍO no son lo mismo, y confundirlos daba un CERO. `SOLO=""` significaba las dos
# cosas: «no se paso --only» y «se paso --only con cadena vacia». Con `--only ''` el guion caia en
# la rama de «no se paso» y **medía las 149 como si nada**, devolviendo 0 sobre una peticion que
# no seleccionaba nada. Lo aislo el contraste `sol max` (A-02, v2). Ahora la BANDERA dice si se
# paso, y el VALOR dice que se pidio.
if [ "$SOLO_DADO" = "1" ] && [ -z "$SOLO" ]; then
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: --only con valor vacio." >&2
	echo "                          «Ninguna pata seleccionada» no es «ninguna pata roja»." >&2
	exit 2
fi
if [ "$SOLO_DADO" = "1" ]; then
	SUJETOS=""
	while IFS= read -r p; do
		[ -n "$p" ] || continue
		# ⛔ `-F` Y `--`, NO UN PATRON. Con `grep -qx "$p"` el argumento se interpretaba como
		# EXPRESION REGULAR: el contraste `sol max` (A-02) paso `--only 'pata0.'`, el punto caso
		# `pata01`, la comprobacion dijo «esta en la lista derivada» y el guion ejecuto la tarea
		# LITERAL `lint:pata0.` — que no existe en la derivacion. Es decir, `--only` volvia a ser
		# una lista ELEGIDA por la puerta de atras, que es justo lo que este guion no puede ser.
		# Sin tuberia: `grep -q` sale al primer match y el productor puede recibir SIGPIPE (141 en exito).
		case $'\n'"$LISTA"$'\n' in *$'\n'"$p"$'\n'*) _hay=1 ;; *) _hay=0 ;; esac
		if [ "$_hay" = 1 ]; then
			SUJETOS="${SUJETOS}${p}"$'\n'
		else
			echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: '$p' no está en la lista DERIVADA." >&2
			echo "                          --only acota la lista derivada; no la sustituye." >&2
			exit 2
		fi
	done <<EOF
$(printf '%s\n' "$SOLO" | tr ',' '\n')
EOF
	SUJETOS="$(printf '%s' "$SUJETOS")"
	# Una seleccion VACIA (`--only ','`, o comas sueltas) dejaba cero sujetos, y con cero sujetos
	# la comprobacion de cobertura de abajo se desactiva por su propia guarda: el guion salia 0
	# habiendo medido NADA. Otro cero que se lee como verde (A-02).
	if [ -z "$(printf '%s\n' "$SUJETOS" | grep -c . || true)" ] || [ "$(printf '%s\n' "$SUJETOS" | grep -c . || true)" -eq 0 ]; then
		echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: --only no selecciono ninguna pata." >&2
		echo "                          Cero sujetos no es «ninguna roja»: es no haber medido." >&2
		exit 2
	fi
fi
if [ "${LIMITE:-0}" -gt 0 ]; then
	SUJETOS="$(printf '%s\n' "$SUJETOS" | head -n "$LIMITE")"
fi
N_SUJETOS="$(printf '%s\n' "$SUJETOS" | grep -c . || true)"

# ⛔ LA PROCEDENCIA DE LA LISTA SE PUBLICA, y no es adorno: es uno de los limites que yo mismo
# declare en la cabecera de `check-ci-history-depth.sh` («se pierde la PROCEDENCIA del
# repositorio»), y al repasar este guion contra esa lista resulta que lo comparte.
# `OLIVARES_PARITY_CMD` puede apuntar a un guion de FUERA del arbol medido —mi propia bateria lo
# hace, y con razon: asi es hermetica—, y entonces la lista describe un arbol y el `rc` por pata
# describe otro. No lo prohibo, porque prohibirlo rompe el banco de pruebas y porque hay usos
# legitimos; lo que no puede pasar es que el veredicto NO LO DIGA. Un veredicto publica su sujeto:
# el SHA **y** de donde salio su lista.
PARIDAD_ABS="$(cd "$(dirname "$PARIDAD")" 2>/dev/null && pwd -P)/$(basename "$PARIDAD")"
case "$PARIDAD_ABS" in
"$RAIZ"/*) PROCEDENCIA="del arbol medido" ;;
*) PROCEDENCIA="⚠ FUERA del arbol medido" ;;
esac
echo "watchdog-hook-only-legs: SHA $SHA · lista derivada $N_LISTA pata(s) · a medir $N_SUJETOS"
echo "watchdog-hook-only-legs: lista de '$PARIDAD_ABS' ($PROCEDENCIA)"
if [ "$DRY" = "1" ]; then
	printf '%s\n' "$SUJETOS" | sed 's/^/  /'
	# ⛔ `--dry` NO ES LIMPIO: es «no he medido, a proposito». Salia 0, indistinguible de una
	# corrida en la que las 149 patas salieron verdes (A-02). El rc y el rotulo lo separan ahora,
	# porque quien lea esto en un log o en un asiento no ve con que banderas se lanzo.
	echo "watchdog-hook-only-legs: DRY — enumerado y NO medido. Esto no es un verde."
	exit 2
fi

# --- 4 · MEDIR, Y PUBLICAR EL rc POR PATA -----------------------------------------------------
# Un veredicto agregado obliga a reproducir las 149 para saber cuál. El rc va por pata, y con su
# duración, porque una pata que tarda de más es un hallazgo distinto de una que sale roja.
#
# ⛔ SE LLAMA CON `task -x`, no con `task`. Medido el 2026-08-29: `task` aplasta TODO código de
#    fallo a 201, así que el rc de `task` distingue «falló» de «no falló» y nada más — no conserva
#    la tercera respuesta. Con `-x` el guion de dentro entrega su código, y `2` vuelve a poder
#    significar «no he podido mirar», que es la mitad del contrato de este centinela.
# ⛔ EL PRODUCTOR PUBLICA ETIQUETAS DEL GANCHO, NO SUFIJOS DE `lint:`. Bloqueo real encontrado por
# el contraste `sol max` sobre la salida REAL de `--print`: de 144 etiquetas, **141** son
# `lint:<etiqueta>` y **TRES son tareas EXACTAS** — `vet`, `test:cli-walk` y
# `test:publish-enterprise-artifacts` (`.githooks/pre-push:412,:431,:651,:1351,:1359`;
# `Taskfile.yml:803,:1128,:5253`). Anteponiendo `lint:` siempre, este guion invocaba la INEXISTENTE
# `lint:vet`, recibia rc 200 y publicaba **ROJA** — un rojo falso sobre una pata **que nunca midio**.
# Mi banco no podia verlo porque solo fabricaba `lint:pataNN`.
#
# Se resuelve contra la UNICA autoridad de «que tareas existen», que es go-task: la etiqueta L es
# `lint:L` si esa tarea existe, o `L` si existe. Si no existe ninguna, o existen LAS DOS, **rehusa**:
# adivinar cual de dos es la buena seria elegir, y elegir es lo que este guion no hace.
declare -A TAREA_DE
if ! LISTADO="$(task --list-all 2>/dev/null | sed -n 's/^\* \([a-z0-9:._-]*\):.*/\1/p' | sort -u)"; then
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: \`task --list-all\` fallo." >&2
	exit 2
fi
N_TAREAS="$(printf '%s\n' "$LISTADO" | grep -c . || true)"
MIN_TAREAS="${OLIVARES_WATCHDOG_MIN_TASKS:-50}"
if [ "${N_TAREAS:-0}" -lt "$MIN_TAREAS" ]; then
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: \`task --list-all\` devolvio $N_TAREAS tarea(s) (minimo $MIN_TAREAS)." >&2
	echo "                          Con un listado vacio o recortado, «esa tarea no existe» seria" >&2
	echo "                          cierto para todas." >&2
	exit 2
fi
AMBIGUAS=""; SIN_TAREA=""
while IFS= read -r p; do
	[ -n "$p" ] || continue
	hay_lint=0; hay_exacta=0
	case $'\n'"$LISTADO"$'\n' in *$'\n'"lint:$p"$'\n'*) hay_lint=1 ;; esac
	case $'\n'"$LISTADO"$'\n' in *$'\n'"$p"$'\n'*) hay_exacta=1 ;; esac
	if [ "$hay_lint" = 1 ] && [ "$hay_exacta" = 1 ]; then AMBIGUAS="${AMBIGUAS}  $p → lint:$p y $p"$'\n'
	elif [ "$hay_lint" = 1 ]; then TAREA_DE["$p"]="lint:$p"
	elif [ "$hay_exacta" = 1 ]; then TAREA_DE["$p"]="$p"
	else SIN_TAREA="${SIN_TAREA}  $p"$'\n'; fi
done <<EOF_R2
$SUJETOS
EOF_R2
if [ -n "$SIN_TAREA" ]; then
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: etiqueta(s) sin tarea ni como \`lint:X\` ni como \`X\`:" >&2
	printf '%s' "$SIN_TAREA" >&2
	echo "                          Inventar el nombre de la tarea es elegir, y este guion consume." >&2
	exit 2
fi
if [ -n "$AMBIGUAS" ]; then
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: etiqueta(s) que resuelven a DOS tareas:" >&2
	printf '%s' "$AMBIGUAS" >&2
	exit 2
fi

ROJAS=0
CIEGAS=0
VERDES=0
RESUMEN=""
while IFS= read -r p; do
	[ -n "$p" ] || continue
	ini="$(date +%s)"
	salida="$(task -x "${TAREA_DE[$p]}" 2>&1)"
	rc=$?
	fin="$(date +%s)"
	dur=$((fin - ini))
	# ⛔ EL 200 DE go-task ES «NO HE PODIDO MIRAR», NO UN HALLAZGO. Lo aisló un contraste sobre la v3 con
	# una sonda propia, y viola una propiedad que yo mismo había escrito: «lo que no se ha medido
	# es 2». `task` devuelve **200** cuando la TAREA NO EXISTE — y eso puede pasar aunque la
	# resolución de identidad haya dicho que sí, porque entre `task --list-all` y la ejecución hay
	# una CARRERA: otro carril reescribe el `Taskfile`, un merge aterriza, alguien renombra. El
	# fallo entonces **no es del árbol que estoy midiendo**, es de que la tarea dejó de existir
	# mientras la medía.
	#
	# Publicarlo como ROJA es un hallazgo falso sobre una pata que nunca se ejecutó, y además con
	# la polaridad al revés: el operador va a mirar el código de la pata cuando lo que cambió fue
	# el Taskfile. Con 2 el mensaje dice lo que pasó y el rc global lo separa del rojo real.
	case "$rc" in
	0) VERDES=$((VERDES + 1)); estado="limpio" ;;
	2) CIEGAS=$((CIEGAS + 1)); estado="NO HE PODIDO MIRAR" ;;
	200)
		# ⛔ 200 ES AMBIGUO Y SE DESAMBIGUA, NO SE ELIGE. Medido: `task -x` devuelve **200** tanto
		# cuando la TAREA NO EXISTE como cuando el guion de la pata sale 200 por su cuenta. Mapear
		# 200 a «ciega» a secas convertiria un rojo real en un «no pude mirar»; mapearlo a «roja»
		# publica un hallazgo falso sobre una pata que nunca corrio. Las dos son mentiras, en
		# direcciones opuestas.
		#
		# La pregunta que las separa es barata: **¿sigue existiendo la tarea?** Si desaparecio
		# entre el listado y la ejecucion, es la carrera —otro carril reescribio el `Taskfile`, un
		# merge aterrizo, alguien renombro— y no es del arbol que mido: CIEGA. Si sigue ahi, la
		# pata salio 200 de verdad: ROJA.
		_ahora="$(task --list-all 2>/dev/null | sed -n 's/^\* \([a-z0-9:._-]*\):.*/\1/p')"
		case $'\n'"$_ahora"$'\n' in *$'\n'"${TAREA_DE[$p]}"$'\n'*) _sigue=1 ;; *) _sigue=0 ;; esac
		if [ "$_sigue" = 1 ]; then
			ROJAS=$((ROJAS + 1)); estado="ROJA (la pata salio 200 y su tarea sigue existiendo)"
		else
			CIEGAS=$((CIEGAS + 1)); estado="NO HE PODIDO MIRAR (la tarea no existe ya: carrera lista→ejecucion)"
		fi
		;;
	*) ROJAS=$((ROJAS + 1)); estado="ROJA" ;;
	esac
	printf 'watchdog-hook-only-legs:   %-42s rc=%-3s %5ss  %s\n' "${TAREA_DE[$p]}" "$rc" "$dur" "$estado"
	if [ "$rc" -ne 0 ]; then
		printf '%s\n' "$salida" | tail -4 | sed 's/^/                              /'
	fi
	RESUMEN="${RESUMEN}${TAREA_DE[$p]} rc=${rc} ${dur}s"$'\n'
done <<EOF
$SUJETOS
EOF

# ⛔ SE REVALIDA EL SUJETO AL CERRAR, no solo al abrir. Medir 149 patas tarda decenas de minutos, y
# el contraste `sol max` lo nombro: el SHA del principio puede no ser el arbol del final — alguien
# cambia de rama, un gate reescribe un fichero, otro carril toca el worktree. Un veredicto que
# nombra un SHA que ya no es el que midio es peor que ninguno, porque es COMPROBABLE y falso.
SHA_FIN="$(git rev-parse HEAD 2>/dev/null || echo "")"
if [ "$SHA_FIN" != "$SHA" ]; then
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: el arbol cambio mientras media ($SHA -> ${SHA_FIN:-<ilegible>})." >&2
	echo "                          Las patas se midieron sobre dos arboles distintos, asi que el" >&2
	echo "                          veredicto no tiene un sujeto que nadie pueda comprobar." >&2
	exit 2
fi
if ! ESTADO_FIN="$(git status --porcelain 2>/dev/null)"; then
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: no puedo releer el estado del arbol al cerrar." >&2
	exit 2
fi
SUCIO_FIN="$(printf '%s\n' "$ESTADO_FIN" | grep -c . || true)"
if [ "${SUCIO_FIN:-1}" -ne 0 ]; then
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: el arbol quedo sucio al terminar ($SUCIO_FIN cambio(s))." >&2
	echo "                          Alguna pata escribio en el arbol que estaba midiendo, asi que" >&2
	echo "                          las posteriores no midieron lo mismo que las primeras." >&2
	exit 2
fi

echo "watchdog-hook-only-legs: SHA $SHA — $VERDES limpia(s) · $ROJAS roja(s) · $CIEGAS sin poder mirar (de $N_SUJETOS)"
if [ "$N_SUJETOS" -gt 0 ] && [ "$((VERDES + ROJAS + CIEGAS))" -ne "$N_SUJETOS" ]; then
	echo "watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: medí $((VERDES + ROJAS + CIEGAS)) de $N_SUJETOS." >&2
	exit 2
fi
[ "$CIEGAS" -eq 0 ] || exit 2
[ "$ROJAS" -eq 0 ] || exit 1
exit 0
