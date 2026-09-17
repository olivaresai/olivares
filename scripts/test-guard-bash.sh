#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-guard-bash.sh — el testigo de `.claude/hooks/guard-bash.sh`.
#
# ⛔ POR QUE EXISTE. Ese hook corta comandos que en este entorno miden mal, y hasta hoy **no lo
# probaba nada**: ningun guion del arbol lee `.claude/hooks/`. Una guarda sin testigo es una
# leccion escrita e indefensa — se puede romper entera sin que nada enrojezca, y el sintoma seria
# el peor posible: **deja de cortar y nadie se entera**, porque un hook que no bloquea es
# indistinguible de un hook que no hacia falta.
#
# QUE COMPRUEBA, y las dos direcciones importan por igual:
#   · CORTA lo que debe cortar (rc 2)         — si deja de hacerlo, el control desaparece en silencio
#   · DEJA PASAR lo que no lo es (rc 0)       — un falso positivo cuesta trabajo real a cada carril
#   · el escape `# guard:ok` funciona          — o el override documentado seria mentira
#   · FALLA ABIERTO sin `jq`                   — es su postura declarada: un guardian roto que
#                                                bloquea todo convierte un despiste en sesion muerta
set -uo pipefail

# Aislamiento de git: este guion empareja `mktemp -d` con `git` —aunque aqui `git` solo aparece
# como DATO de prueba, una cadena que se le pasa al hook— y el gate no distingue uso de dato, con
# razon: un GIT_DIR envenenado apuntaria al repo real y el coste de equivocarse es destruir algo.
# Fallar cerrado: no poder sanear es «no he podido aislar», nunca «no hacia falta aislar».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}

cd "$(dirname "$0")/.."
# export-closure: hub-only .claude/hooks/guard-bash.sh — el hook es del ENTORNO DE TRABAJO, no
# del producto: corta comandos que miden mal en esta caja (zsh, /tmp noexec, clon compartido) y no
# tiene sentido en el arbol publicado, que ni tiene esas condiciones ni ejecuta hooks de sesion.
# Por eso el export lo quita y este testigo lo llama GUARDADO: si no esta, sale 2 —NO HE PODIDO
# MIRAR— en vez de fingir un pase.
if [ -f ".claude/hooks/guard-bash.sh" ]; then
	H=".claude/hooks/guard-bash.sh"
else
	# The hook is hub-only (`.claude/` is TOP_BLOCK). In a stamped public export that
	# absence is the contract, not a missing control: answer SCOPED. In any other tree
	# it remains COULD NOT LOOK (exit 2).
	_cls=""
	if [ -f scripts/hub-leg.sh ]; then
		_cls="$(bash scripts/hub-leg.sh --classify --root . 2>/dev/null || true)"
	fi
	if [ "$_cls" = "public" ]; then
		echo "test-guard-bash: SCOPED — public export; .claude/hooks/guard-bash.sh is hub-only"
		echo "  session tooling and is curated out of the published tree. This witness has no"
		echo "  subject here and never will. In the hub it still grades the hook."
		exit 0
	fi
	echo "test-guard-bash: no existe .claude/hooks/guard-bash.sh (hub-only); no puedo probarlo" >&2
	exit 2
fi
command -v jq >/dev/null 2>&1 || { echo "test-guard-bash: sin jq no puedo construir la entrada" >&2; exit 2; }

pass=0; fail=0
caso() { # caso <nombre> <rc-esperado> <comando>
	local nombre="$1" want="$2" cmd="$3" rc=0
	# export-closure: hub-only .claude/hooks/guard-bash.sh — el hook es del entorno de trabajo y el
	# export lo quita; la llamada va GUARDADA aqui mismo porque una declaracion excusa una llamada
	# guardada, no un fichero: sin la guarda, en el arbol publicado esta linea moriria con exit 127.
	if [ ! -f "$H" ]; then
		printf 'FAIL  %-58s (el hook desaparecio a mitad)\n' "$nombre"; fail=$((fail + 1)); return
	fi
	printf '%s' "$(jq -nc --arg c "$cmd" '{tool_input:{command:$c}}')" | bash "$H" >/dev/null 2>&1 || rc=$?
	if [ "$rc" -eq "$want" ]; then
		printf 'ok    %-58s rc=%s\n' "$nombre" "$rc"; pass=$((pass + 1))
	else
		printf 'FAIL  %-58s rc=%s, esperado %s\n' "$nombre" "$rc" "$want"; fail=$((fail + 1))
	fi
}

# --- CORTA (rc 2) · cada uno con la medida que lo puso en el hook ---
caso "for sobre variable sin comillas (zsh no divide en palabras)" 2 'for x in $VAR; do echo $x; done'
caso "sonda /dev/tcp (zsh no la implementa: dice cerrado siempre)"  2 '(echo > /dev/tcp/localhost/5432) 2>/dev/null'
caso "matar por patron en un host compartido"                        2 'pkill -f algo'
caso "git add sin ruta en un clon compartido"                        2 'git add -A'
caso "comm sobre listas ordenadas con sort -n"                       2 'comm -23 <(sort -n a) <(sort -n b)'

# --- DEJA PASAR (rc 0): un falso positivo cuesta trabajo real ---
caso "for sobre una variable ENTRECOMILLADA"                         0 'for x in "$VAR"; do echo "$x"; done'
caso "while read, la forma correcta en zsh"                          0 'while IFS= read -r x; do echo "$x"; done < f'
caso "git add CON ruta explicita"                                    0 'git add core/algo.go'
caso "comm sobre listas en orden lexicografico"                      0 'comm -23 a.lex b.lex'
caso "sort -n suelto, sin comm"                                      0 'sort -n fichero.txt'
# ⛔ LIMITACION CONOCIDA, FIJADA A PROPOSITO CON rc=2. El hook casa la PALABRA, no el sujeto:
#    la palabra prohibida DENTRO de una cadena dispara igual. Medido dos veces el 2026-08-31, las
#    dos con la palabra en un `echo` de prosa explicativa.
#    NO se «arregla» sin parsear el comando —distinguir codigo de cadena en shell exige un lexer,
#    y este hook existe para ser barato y fallar abierto—. Se FIJA aqui para que quien lo cambie
#    vea que el cambio es deliberado: si algun dia deja de cortar, esta fila enrojece y obliga a
#    decidirlo, en vez de que el control se afloje sin que nadie lo note.
#    El escape documentado (`# guard:ok`) es la salida para el caso legitimo.
caso "LIMITACION: la palabra en prosa TAMBIEN dispara"               2 'echo "no uses pkill aqui"' 
caso "kill por PID, que es la forma sancionada"                      0 'kill -TERM 1234'

# --- el escape documentado ---
caso "escape guard:ok sobre un comando que si se corta"              0 'git add -A # guard:ok'

# --- FALLA ABIERTO: es su postura, y se fija para que no se invierta sin querer ---
sin_jq=0
# ⚠ Se quita SOLO `jq`, no el PATH entero: un `PATH=/nonexistent` mata tambien `cat` y devuelve
#   127 («command not found»), que NO es el fallo-abierto del hook sino que no arranco nada.
#   Medido: la primera version de esta prueba daba 127 y acusaba al hook de algo que no hacia.
_sinjq="$(mktemp -d)"; trap 'rm -rf "$_sinjq"' EXIT
for _b in cat printf grep sed awk bash; do
	_p="$(command -v "$_b" 2>/dev/null)" && ln -sf "$_p" "$_sinjq/$_b"
done
# export-closure: hub-only .claude/hooks/guard-bash.sh — mismo motivo que arriba; se repite la
# declaracion porque este sitio de llamada esta lejos del primero y una exencion vale donde alguien
# la miro, no en todo el fichero.
if [ -f "$H" ]; then
	printf '%s' '{"tool_input":{"command":"git add -A"}}' \
		| PATH="$_sinjq" bash "$H" >/dev/null 2>&1 || sin_jq=$?
fi
if [ "$sin_jq" -eq 0 ]; then
	printf 'ok    %-58s rc=0\n' "sin jq FALLA ABIERTO (postura declarada)"; pass=$((pass + 1))
else
	printf 'FAIL  %-58s rc=%s, esperado 0\n' "sin jq FALLA ABIERTO (postura declarada)" "$sin_jq"; fail=$((fail + 1))
fi

# --- GUARDA 6: `pgrep -f` cuyo resultado se MATA en el mismo comando -----------------------------
# El patron viaja en la linea de comandos que `pgrep` inspecciona, asi que se casa a SI MISMO. No es
# un descuido: es estructural, y `-f` lo garantiza. El caso que la puso aqui es del 2026-09-01 y es
# propio — un `pgrep -f` mio caso mi shell y el `kill` de al lado le mando SIGTERM (rc 144).
caso "pgrep -f cuyo resultado se mata en el mismo comando"  2 'pgrep -f foo | xargs kill'
caso "y la forma de bucle, que es la que me mordio"         2 'for p in $(pgrep -f foo); do kill $p; done'
caso "y --full, que es lo mismo escrito largo"              2 'pgrep --full foo && kill 123'
# ⚠ LOS TRES NEGATIVOS IMPORTAN TANTO COMO LOS POSITIVOS: sin ellos la guarda podria estar
#    bloqueando TODO y estas filas no lo verian.
caso "mirar no mata: pgrep -f solo, sin kill, PASA"         0 'pgrep -f foo'
caso "matar por PID sin pgrep PASA"                         0 'kill -TERM 1234'
caso "y sin -f no aplica: pgrep -l no inspecciona cmdline"  0 'pgrep -l foo | wc -l'

# Public export: the hook is curated out with `.claude/`. Exit 2 there is a CI red
# by construction. The witness must answer SCOPED when hub-leg classifies `public`.
# GUARD_BASH_SKIP_PUBLIC_FIXTURE stops the copy from recursing into this case.
if [ "${GUARD_BASH_SKIP_PUBLIC_FIXTURE:-}" != "1" ]; then
	_pub="$(mktemp -d "${TMPDIR:-/workspace/.olivares-tmptest}/guard-bash-public.XXXXXX")"
	mkdir -p "$_pub/scripts/lib"
	cp scripts/hub-leg.sh "$_pub/scripts/hub-leg.sh"
	cp scripts/lib/git-env.sh "$_pub/scripts/lib/git-env.sh"
	cp scripts/test-guard-bash.sh "$_pub/scripts/test-guard-bash.sh"
	printf '%s\n' \
		'This repository is the public, curated export of the Olivares AI control plane.' \
		> "$_pub/PUBLIC-EXPORT.md"
	_pub_out=0
	_pub_txt="$(
		cd "$_pub" && env GUARD_BASH_SKIP_PUBLIC_FIXTURE=1 bash scripts/test-guard-bash.sh
	)" || _pub_out=$?
	if [ "$_pub_out" -eq 0 ] && printf '%s' "$_pub_txt" | grep -qF 'SCOPED'; then
		printf 'ok    %-58s rc=0 SCOPED\n' "public export without the hook is SCOPED, not exit 2"
		pass=$((pass + 1))
	else
		printf 'FAIL  %-58s rc=%s (want 0+SCOPED)\n' "public export without the hook is SCOPED, not exit 2" "$_pub_out"
		printf '%s\n' "$_pub_txt" | sed 's/^/          /' | head -8
		fail=$((fail + 1))
	fi
	rm -rf "$_pub"
fi

printf '\ntest-guard-bash: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
