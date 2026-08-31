#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# r8-preflight.sh — deja el arnés de captura APUNTANDO a un SHA, y NO captura.
#
# ⛔ QUÉ HACE Y QUÉ NO. Prepara: worktree en el SHA del corte, precondiciones de caja comprobadas,
#    binario del motor presente, Playwright instalado. **No fotografía nada.** La mañana del 30
#    tiene que ser pulsar, y descubrir que falta el binario a las 09:00 cuesta la mañana entera.
#
# ⛔ POR QUÉ UN WORKTREE Y NO UN FLAG. `docs-captures.sh` deriva su `ROOT` de su propia ubicación
#    (`ROOT="$(cd "$(dirname "$0")/.." && pwd)"`), así que **no hay parámetro de ref**: «apuntar el
#    arnés a un SHA» ES correrlo desde un árbol en ese SHA. Medido leyendo el guion, no supuesto.
#
# ⛔ Y `docs-captures.sh` NO CONSTRUYE EL BINARIO. Usa `$ROOT/bin/olivares` (:55) y arranca el motor
#    con él (:69), pero en ninguna línea lo compila: sólo construye la web (`pnpm run build`, :65).
#    Un árbol recién creado NO tiene `bin/olivares`, así que sin este pre-vuelo la primera señal
#    sería el motor sin arrancar, ya con el reloj corriendo.
#
# Uso:  bash scripts/r8-preflight.sh <sha-o-ref>
#       R8_DIR=/ruta/al/worktree   (por omisión ../r8-captures, junto al repo)
#
# rc=0 LISTO · rc=1 falta algo (lo dice y dice cuál) · rc=2 no he podido mirar
set -u -o pipefail

# ⛔ AISLAMIENTO DE ENTORNO GIT, y no es formalismo: este guion opera con `git -C <dir>`, pero un
#    `GIT_DIR` heredado **manda sobre `-C`** — y git lo exporta a todo hook `pre-push`, o sea desde
#    cualquier carril en paralelo. Sin sanear, un pre-vuelo lanzado desde un hook leería el árbol de
#    OTRO repositorio y diría que el worktree del corte está listo mirando otra cosa.
#    Fail-closed: un saneador que no se puede cargar es «no he podido aislar», nunca «no hacía falta».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "r8-preflight: FATAL: no puedo cargar $_olivares_git_env (aislamiento git-env)" >&2
	exit 2
}
unset _olivares_git_env

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# ⛔ RUTA NEUTRA Y DERIVADA, no una absoluta de esta caja. La primera versión ponía
#    `/workspace/<repo privado>-R8` por omisión y **`lint:export` la marcó como fuga**: el
#    árbol público no puede llevar el nombre del repositorio interno ni la disposición de
#    nuestros directorios. Se deriva de `ROOT`, que además la hace portátil.
DIR="${R8_DIR:-$ROOT/../r8-captures}"
# Pico medido de un `vite build` en esta caja: ~16 GiB. Se pide margen por encima.
MIN_DISCO_G="${R8_MIN_DISCO_G:-25}"

fallos=0
ok() { printf '  \033[32mOK  \033[0m %-34s %s\n' "$1" "${2:-}"; }
malo() {
	fallos=$((fallos + 1))
	printf '  \033[31mFALTA\033[0m %-34s %s\n' "$1" "${2:-}"
}

# ⛔ TRES ESTADOS, NO DOS. `malo` es un HALLAZGO —falta algo que se puede preparar— y `nopuedo` es
#    «no he podido mirar»: el entorno impide la comprobacion, asi que no hay veredicto sobre el
#    sujeto. Confundirlos hace que un TMPDIR no escribible se lea como «el arbol no esta listo»,
#    que manda a preparar lo que ya estaba bien. Lo separo porque es la regla que llevo toda la
#    noche exigiendo a los demas y este guion no la cumplia: rc 1 = falta algo, rc 2 = no he mirado.
sin_mirar=0
nopuedo() {
	sin_mirar=$((sin_mirar + 1))
	printf '  \033[33mNO SE\033[0m %-34s %s\n' "$1" "${2:-}"
}
mirar_no() {
	printf '  \033[33m?   \033[0m %-34s %s\n' "$1" "${2:-}"
	exit 2
}

REF="${1:-}"
if [ -z "$REF" ]; then
	echo "uso: bash scripts/r8-preflight.sh <sha-o-ref>   (R8_DIR=… para el worktree)" >&2
	exit 2
fi

SHA="$(git -C "$ROOT" rev-parse --verify "${REF}^{commit}" 2>/dev/null || true)"
[ -n "$SHA" ] || mirar_no "el corte existe" "\`$REF\` no resuelve a un commit en este clon"

echo "r8-preflight sobre ${SHA:0:9}  (worktree $DIR)"

# ── 1 · TMPDIR EJECUTABLE, y se comprueba EJECUTANDO ───────────────────────────────────────────
# `/tmp` está montado `noexec` en esta caja. Go compila el binario de test bajo TMPDIR y no puede
# ejecutarlo; la firma es `fork/exec …: permission denied` con **0.000s**, o sea que ningún test
# llegó a empezar. Leer `/proc/mounts` NO basta: lo que decide es si un fichero con `+x` corre.
_t="${TMPDIR:-/tmp}"
_p="$_t/.r8-preflight-$$"
if printf '#!/bin/sh\nexit 7\n' >"$_p" 2>/dev/null && chmod +x "$_p" 2>/dev/null; then
	"$_p" >/dev/null 2>&1
	[ "$?" = 7 ] && ok "TMPDIR ejecutable" "$_t" || malo "TMPDIR ejecutable" "$_t es noexec — \`export TMPDIR=/workspace/.tmp-capturas && mkdir -p \$TMPDIR\`"
	rm -f "$_p"
else
	nopuedo "TMPDIR ejecutable" "no he podido escribir en $_t"
fi

# ── 2 · DISCO ──────────────────────────────────────────────────────────────────────────────────
libre_g="$(df -BG --output=avail /workspace 2>/dev/null | tail -1 | tr -dc '0-9')"
if [ -n "$libre_g" ] && [ "$libre_g" -ge "$MIN_DISCO_G" ]; then
	ok "disco" "${libre_g}G libres (mínimo $MIN_DISCO_G)"
else
	malo "disco" "${libre_g:-?}G libres; un vite build pica ~16G"
fi

# ── 3 · UN SOLO `vite build` POR CAJA ─────────────────────────────────────────────────────────
# Dos simultáneos se comen el cgroup de 14 GiB. Se cuentan procesos de NODE, no cualquier línea de
# shell que mencione «vite build»: contar envoltorios da un falso positivo (me pasó).
# ⚠ `pgrep -c` YA IMPRIME 0 cuando no casa, y ADEMÁS sale 1: un `|| echo 0` detrás añade un
#   segundo cero y la variable queda «0\n0», que revienta la comparación aritmética. Medido aquí
#   mismo. Se sanea tomando la primera línea.
n_vite="$(pgrep -fc 'node.*vite.*build' 2>/dev/null | head -1)"
n_vite="${n_vite:-0}"
if [ "$n_vite" -eq 0 ]; then
	ok "sin vite build compitiendo"
else
	malo "vite build compitiendo" "$n_vite vivo(s) — espera"
fi

# ── 3 bis · LA PUERTA DE LA CAJA (#112-bis, adjudicado por r4 el 2026-08-29) ──────────────────
#
# La corrida de capturas es LARGA y pesada: arranca un motor, construye la web y pasea un navegador
# por decenas de rutas con esperas por red. Contra una caja saturada eso no sale «mas lento»: sale
# MAL, porque las esperas vencen y el arnes fotografia pantallas a medio pintar. La spec ya lleva
# cuatro mediciones muertas por timeout documentadas con nombre.
#
# ⛔ EL UMBRAL DE CARGA ES 2x LA CUOTA DE CPU DEL CGROUP, NO 2x LOS NUCLEOS VISIBLES. En esta caja
#    `nproc` dice 16 y `cpu.max` dice 400000/100000 = CUATRO. Usar los visibles daria umbral 32 y
#    dejaria pasar una caja el doble de saturada de lo que el criterio permite. La cuota se lee, no
#    se supone.
#
# Se prefiere la sonda del vigia porque mide ademas swap y throttle, que desde aqui no se ven
# baratos; pero SOLO si esta fresca. Una sonda vieja no es una lectura: es una foto de otro momento.
_puerta="${R8_PUERTA:-/workspace/.olivares-tmptest/vigias-plan-cc3/puerta.estado}"
_edad=99999
[ -f "$_puerta" ] && _edad=$(($(date +%s) - $(stat -c %Y "$_puerta" 2>/dev/null || echo 0)))
if [ -f "$_puerta" ] && [ "$_edad" -le 90 ]; then
	_estado="$(sed -n 's/.*PUERTA=\([A-Z]*\).*/\1/p' "$_puerta" | head -1)"
	_detalle="$(tr -s ' ' <"$_puerta" | head -1 | cut -c1-120)"
	case "$_estado" in
	ABIERTA) ok "puerta de la caja" "ABIERTA (sonda de ${_edad}s)" ;;
	CERRADA) malo "puerta de la caja" "CERRADA — $_detalle · espera a que abra y vuelve a correr esto" ;;
	*) malo "puerta de la caja" "sonda ilegible: no puedo mirar" ;;
	esac
else
	# Sin sonda fresca NO se declara abierta: se mide aqui lo que se puede —la carga contra la
	# cuota— y se dice que la lectura es parcial. «No pude mirar» y «esta limpio» no son lo mismo.
	_cpumax="$(cat /sys/fs/cgroup/cpu.max 2>/dev/null || echo 'max 100000')"
	_q="${_cpumax%% *}"
	_per="${_cpumax##* }"
	if [ "$_q" = "max" ] || [ -z "${_per:-}" ] || [ "${_per:-0}" -eq 0 ] 2>/dev/null; then
		_cuota="$(nproc 2>/dev/null || echo 1)"
	else
		_cuota=$((_q / _per))
		[ "$_cuota" -lt 1 ] && _cuota=1
	fi
	_umbral=$((_cuota * 2))
	_l1="$(cut -d' ' -f1 /proc/loadavg 2>/dev/null || echo 0)"
	_l1e="${_l1%%.*}"
	if [ "${_l1e:-0}" -lt "$_umbral" ]; then
		ok "puerta de la caja" "load1 $_l1 < $_umbral (cuota $_cuota) · PARCIAL: sin swap ni throttle"
	else
		# El «PARCIAL» va TAMBIEN aqui: sin la sonda no se ven swap ni throttle, y eso es cierto
		# tanto si la carga pasa como si no. Decirlo solo en el verde haria que el rojo pareciera
		# mas completo de lo que es — y quien lo lea creera que se han mirado las cuatro patas.
		malo "puerta de la caja" "load1 $_l1 >= $_umbral (cuota $_cuota, $(nproc 2>/dev/null) visibles) · PARCIAL: sin swap ni throttle — espera"
	fi
fi

# ── 4 · EL WORKTREE, en el SHA pedido ─────────────────────────────────────────────────────────
if [ -d "$DIR/.git" ] || [ -f "$DIR/.git" ]; then
	actual="$(git -C "$DIR" rev-parse HEAD 2>/dev/null || true)"
	if [ "$actual" = "$SHA" ]; then
		# ⛔ ESTAR EN EL SITIO NO ES ESTAR DESPRENDIDO, y aquí son dos estados distintos.
		#    Un worktree EN UNA RAMA a la altura del corte pasa la comparación de arriba
		#    exactamente igual que uno desprendido. La diferencia sólo se ve después: la corrida
		#    de capturas ENSUCIA el árbol —`docs-captures.sh` construye la web, y `at:gate` deja
		#    `dist/` y el sello desalineados—, así que si ese árbol tiene rama, su siguiente push
		#    muere en `lint:web-bundle-freshness` SIN RELACIÓN APARENTE con lo que se estaba
		#    haciendo. Medido el 2026-08-29: una sesión perdió un push exactamente así, y quien
		#    lo diagnosticó lo avisó al resto el mismo día.
		#
		#    El remedio propuesto entonces fue «si tocas el worktree de una rama, reconstruye y resella antes
		#    de empujar». Esto es más fuerte y más barato: **un worktree desprendido NO PUEDE
		#    tener un push pendiente**, así que la seguridad deja de depender de que alguien se
		#    acuerde de resellar. El pre-vuelo ya prescribía `--detach` en su remedio; lo que le
		#    faltaba era COMPROBARLO.
		if rama="$(git -C "$DIR" symbolic-ref -q --short HEAD)"; then
			malo "worktree desprendido" "está en la rama \`$rama\`; la captura ensucia el bundle y matará su push: \`git -C $DIR checkout --detach\`"
		else
			ok "worktree en el corte" "${SHA:0:9} · desprendido"
		fi
	else
		malo "worktree en el corte" "está en ${actual:0:9}; \`git -C $DIR checkout --detach $SHA\`"
	fi
else
	malo "worktree" "no existe; \`git -C $ROOT worktree add --detach $DIR $SHA\`"
fi

# ── 5 · EL BINARIO DEL MOTOR, que docs-captures.sh NO construye ───────────────────────────────
if [ -x "$DIR/bin/olivares" ]; then
	ok "bin/olivares" "$(du -h "$DIR/bin/olivares" 2>/dev/null | cut -f1)"
else
	malo "bin/olivares" "falta; \`cd $DIR && task build\` (minutos, hazlo la noche antes; deja los trece conectores en bins/ y la comprobacion 7 te dira como limpiarlos)"
fi

# ── 6 · PLAYWRIGHT ────────────────────────────────────────────────────────────────────────────
if [ -d "$DIR/web/node_modules" ]; then
	ok "node_modules de web" ""
else
	malo "node_modules de web" "\`pnpm --dir $DIR/web install\`"
fi
# ⚠ `$HOME` CON GUARDA, y no es formalismo: bajo `set -u` una variable sin definir MATA el guion
#   en esa línea, y en los runners de CI `HOME` falta en seis de nueve (medido). El rojo saldría con
#   el nombre del PASO, no con el de la variable — que es como un `$HOME` sin guarda tumbó
#   `control-plane` bajo el rótulo «license boundary» con la licencia impecable. Y aquí NO se
#   inventa un valor por defecto: si no hay `HOME` no se sabe dónde mirar, así que se DICE.
_cache="${PLAYWRIGHT_BROWSERS_PATH:-${HOME:-}/.cache/ms-playwright}"
if [ -z "${HOME:-}" ] && [ -z "${PLAYWRIGHT_BROWSERS_PATH:-}" ]; then
	malo "chromium de Playwright" "NO PUEDO MIRAR: sin HOME ni PLAYWRIGHT_BROWSERS_PATH no sé dónde buscar; exporta uno de los dos y repite"
elif ls "$_cache"/chromium-* >/dev/null 2>&1; then
	ok "chromium de Playwright" ""
else
	malo "chromium de Playwright" "\`pnpm --dir $DIR/web exec playwright install chromium\`"
fi

# ── 7 · ⛔ LOS RESIDUOS DE `task build`, QUE MATAN EL SIGUIENTE PUSH DESDE ESTE ÁRBOL ──────────
#
# Esta comprobación existe por una cadena que sólo se ve entera desde aquí:
#   · `docs-captures.sh` NO construye `bin/olivares` (comprobación 5) ⇒ hay que correr `task build`.
#   · `task build` compila los TRECE conectores de primera parte en `cmd/olivares/firstparty/bins/`.
#   · `check-c03-41-plugin-census.sh:75` exige `os.listdir(bins) == ["PLACEHOLDER"]` — mide el
#     DISCO, no el commit, así que ficheros SIN TRACKEAR lo rompen igual.
#   · ⇒ quien prepare el árbol de capturas se queda con trece residuos, y el siguiente push desde
#     ese worktree muere en `lint:addon-sets` **sin relación aparente con lo que estaba haciendo**.
#
# Lo reportó otro carril el 2026-08-29 tras perder un push por esto, y me alcanzó con el mío ya
# en vuelo: el mismo árbol donde construí el binario para verificar su documento los tenía.
#
# ⚠ La atribución va sin el nombre del carril A PROPÓSITO: `export-public.sh:820` cuenta
#   los nombres de carril del hub como vocabulario interno, y su línea base es CERO.
#
#   ⚠ Y esta nota NO deletrea esos nombres, porque la primera versión SÍ los citaba «para
#     documentar el patrón» y **seguía contando 1**: escribir el token prohibido dentro de
#     la explicación de por qué está prohibido lo dispara igual. El gate lee bytes, no
#     intenciones. El patrón vive en `export-public.sh:820`; aquí sólo el porqué.
#   El hecho —que lo encontró otro y no yo— vale igual; el identificador no viaja al árbol
#   público. Quién fue está en el registro de sesión, que no se exporta.
#
# ⚠ `git status` A SECAS PUEDE NO ENSEÑARLOS. El censo mira el disco; si el ignore los cubre, sólo
#   los ves con `--ignored`. Por eso aquí se cuenta el DIRECTORIO, que es lo que el censo cuenta.
_bins="$DIR/cmd/olivares/firstparty/bins"
if [ ! -d "$_bins" ]; then
	ok "residuos de build" "no hay bins/ todavía"
else
	_n="$(ls -A "$_bins" 2>/dev/null | grep -cvx 'PLACEHOLDER' || true)"
	_n="${_n:-0}"
	if [ "$_n" -eq 0 ]; then
		ok "sin residuos de build" "bins/ sólo con PLACEHOLDER"
	else
		malo "residuos de build" "$_n fichero(s) en bins/; \`find $_bins -mindepth 1 ! -name PLACEHOLDER -delete\` antes de empujar"
	fi
fi

# ── 8 · LO QUE SE VA A CAPTURAR, para que nadie descubra el alcance el día D ───────────────────
if [ -f "$DIR/web/e2e/docs-captures.spec.ts" ]; then
	_spec="$DIR/web/e2e/docs-captures.spec.ts"
	# ⛔ LA SONDA VA CON LA CIFRA, o dos personas miden el MISMO arbol y no coinciden.
	#    Esta linea contaba con `^    id: '` —cuatro espacios exactos— y se deja SEIS entradas
	#    fuera: las que viven a otra sangria (escenas de video anidadas y un `setup` de nivel
	#    superior). Sobre `main` daba 39 donde la sonda de sangria libre da 45, y sobre el arbol
	#    del candidato daria 45 donde el manifiesto declara 51. **Las dos cifras son correctas y
	#    de predicados distintos**, que es justo la clase de desacuerdo que este repositorio ya
	#    documenta para el conteo de gates del hook. Como el manifiesto del candidato 2 declara
	#    51, la sonda que manda aqui es la de sangria libre, y se imprime CON SU NOMBRE.
	# ⛔⛔ Y LA SONDA DE SANGRIA TAMBIEN ERA CORTA — corregido el 2026-08-29 con las dos cuentas
	#    delante. `^[[:space:]]+id:` exige que `id:` empiece la linea, asi que sólo ve las entradas
	#    escritas como objeto MULTILINEA y se deja fuera las de una sola linea, del estilo
	#    `{ id: 'killswitch', path: '/killswitch', heading: /^Kill switch$/ },`. Sobre el arbol del
	#    candidato daba 51 donde los ids DISTINTOS son 71: veinte entradas invisibles.
	#
	#    Y la cuenta mala no se quedaba en la cifra: al cruzar «lo publicado» con «lo que la spec
	#    produce» acusaba a 21 superficies publicadas de no tener ya productor —entre ellas
	#    `killswitch`, `catalog` y `alerting`, recien adjudicadas—, y con la sonda buena la lista
	#    real es UNA: `accept-invite`, que es justo la retirada a proposito. 62 publicadas − 1 + 10
	#    nuevas = 71, y cuadra por los dos lados.
	#
	#    Se cuentan ids DISTINTOS y se excluyen las lineas de comentario, porque este fichero
	#    documenta sus propias vistas en prosa y un `id: '...'` dentro de un comentario se contaria.
	n_vistas="$(grep -vE '^[[:space:]]*(//|\*|/\*)' "$_spec" 2>/dev/null | grep -oE "id: '[^']+'" | sort -u | wc -l | tr -d ' ')"
	ok "spec de capturas" "presente ($n_vistas ids distintos · sonda: id:'…' fuera de comentarios)"

	# ⛔ CONTAR NO ES EXIGIR, y aquí la diferencia se paga una sola vez al año.
	#    La linea de arriba IMPRIME cuantas vistas hay y no pide ninguna, asi que un arbol al que
	#    le falten las tomas de estado interno pasa este pre-vuelo en VERDE y la corrida sale
	#    "completa": capturar 45 de 45 no es un fallo para nadie — el arnes no sabe que le faltan
	#    seis. Y es un fallo IRREVERSIBLE: las capturas se toman una vez, el dia del corte.
	#
	#    Medido el 2026-08-29: `origin/main` llevaba 45 entradas y la rama de R8-V3 51. El hook
	#    `despues` SI habia aterrizado en main y sus SEIS USUARIOS no — un arnes con el gancho
	#    puesto y nada colgando, que es la forma mas silenciosa de este fallo.
	#
	#    Se nombran una a una, no por numero: un umbral (">= 51") pasaria con seis vistas
	#    cualesquiera, y lo que importa no es cuantas hay sino QUE ESTEN ESTAS, que son las que
	#    prueban estado que no se ve en una pantalla vacia (paginacion, rechazo de intent,
	#    solo-lectura por scope, error de runs, lista truncada).
	_faltan=""
	while IFS= read -r _v; do
		[ -n "$_v" ] || continue
		grep -q "id: '$_v'" "$_spec" 2>/dev/null || _faltan="$_faltan $_v"
	done <<VISTAS_DE_ESTADO
work-decisions
work-decisions-paginated
work-apply-refused
templates-readonly
workflow-runs-error
list-truncated
VISTAS_DE_ESTADO
	if [ -z "$_faltan" ]; then
		ok "tomas de estado interno" "las 6 en la spec"
	else
		malo "tomas de estado interno" "faltan:$_faltan — el arbol NO lleva #2134; capturar asi pierde esas tomas EN SILENCIO. Desprende en un SHA que la incluya: \`git -C $DIR checkout --detach <sha-con-2134>\`"
	fi
else
	malo "spec de capturas" "no está en $DIR/web/e2e/; el worktree no es de este repo o esta a medio crear: \`git -C $ROOT worktree add --detach $DIR $SHA\`"
fi

echo
if [ "$fallos" -eq 0 ]; then
	echo "r8-preflight: LISTO — el arnés apunta a ${SHA:0:9}."
	echo "  para capturar (esto NO lo hace este guion):"
	echo "    cd $DIR && TMPDIR=<uno ejecutable> bash scripts/docs-captures.sh"
	if [ "$sin_mirar" -gt 0 ]; then
		echo "r8-preflight: sin fallos, pero $sin_mirar comprobacion(es) NO HECHAS: rc 2." >&2
		exit 2
	fi
	exit 0
fi
if [ "$sin_mirar" -gt 0 ]; then
	echo "r8-preflight: $sin_mirar comprobacion(es) que NO HE PODIDO HACER (y $fallos sin cumplir)." >&2
	echo "  «No he podido mirar» no es «esta limpio»: arregla el entorno y vuelve a correr esto." >&2
	exit 2
fi
echo "r8-preflight: $fallos precondición(es) sin cumplir. NO lances la captura todavía." >&2
exit 1
