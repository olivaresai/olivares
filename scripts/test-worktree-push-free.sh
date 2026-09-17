#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Batería de scripts/check-worktree-push-free.sh — prueba las TRES respuestas y, sobre todo, las ROJAS.
#
# ⛔ EL CASO DIFÍCIL ES EL 1, Y ES EL ÚNICO QUE IMPORTA. Un verde sobre un árbol sin procesos lo
# daría también una guarda que no mirase nada. Para probar el rojo hay que FABRICAR un push: un
# proceso cuyo `argv` contenga `olivares-prepush` y cuyo `/proc/<pid>/cwd` sea el worktree. Eso
# obliga a un directorio EJECUTABLE — /tmp está montado noexec en estas cajas y el señuelo moriría
# con 126, que la guarda leería como «no hay push». Por eso usa `olivares_pick_exec_workdir`, que
# no ELIGE un directorio: DEMUESTRA que puede crear y ejecutar en él.
set -uo pipefail
RAIZ="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
SUT="$RAIZ/scripts/check-worktree-push-free.sh"
[ -r "$SUT" ] || { echo "test-worktree-push-free: NO HE PODIDO MIRAR: no leo $SUT" >&2; exit 2; }
# shellcheck source=lib/exec-workdir.sh
. "$RAIZ/scripts/lib/exec-workdir.sh" || {
	echo "test-worktree-push-free: NO HE PODIDO MIRAR: falta scripts/lib/exec-workdir.sh" >&2; exit 2; }

WORK="$(olivares_pick_exec_workdir wtpushfree)" || {
	echo "test-worktree-push-free: NO HE PODIDO MIRAR: sin directorio ejecutable" >&2; exit 2; }
SENUELO=""
limpia() { [ -n "$SENUELO" ] && kill "$SENUELO" 2>/dev/null; rm -rf "$WORK"; }
trap limpia EXIT HUP INT TERM

fallos=0
comprueba() { # <nombre> <esperado> <rc>
	if [ "$2" = "$3" ]; then printf '  ok    %-52s rc=%s\n' "$1" "$3"
	else printf '  FAIL  %-52s esperaba %s, dio %s\n' "$1" "$2" "$3"; fails=1; fallos=$((fallos + 1)); fi
}

ARBOL="$WORK/arbol"; mkdir -p "$ARBOL"

# ── VERDE: un worktree sin ningún push ────────────────────────────────────────────────────────
bash "$SUT" "$ARBOL" >/dev/null 2>&1; comprueba "sin push: libre" 0 "$?"

# ── ROJO: con un push VIVO. El señuelo se llama como el hook real y vive en el árbol. ─────────
# ⛔ SIN `exec`, y es la diferencia entre una batería que prueba algo y una que no. La primera
# versión hacía `exec sleep 120`: eso REEMPLAZA el argv, el proceso pasa a llamarse `sleep` y la
# guarda —que busca `olivares-prepush` en `ps -eo args`— no lo veía. Verde falso en el caso rojo.
# El hook REAL no hace exec: es `bash …/olivares-prepush.XXXX origin https://…` con su hijo dentro,
# así que el señuelo tiene que conservar su nombre igual y dormir en un HIJO.
printf '#!/usr/bin/env bash\ncd "$1" || exit 1\nsleep 120\n' > "$WORK/olivares-prepush.senuelo"
chmod +x "$WORK/olivares-prepush.senuelo"
"$WORK/olivares-prepush.senuelo" "$ARBOL" &
SENUELO=$!
# Esperar a que el hijo haya hecho el `cd`: sin esto la batería tiene una CARRERA y falla a veces,
# que es peor que fallar siempre. Se comprueba el hecho, no se duerme a ojo.
for _ in $(seq 1 50); do
	[ "$(readlink "/proc/$SENUELO/cwd" 2>/dev/null)" = "$ARBOL" ] && break
	sleep 0.1
done
if [ "$(readlink "/proc/$SENUELO/cwd" 2>/dev/null)" != "$ARBOL" ]; then
	echo "test-worktree-push-free: NO HE PODIDO MIRAR: el señuelo no llegó a $ARBOL" >&2
	exit 2
fi
bash "$SUT" "$ARBOL" >/dev/null 2>&1; comprueba "con push vivo: NO editar" 1 "$?"

# ── Y el control que separa «lo detecta» de «dice que sí a todo»: OTRO árbol, mismo instante. ──
OTRO="$WORK/otro"; mkdir -p "$OTRO"
bash "$SUT" "$OTRO" >/dev/null 2>&1; comprueba "otro árbol con el señuelo vivo: libre" 0 "$?"

kill "$SENUELO" 2>/dev/null; wait "$SENUELO" 2>/dev/null; SENUELO=""
# ── Y al morir el push, el mismo árbol vuelve a estar libre ───────────────────────────────────
for _ in $(seq 1 50); do ps -o pid= -p "$SENUELO" >/dev/null 2>&1 || break; sleep 0.1; done
bash "$SUT" "$ARBOL" >/dev/null 2>&1; comprueba "muerto el push: libre otra vez" 0 "$?"

# ── NO HE PODIDO MIRAR ────────────────────────────────────────────────────────────────────────
bash "$SUT" "$WORK/no-existe" >/dev/null 2>&1; comprueba "ruta inexistente: no he podido mirar" 2 "$?"
bash "$SUT" >/dev/null 2>&1;                   comprueba "sin argumento: no he podido mirar" 2 "$?"

if [ "$fallos" -eq 0 ]; then
	echo "check-worktree-push-free --batería: 6/6 (libre · push vivo · otro árbol · tras morir · sin ruta · sin argumento)"
	exit 0
fi
echo "check-worktree-push-free --batería: $fallos caso(s) en rojo"
exit 1
