#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# test-rescue-backlog.sh — la batería de check-rescue-backlog.sh.
#
# ⛔ SEÑUELOS, NUNCA LOS REFS VIVOS. Contra `refs/rescate/*` de verdad esta batería sería verde el
#    día que alguien integre un rescate y roja al siguiente, sin que nadie tocara el gate. Cada
#    caso monta su repositorio desechable.
#
# ⛔ Y LLEVA EL CONTROL POSITIVO QUE ESTE GATE NECESITA MÁS QUE NINGÚN OTRO (lo pidió al
#    diseñarlo): un parche que YA está en la base pero con OTRO SHA. Sin esa casilla, un censo que
#    devuelve «0 pendientes» no se distingue de uno que no está mirando — y ése es exactamente el
#    modo de fallo del que nace este gate. Mi primer intento de control usó un commit que ya era
#    ANTEPASADO de la base: `git cherry` devolvió 0 `+` y 0 `-` —nada que comparar— y lo leí como
#    «distingue». Un cero mudo se lee igual que un cero limpio.
set -uo pipefail
LC_ALL=C
export LC_ALL

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

RAIZ="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)"
GATE="$RAIZ/scripts/check-rescue-backlog.sh"
[ -r "$GATE" ] || { echo "test-rescue-backlog: ⛔ NO HE PODIDO MIRAR: no existe $GATE" >&2; exit 2; }

BANCO="$(mktemp -d "${TMPDIR:-/tmp}/rescate-bat.XXXXXX")" || exit 2
trap 'rm -rf -- "${BANCO:-}"' EXIT INT TERM

pasan=0; fallan=0
comprobar() { if [ "$3" -eq "$2" ]; then printf '  ok    %-56s rc=%s\n' "$1" "$3"; pasan=$((pasan+1))
	else printf '  FALLA %-56s rc=%s (quiere %s)\n' "$1" "$3" "$2"; fallan=$((fallan+1)); fi; }
dice() { if grep -q "$2" "$BANCO/salida" 2>/dev/null; then printf '  ok    %-56s lo NOMBRA\n' "$1"; pasan=$((pasan+1))
	else printf '  FALLA %-56s no dice «%s»\n' "$1" "$2"; fallan=$((fallan+1)); fi; }

monta() {
	R="$BANCO/repo"; rm -rf -- "$R"; mkdir -p "$R"
	git -C "$R" init -q -b main 2>/dev/null || return 1
	git -C "$R" config user.email b@b; git -C "$R" config user.name b
	printf 'base\n' > "$R/a.txt"; git -C "$R" add -A >/dev/null 2>&1
	git -C "$R" commit -qm base >/dev/null 2>&1
	git -C "$R" branch -f rescate/vacio main >/dev/null 2>&1
}
corre() { ( cd "$BANCO/repo" && OLIVARES_RESCUE_BASE=main OLIVARES_RESCUE_LEDGER="$BANCO/ledger.md" bash "$GATE" ) >"$BANCO/salida" 2>&1; }

monta || { echo "test-rescue-backlog: ⛔ NO HE PODIDO MIRAR: no pude montar el repo" >&2; exit 2; }

# 1 · un rescate cuyo parche NO está en la base: PENDIENTE
git -C "$BANCO/repo" checkout -q -b rescate/pendiente main
printf 'algo nuevo\n' > "$BANCO/repo/b.txt"
git -C "$BANCO/repo" add -A >/dev/null 2>&1; git -C "$BANCO/repo" commit -qm "trabajo sin integrar" >/dev/null 2>&1
git -C "$BANCO/repo" checkout -q main
corre; comprobar "un rescate con parches fuera de la base es ROJO" 1 "$?"
dice "y NOMBRA la rama" "rescate/pendiente"
dice "y dice cuántos parches" "1 parche"
dice "y da su EDAD" "día(s)"

# 2 · ⛔ EL CONTROL POSITIVO: mismo parche, OTRO SHA (el caso del rebase)
#    ⛔ CON `-x`, Y NO ES COSMETICA. Sin el, el cherry-pick de un commit con el MISMO arbol, el
#       MISMO mensaje, el MISMO autor y dentro del MISMO segundo produce el MISMO SHA: git lo
#       resuelve como avance rapido y la premisa del caso —«otro SHA»— es FALSA. Medido: con esa
#       version, el mutante que cambia `git cherry` por `merge-base --is-ancestor` SOBREVIVIA con
#       10/0, porque los dos predicados contestaban lo mismo sobre un fixture que no discriminaba.
#       `-x` anade la linea «(cherry picked from ...)» al mensaje: mismo parche, otro SHA, que es
#       exactamente el caso que este gate existe para distinguir.
git -C "$BANCO/repo" checkout -q main
git -C "$BANCO/repo" cherry-pick -x "$(git -C "$BANCO/repo" rev-parse rescate/pendiente)" >/dev/null 2>&1
# y se COMPRUEBA la premisa antes de juzgar: un control cuyo supuesto no se cumple no prueba nada
if git -C "$BANCO/repo" merge-base --is-ancestor rescate/pendiente main 2>/dev/null; then
	printf '  FALLA %-56s el fixture NO produjo otro SHA: el caso no discrimina\n' "(premisa del control positivo)"
	fallan=$((fallan + 1))
else
	printf '  ok    %-56s el fixture SI produce otro SHA\n' "(premisa del control positivo)"
	pasan=$((pasan + 1))
fi
corre; comprobar "el MISMO parche ya en la base, con otro SHA, deja de contar" 0 "$?"

# 3 · la dirección que NO dispara: un rescate vacío
git -C "$BANCO/repo" branch -D rescate/pendiente >/dev/null 2>&1
corre; comprobar "sin rescates con parches propios, LIMPIO" 0 "$?"

# 4 · la salida explícita del libro mayor (integración NO idéntica: squash, revisión…)
git -C "$BANCO/repo" checkout -q -b rescate/aplastado main
printf 'otro\n' > "$BANCO/repo/c.txt"
git -C "$BANCO/repo" add -A >/dev/null 2>&1; git -C "$BANCO/repo" commit -qm "se integró aplastado" >/dev/null 2>&1
git -C "$BANCO/repo" checkout -q main
corre; comprobar "un parche aplastado sigue contando SIN declaración" 1 "$?"
printf 'rescue-integrated: rescate/aplastado abc1234 2026-09-02\n' > "$BANCO/ledger.md"
corre; comprobar "y con la declaración en el libro mayor, deja de contar" 0 "$?"

# 5 · las dos formas de NO HE PODIDO MIRAR, que nunca son verde
( cd "$BANCO" && OLIVARES_RESCUE_BASE=main bash "$GATE" ) >"$BANCO/salida" 2>&1
comprobar "fuera de un repositorio es NO HE PODIDO MIRAR" 2 "$?"
( cd "$BANCO/repo" && OLIVARES_RESCUE_BASE=no-existe-esta-base bash "$GATE" ) >"$BANCO/salida" 2>&1
comprobar "una base inexistente es NO HE PODIDO MIRAR, no limpio" 2 "$?"

echo "test-rescue-backlog: $pasan pasan, $fallan fallan"
[ "$fallan" -eq 0 ] || exit 1
exit 0
