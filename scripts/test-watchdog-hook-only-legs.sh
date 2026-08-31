#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# test-watchdog-hook-only-legs.sh — a repository gate: batería del centinela.
#
# ⛔ LOS TRES MUTANTES QUE r4 EXIGE, y cada uno mueve UNA SOLA dimensión — si un mutante moviera
#    dos, un verde no diría cuál de las dos lo sostiene:
#      lista congelada  -> rojo    (el centinela DERIVA en cada corrida, no cachea)
#      silencio         -> rojo    (una lista vacía no es «ninguna pata roja»)
#      SHA no publicado -> rojo    (un veredicto sin sujeto comprobable no vale)
#
# ⛔ Y LA DIRECCIÓN DE NO-DISPARO, que es la casilla sin la cual las otras no valen nada: un
#    centinela que REHUSARA SIEMPRE pasaría las tres casillas de rechazo con nota. Por eso el caso
#    (1) exige un CERO sobre un fixture sano, y el (2) exige que un rojo real salga 1 y NO 2.
#
# Hermético: repositorio de juguete con su `origin` bare, un `--print` de mentira y un Taskfile de
# mentira. No toca el repositorio real, no usa red y no depende de que exista
# `scripts/check-gate-parity.sh`. (El comentario original decia que ese guion vivia «en una rama
# y no en main»; el 2026-08-30 SI esta en `main`. Corregido aqui en vez de viajar caducado.)
set -uo pipefail

# ⛔ AISLAMIENTO DE ENTORNO GIT (trinquete `lint:git-env`). Este guion empareja `mktemp -d` con
# git, y git EXPORTA `GIT_DIR` a los hooks desde un worktree enlazado. `GIT_DIR` manda sobre
# `-C`, asi que sin sanear, un `git -C "$tmp" ...` de banco de pruebas actua sobre el
# repositorio VIVO.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env
LC_ALL=C
export LC_ALL

RAIZ="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)" || exit 2
SUT="$RAIZ/scripts/watchdog-hook-only-legs.sh"
[ -r "$SUT" ] || { echo "test-watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: no encuentro $SUT" >&2; exit 2; }
command -v task >/dev/null 2>&1 || { echo "test-watchdog-hook-only-legs: ⛔ NO HE PODIDO MIRAR: sin \`task\`" >&2; exit 2; }

PASS=0; FAIL=0
check() { # <nombre> <rc esperado> <rc real>
	if [ "$2" = "$3" ]; then PASS=$((PASS+1)); printf 'ok   %-58s rc=%s\n' "$1" "$3"
	else FAIL=$((FAIL+1)); printf 'FAIL %-58s esperaba rc=%s, dio rc=%s\n' "$1" "$2" "$3"; fi
}

TMP="$(mktemp -d "${TMPDIR:-/tmp}/wdhol.XXXXXX")" || exit 2
trap 'rm -rf "$TMP"' EXIT

# --- fixture -----------------------------------------------------------------------------------
# <dir> con: repo git + origin bare + Taskfile con N patas + un `--print` de mentira.
# Los parámetros son las DIMENSIONES que los mutantes mueven, una cada uno.
fixture() { # <nombre> <n_patas> <rc_de_la_primera> [publicado=si|no] [limpio=si|no]
	local nom="$1" n="$2" rc1="$3" pub="${4:-si}" limpio="${5:-si}"
	local d="$TMP/$nom"
	rm -rf "$d"; mkdir -p "$d/scripts"
	git -c init.defaultBranch=main init -q "$d"
	git -C "$d" config user.email t@t.t; git -C "$d" config user.name t
	# Taskfile con n patas: la primera con el rc que pida el caso, el resto limpias.
	# ⛔ EL FIXTURE FABRICA TAMBIÉN LAS TRES TAREAS **EXACTAS** que el productor real publica sin
	# prefijo `lint:` — `vet`, `test:cli-walk`, `test:publish-enterprise-artifacts`. Antes sólo
	# fabricaba `lint:pataNN`, y por eso NO PODÍA VER el bloqueo que encontró el contraste: el
	# consumidor anteponía `lint:` siempre e invocaba la inexistente `lint:vet`. Un banco que sólo
	# fabrica la forma que su sujeto maneja bien no puede refutar nada.
	{
		printf 'version: "3"\ntasks:\n'
		for i in $(seq 1 "$n"); do
			printf '  lint:pata%02d:\n    cmds:\n      - sh -c "exit %s"\n' "$i" "$( [ "$i" = 1 ] && echo "$rc1" || echo 0 )"
		done
		printf '  vet:\n    cmds:\n      - sh -c "exit 0"\n'
		printf '  test:cli-walk:\n    cmds:\n      - sh -c "exit 0"\n'
		printf '  test:publish-enterprise-artifacts:\n    cmds:\n      - sh -c "exit 0"\n'
	} > "$d/Taskfile.yml"
	# ⛔ EL `--print` DE MENTIRA VIVE **FUERA** DEL REPOSITORIO, y eso es una corrección de esta
	# misma batería: la primera versión lo escribía dentro (`$d/scripts/paridad.sh`) y lo
	# commiteaba. Entonces los mutantes que lo REESCRIBEN o lo BORRAN ensuciaban el árbol, y el
	# centinela rehusaba por «árbol sucio» — es decir, el caso (5) pasaba **por la razón
	# equivocada** y el (5-bis) fallaba por una dimensión que no era la suya. Un mutante que mueve
	# DOS cosas no prueba ninguna. Fuera del repo, cada uno mueve la suya.
	{
		printf '#!/usr/bin/env bash\n'
		printf 'echo "check-gate-parity: gancho=X ci=Y ambas=Z"\n'
		printf 'echo "--- SOLO-CI (0): ---"\n'
		printf 'echo "--- SOLO-GANCHO (%s): mainline-ci no las ve ---"\n' "$n"
		for i in $(seq 1 "$n"); do
			if [ "$i" = 2 ]; then printf 'echo "  pata%02d   (informa, no bloquea)"\n' "$i"
			else printf 'echo "  pata%02d"\n' "$i"; fi
		done
		printf 'echo "--- fin ---"\n'
	} > "$TMP/$nom-paridad.sh"
	chmod +x "$TMP/$nom-paridad.sh"
	git -C "$d" add -A >/dev/null 2>&1; git -C "$d" commit -qm base >/dev/null 2>&1
	git -c init.defaultBranch=main init -q --bare "$d.git"
	git -C "$d" remote add origin "$d.git"
	git -C "$d" push -q origin main >/dev/null 2>&1
	git -C "$d" fetch -q origin main >/dev/null 2>&1
	if [ "$pub" = "no" ]; then   # un commit que NO se empuja: HEAD deja de estar en origin/main
		echo x > "$d/sin-publicar.txt"; git -C "$d" add -A >/dev/null 2>&1
		git -C "$d" commit -qm "sin publicar" >/dev/null 2>&1
	fi
	if [ "$limpio" = "no" ]; then echo suciedad > "$d/sucio.txt"; fi
	printf '%s' "$d"
}

corre() { # <dir> [args...] -> rc
	local d="$1"; shift
	( cd "$d" && OLIVARES_ROOT="$d" OLIVARES_PARITY_CMD="$TMP/$(basename "$d")-paridad.sh" \
		OLIVARES_WATCHDOG_MIN_LEGS=3 OLIVARES_WATCHDOG_MIN_TASKS=3 bash "$SUT" "$@" >"$TMP/out.$$" 2>&1 ); local rc=$?
	cp "$TMP/out.$$" "$TMP/ultima.out" 2>/dev/null; rm -f "$TMP/out.$$"; echo "$rc"
}

# --- 1 · LA DIRECCIÓN DE NO-DISPARO, primero, porque sin ella las demás no valen -------------
d="$(fixture sano 4 0)"
check "(1) fixture sano, cuatro patas limpias -> 0" 0 "$(corre "$d")"

# y que de verdad MIDIÓ: si dijera 0 sin medir nada, este control lo caza
if grep -q 'lista derivada 4 pata(s) · a medir 4' "$TMP/ultima.out" 2>/dev/null; then
	PASS=$((PASS+1)); printf 'ok   %-58s\n' "(1-bis) y publica que derivó 4 y midió 4"
else
	FAIL=$((FAIL+1)); printf 'FAIL %-58s\n' "(1-bis) no publica cuántas derivó/midió"
	sed 's/^/       /' "$TMP/ultima.out" | head -4
fi

# --- 2 · UN ROJO REAL ES 1, NO 2 — la otra mitad del no-disparo ------------------------------
d="$(fixture roja 4 1)"
check "(2) una pata roja -> 1 (hallazgo), no 2" 1 "$(corre "$d")"
grep -q 'lint:pata01' "$TMP/ultima.out" && { PASS=$((PASS+1)); printf 'ok   %-58s\n' "(2-bis) y NOMBRA la pata roja"; } \
	|| { FAIL=$((FAIL+1)); printf 'FAIL %-58s\n' "(2-bis) no nombra la pata roja"; }

# --- 3 · «no he podido mirar» de una pata GANA al rojo ----------------------------------------
d="$(fixture ciega 4 2)"
check "(3) una pata con rc=2 -> 2, no 1 ni 0" 2 "$(corre "$d")"

# --- 4 · MUTANTE «SILENCIO»: la lista sale vacía -> ROJO, nunca 0 ------------------------------
# Mueve UNA dimensión: el número de patas que el `--print` emite. Todo lo demás igual que (1).
d="$(fixture silencio 4 0)"
printf '#!/usr/bin/env bash\necho "--- SOLO-GANCHO (0): ---"\necho "--- fin ---"\n' > "$TMP/silencio-paridad.sh"
chmod +x "$TMP/silencio-paridad.sh"
check "(4) MUTANTE silencio: lista vacía -> 2, jamás 0" 2 "$(corre "$d")"

# --- 5 · MUTANTE «LISTA CONGELADA»: el comando de paridad no existe ---------------------------
# La única entrada legítima es el COMANDO. Si no se puede ejecutar, el centinela no puede caer
# en una lista guardada: tiene que rehusar.
d="$(fixture congelada 4 0)"
rm -f "$TMP/congelada-paridad.sh"
check "(5) MUTANTE lista congelada: sin --print -> 2, no una lista vieja" 2 "$(corre "$d")"

# --- 5-bis · y que DERIVA de verdad: si la salida cambia, el sujeto cambia ---------------------
# Es la otra cara del mutante: no basta con rehusar cuando falta; hay que SEGUIR al comando.
d="$(fixture deriva 6 0)"
rc_a="$(corre "$d")"; a="$(grep -c 'rc=' "$TMP/ultima.out" || true)"
printf '#!/usr/bin/env bash\necho "--- SOLO-GANCHO (3): ---"\nfor i in 01 02 03; do echo "  pata$i"; done\necho "--- fin ---"\n' > "$TMP/deriva-paridad.sh"
chmod +x "$TMP/deriva-paridad.sh"
rc_b="$(corre "$d")"; b="$(grep -c 'rc=' "$TMP/ultima.out" || true)"
if [ "$rc_a" = 0 ] && [ "$rc_b" = 0 ] && [ "$a" -gt "$b" ]; then
	PASS=$((PASS+1)); printf 'ok   %-58s %s -> %s patas\n' "(5-bis) re-deriva: sigue al comando, no cachea" "$a" "$b"
else
	FAIL=$((FAIL+1)); printf 'FAIL %-58s rc %s/%s patas %s/%s\n' "(5-bis) no re-deriva" "$rc_a" "$rc_b" "$a" "$b"
fi

# --- 6 · MUTANTE «SHA NO PUBLICADO» ------------------------------------------------------------
# Mueve UNA dimensión: un commit que no se empuja. Lista, patas y limpieza, idénticas a (1).
d="$(fixture nopublicado 4 0 no)"
check "(6) MUTANTE SHA no publicado -> 2" 2 "$(corre "$d")"

# --- 7 · árbol sucio: el sujeto no es un árbol publicado ---------------------------------------
d="$(fixture sucio 4 0 si no)"
check "(7) árbol sucio -> 2 (el sujeto no es lo publicado)" 2 "$(corre "$d")"

# --- 8 · `--only` no puede colar una pata de FUERA de la lista derivada ------------------------
d="$(fixture only 4 0)"
check "(8) --only con una pata derivada -> 0" 0 "$(corre "$d" --only pata03)"
check "(8-bis) --only con una pata INVENTADA -> 2" 2 "$(corre "$d" --only pata99)"

# --- 9 · el rótulo «(informa, no bloquea)» se PELA, no se toma como parte del nombre -----------
# La pata02 del fixture lo lleva pegado. Si no se pelara, `task -x lint:pata02   (informa...)`
# no existiría y el centinela contaría una ciega — es decir, saldría 2 en vez de 0.
d="$(fixture rotulo 4 0)"
rc="$(corre "$d")"
if [ "$rc" = 0 ] && grep -q 'lint:pata02 ' "$TMP/ultima.out"; then
	PASS=$((PASS+1)); printf 'ok   %-58s\n' "(9) pela «(informa, no bloquea)» del nombre"
else
	FAIL=$((FAIL+1)); printf 'FAIL %-58s rc=%s\n' "(9) no pela el rótulo del nombre" "$rc"
	grep -n 'pata02' "$TMP/ultima.out" | sed 's/^/       /' | head -2
fi

# --- 10 · LO QUE NO PARSEA REHÚSA, NO SE DESCARTA -----------------------------------------------
# Encontrado releyendo el centinela contra su propia lista de límites, no por el contraste: una
# pata con MAYÚSCULA la tiraba el filtro **en silencio**, y el guion habría publicado «medí N de N»
# con la N ya recortada. Un descarte silencioso es peor que un rechazo, porque el rechazo se ve.
# ⛔ UNA SOLA DIMENSIÓN: el cardinal (4) coincide con las CUATRO líneas del cuerpo; lo único que
# cambia respecto del fixture sano es la FORMA de un nombre. La versión anterior declaraba 3 con
# cuatro patas y cambiaba cardinal, miembros y forma a la vez — lo señaló el contraste (e), y era
# justo la propiedad que yo había escrito en esta cabecera sin cumplirla.
d="$(fixture mayuscula 4 0)"
printf '#!/usr/bin/env bash\necho "--- SOLO-GANCHO (4): ---"\necho "  pata01"\necho "  Pata02:Selftest"\necho "  pata03"\necho "  pata04"\necho "--- fin ---"\n' > "$TMP/mayuscula-paridad.sh"
chmod +x "$TMP/mayuscula-paridad.sh"
check "(10) una pata que no parsea -> 2, no se descarta" 2 "$(corre "$d")"
grep -q 'Pata02:Selftest' "$TMP/ultima.out" && { PASS=$((PASS+1)); printf 'ok   %-58s\n' "(10b) y NOMBRA la línea que no entiende"; } \
	|| { FAIL=$((FAIL+1)); printf 'FAIL %-58s\n' "(10b) no nombra la línea que no entiende"; }

# --- 11 · una sub-cabecera dentro del bloque también rehúsa ------------------------------------
d="$(fixture subcabecera 4 0)"
printf '#!/usr/bin/env bash\necho "--- SOLO-GANCHO (4): ---"\necho "  pata01"\necho "  ## las caras:"\necho "  pata02"\necho "  pata03"\necho "--- fin ---"\n' > "$TMP/subcabecera-paridad.sh"
chmod +x "$TMP/subcabecera-paridad.sh"
check "(11) sub-cabecera en el bloque -> 2 (no mide un subconjunto)" 2 "$(corre "$d")"

# --- 12 · NO-DISPARO del fail-closed, UNA DIMENSIÓN CADA UNO ----------------------------------
# ⛔ La versión anterior metía CRLF Y líneas en blanco en el mismo caso. Lo señaló el contraste
# `sol max` (e): un caso que mueve dos cosas no dice cuál de las dos sostiene el verde. Partido.
d="$(fixture crlf 3 0)"
printf '#!/usr/bin/env bash\nprintf -- "--- SOLO-GANCHO (3): ---\\r\\n  pata01\\r\\n  pata02\\r\\n  pata03\\r\\n--- fin ---\\r\\n"\n' > "$TMP/crlf-paridad.sh"
chmod +x "$TMP/crlf-paridad.sh"
check "(12a) SÓLO CRLF -> 0, sí mide" 0 "$(corre "$d")"

d="$(fixture blancos 3 0)"
printf '#!/usr/bin/env bash\necho "--- SOLO-GANCHO (3): ---"\necho ""\necho "  pata01"\necho ""\necho "  pata02"\necho "  pata03"\necho "--- fin ---"\n' > "$TMP/blancos-paridad.sh"
chmod +x "$TMP/blancos-paridad.sh"
check "(12b) SÓLO líneas en blanco -> 0, sí mide" 0 "$(corre "$d")"

# --- 14 · A-01 · el CARDINAL del productor, aislado ---------------------------------------------
# Mueve UNA dimensión: la cabecera dice 4 y el cuerpo trae 3 nombres VÁLIDOS. Sin atar el cardinal,
# el guion contaba el cuerpo y lo comparaba consigo mismo: salía 0 diciendo «derivé 3, medí 3».
d="$(fixture cardinal 4 0)"
printf '#!/usr/bin/env bash\necho "--- SOLO-GANCHO (4): ---"\necho "  pata01"\necho "  pata02"\necho "  pata03"\necho "--- fin ---"\n' > "$TMP/cardinal-paridad.sh"
chmod +x "$TMP/cardinal-paridad.sh"
check "(14) A-01: cabecera 4 y cuerpo 3 -> 2, no 0" 2 "$(corre "$d")"
grep -q 'el productor dice 4' "$TMP/ultima.out" && { PASS=$((PASS+1)); printf 'ok   %-58s\n' "(14b) y nombra las dos cifras"; } \
	|| { FAIL=$((FAIL+1)); printf 'FAIL %-58s\n' "(14b) no nombra las dos cifras"; }

# --- 15 · A-02 · `--only` con un PATRÓN, no un nombre -------------------------------------------
d="$(fixture patron 4 0)"
check "(15) A-02: --only 'pata0.' (regex) -> 2, no 0" 2 "$(corre "$d" --only 'pata0.')"

# --- 16 · A-02 · selección vacía --------------------------------------------------------------
d="$(fixture vacia 4 0)"
check "(16) A-02: --only ',' -> 2 (cero sujetos no es limpio)" 2 "$(corre "$d" --only ',')"

# --- 17 · A-02 · `--dry` no es un verde ---------------------------------------------------------
d="$(fixture dry 4 0)"
check "(17) A-02: --dry -> 2, no 0 (enumeró, no midió)" 2 "$(corre "$d" --dry)"

# --- 18 · A-03 · un `git status` ILEGIBLE no es un árbol limpio ---------------------------------
d="$(fixture statusroto 4 0)"
printf 'basura' > "$d/.git/index"
check "(18) A-03: git status ilegible -> 2, no 0" 2 "$(corre "$d")"

# --- 19 · SUJETO INCOMPLETO · ser ANCESTRO no basta: hay que ser la punta -----------------------
d="$(fixture atrasado 4 0)"
( cd "$d" && git checkout -q -b tmp >/dev/null 2>&1 && echo y > otro.txt && git add -A >/dev/null 2>&1 \
  && git commit -qm "avanza main" >/dev/null 2>&1 && git push -q origin tmp:main >/dev/null 2>&1 \
  && git checkout -q main >/dev/null 2>&1 && git fetch -q origin main >/dev/null 2>&1 )
check "(19) HEAD ancestro pero NO la punta -> 2" 2 "$(corre "$d")"

# --- 20 · el árbol cambia MIENTRAS mide: se revalida al cerrar ----------------------------------
# Una pata que escribe en el árbol que se está midiendo. Sin la revalidación de cierre, las patas
# posteriores medían otro árbol y el veredicto nombraba el SHA inicial.
d="$(fixture ensucia 3 0)"
python3 - "$d" <<'PYEOF'
import sys
p=sys.argv[1]+"/Taskfile.yml"; s=open(p).read()
s=s.replace('  lint:pata01:\n    cmds:\n      - sh -c "exit 0"',
            '  lint:pata01:\n    cmds:\n      - sh -c "echo residuo > residuo.txt"')
open(p,'w').write(s)
PYEOF
( cd "$d" && git add -A >/dev/null 2>&1 && git commit -qm "pata que ensucia" >/dev/null 2>&1 && git push -q origin main >/dev/null 2>&1 && git fetch -q origin main >/dev/null 2>&1 )
check "(20) una pata ensucia el árbol -> 2 al revalidar" 2 "$(corre "$d")"


# --- 13 · LA PROCEDENCIA DE LA LISTA SE PUBLICA ------------------------------------------------
# De los límites que declaró en la cabecera de check-ci-history-depth.sh, éste guion
# comparte uno: «se pierde la PROCEDENCIA del repositorio». La lista puede venir de un guion de
# FUERA del árbol medido —esta batería lo hace a propósito, por hermetismo— y entonces la lista
# describe un árbol y el rc por pata describe otro. No se prohíbe; se DICE.
d="$(fixture procedencia 4 0)"
check "(13) lista de fuera del árbol -> sigue midiendo (0)" 0 "$(corre "$d")"
grep -q 'FUERA del arbol medido' "$TMP/ultima.out" && { PASS=$((PASS+1)); printf 'ok   %-58s\n' "(13b) y lo DICE en el veredicto"; } \
	|| { FAIL=$((FAIL+1)); printf 'FAIL %-58s\n' "(13b) no publica la procedencia de la lista"; }
# --- 21 · IDENTIDAD DE TAREA · las tres etiquetas que NO llevan prefijo `lint:` ------------------
# El bloqueo de la v2: el productor publica ETIQUETAS del gancho, y tres de ellas son tareas
# EXACTAS (`vet`, `test:cli-walk`, `test:publish-enterprise-artifacts`). Anteponiendo `lint:`
# siempre, el guion invocaba `lint:vet` —inexistente— y publicaba ROJA sin haber medido `vet`.
d="$(fixture identidad 2 0)"
printf '#!/usr/bin/env bash\necho "--- SOLO-GANCHO (5): ---"\necho "  pata01"\necho "  pata02"\necho "  vet"\necho "  test:cli-walk"\necho "  test:publish-enterprise-artifacts"\necho "--- fin ---"\n' > "$TMP/identidad-paridad.sh"
chmod +x "$TMP/identidad-paridad.sh"
check "(21) etiquetas SIN prefijo lint: -> 0, las mide" 0 "$(corre "$d")"
if grep -qE '^watchdog-hook-only-legs: +vet +rc=0' "$TMP/ultima.out" && grep -q 'test:cli-walk ' "$TMP/ultima.out"; then
	PASS=$((PASS+1)); printf 'ok   %-58s\n' "(21b) y las nombra por su tarea EXACTA, sin lint:"
else
	FAIL=$((FAIL+1)); printf 'FAIL %-58s\n' "(21b) no resuelve la tarea exacta"
	grep -E 'vet|cli-walk' "$TMP/ultima.out" | sed 's/^/       /' | head -3
fi

# --- 22 · una etiqueta que no resuelve a NINGUNA tarea -> 2, no un rojo falso -------------------
d="$(fixture inexistente 2 0)"
printf '#!/usr/bin/env bash\necho "--- SOLO-GANCHO (3): ---"\necho "  pata01"\necho "  pata02"\necho "  no-existe-esta"\necho "--- fin ---"\n' > "$TMP/inexistente-paridad.sh"
chmod +x "$TMP/inexistente-paridad.sh"
check "(22) etiqueta sin tarea -> 2 (no un ROJO falso)" 2 "$(corre "$d")"

# --- 23 · A-02 · `--only` VACÍO no es `--only` AUSENTE ------------------------------------------
d="$(fixture onlyvacio 4 0)"
check "(23) --only '' -> 2 (era 0: medía las cuatro)" 2 "$(corre "$d" --only '')"

# --- 24 · el ancestro aceptado PUBLICA su distancia ---------------------------------------------
d="$(fixture ancestro 4 0)"
( cd "$d" && git checkout -q -b tmp2 >/dev/null 2>&1 && echo z > z.txt && git add -A >/dev/null 2>&1 \
  && git commit -qm "avanza" >/dev/null 2>&1 && git push -q origin tmp2:main >/dev/null 2>&1 \
  && git checkout -q main >/dev/null 2>&1 && git fetch -q origin main >/dev/null 2>&1 )
rc="$( ( cd "$d" && OLIVARES_ROOT="$d" OLIVARES_PARITY_CMD="$TMP/ancestro-paridad.sh" \
	OLIVARES_WATCHDOG_MIN_LEGS=3 OLIVARES_WATCHDOG_MIN_TASKS=3 OLIVARES_WATCHDOG_MAX_BEHIND=1 bash "$SUT" >"$TMP/anc.out" 2>&1 ); echo $? )"
if [ "$rc" = 0 ] && grep -q 'mido un ANCESTRO: HEAD va 1 commit' "$TMP/anc.out"; then
	PASS=$((PASS+1)); printf 'ok   %-58s rc=0\n' "(24) ancestro permitido: mide Y publica la distancia"
else
	FAIL=$((FAIL+1)); printf 'FAIL %-58s rc=%s\n' "(24) no publica la distancia del ancestro" "$rc"
	tail -3 "$TMP/anc.out" | sed 's/^/       /'
fi

# --- 25 · rc=200 es AMBIGUO: los DOS brazos, uno por caso ---------------------------------------
# `task -x` devuelve 200 tanto si la tarea NO EXISTE como si el guion de la pata sale 200. Mapearlo
# a un solo lado miente en una dirección u otra, así que el guion pregunta si la tarea sigue ahí.
# Brazo A — la tarea EXISTE y la pata sale 200: es un rojo de verdad.
d="$(fixture rc200 3 200)"
check "(25a) pata sale 200 y su tarea existe -> 1 (ROJA)" 1 "$(corre "$d")"
grep -q 'su tarea sigue existiendo' "$TMP/ultima.out" && { PASS=$((PASS+1)); printf 'ok   %-58s\n' "(25a-bis) y dice por qué la llama ROJA"; } \
	|| { FAIL=$((FAIL+1)); printf 'FAIL %-58s\n' "(25a-bis) no explica el 200"; }

# Brazo B — la tarea DESAPARECE entre el listado y la ejecución: es la carrera, y es CIEGA.
# La pata01 borra del Taskfile la definición de pata03, que se mide después. El borrado ensucia el
# árbol —y la revalidación de cierre lo cazará—, pero la CLASIFICACIÓN de pata03 ocurre antes, que
# es lo que este caso mide: la línea, no el rc global.
d="$(fixture carrera 3 0)"
cat > "$d/borra.sh" <<'BORRA'
#!/bin/sh
python3 - <<'PYX'
import re
s=open("Taskfile.yml").read()
s=re.sub(r'  lint:pata03:\n    cmds:\n      - sh -c "exit 0"\n', '', s)
open("Taskfile.yml","w").write(s)
PYX
BORRA
chmod +x "$d/borra.sh"
python3 - "$d" <<'PYEOF'
import sys
p=sys.argv[1]+"/Taskfile.yml"; s=open(p).read()
s=s.replace('  lint:pata01:\n    cmds:\n      - sh -c "exit 0"',
            '  lint:pata01:\n    cmds:\n      - sh borra.sh')
open(p,'w').write(s)
PYEOF
( cd "$d" && git add -A >/dev/null 2>&1 && git commit -qm "pata que borra otra" >/dev/null 2>&1 \
  && git push -q origin main >/dev/null 2>&1 && git fetch -q origin main >/dev/null 2>&1 )
rc="$(corre "$d")"
if grep -q 'la tarea no existe ya' "$TMP/ultima.out"; then
	PASS=$((PASS+1)); printf 'ok   %-58s rc=%s\n' "(25b) la tarea desaparece a mitad -> CIEGA, no ROJA" "$rc"
else
	FAIL=$((FAIL+1)); printf 'FAIL %-58s rc=%s\n' "(25b) no distingue la carrera de un rojo" "$rc"
	grep -E 'pata0[13]|200|ROJA|CIEGA' "$TMP/ultima.out" | sed 's/^/       /' | head -4
fi

# --- 26 · AMBIGÜEDAD REAL: `lint:L` y `L` existiendo a la vez ----------------------------------
# El 21b sólo comprobaba cómo IMPRIME las tareas exactas; no fabricaba la ambigüedad. Sin este
# caso, el brazo «existen las DOS -> rehúsa» del resolutor no lo mataba nada de mi banco: lo mató
# una sonda del contraste, no la mía.
d="$(fixture ambigua 2 0)"
printf '  lint:vet:\n    cmds:\n      - sh -c "exit 0"\n' >> "$d/Taskfile.yml"
( cd "$d" && git add -A >/dev/null 2>&1 && git commit -qm "lint:vet Y vet" >/dev/null 2>&1 \
  && git push -q origin main >/dev/null 2>&1 && git fetch -q origin main >/dev/null 2>&1 )
printf '#!/usr/bin/env bash\necho "--- SOLO-GANCHO (3): ---"\necho "  pata01"\necho "  pata02"\necho "  vet"\necho "--- fin ---"\n' > "$TMP/ambigua-paridad.sh"
chmod +x "$TMP/ambigua-paridad.sh"
check "(26) 'vet' resuelve a lint:vet Y a vet -> 2" 2 "$(corre "$d")"
grep -q 'DOS tareas' "$TMP/ultima.out" && { PASS=$((PASS+1)); printf 'ok   %-58s\n' "(26b) y dice que la etiqueta resuelve a dos"; } \
	|| { FAIL=$((FAIL+1)); printf 'FAIL %-58s\n' "(26b) no nombra la ambigüedad"; }

echo
echo "watchdog-hook-only-legs selftest: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
