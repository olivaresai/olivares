#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# test-rescue-backlog.sh — tests for check-rescue-backlog.sh.
# Use disposable repositories, never live refs/rescate/*: integration would change
# the verdict without changes to the gate.
# requested the essential positive control: a patch already in the base under
# another SHA. Zero pending alone cannot distinguish integration from an empty check.
# The first attempt used an ancestor commit, so git cherry returned no + or - rows:
# there was nothing to compare, and that silent zero proved no discrimination.
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
[ -r "$GATE" ] || { echo "test-rescue-backlog: ⛔ CANNOT INSPECT: $GATE does not exist" >&2; exit 2; }

BANCO="$(mktemp -d "${TMPDIR:-/tmp}/rescate-bat.XXXXXX")" || exit 2
trap 'rm -rf -- "${BANCO:-}"' EXIT INT TERM

pass_count=0; fail_count=0
comprobar() { if [ "$3" -eq "$2" ]; then printf '  ok    %-56s rc=%s\n' "$1" "$3"; pass_count=$((pass_count+1))
	else printf '  FAIL %-56s rc=%s (expected %s)\n' "$1" "$3" "$2"; fail_count=$((fail_count+1)); fi; }
dice() { if grep -q "$2" "$BANCO/output" 2>/dev/null; then printf '  ok    %-56s NAMES it\n' "$1"; pass_count=$((pass_count+1))
	else printf '  FAIL %-56s does not report «%s»\n' "$1" "$2"; fail_count=$((fail_count+1)); fi; }

monta() {
	R="$BANCO/repo"; rm -rf -- "$R"; mkdir -p "$R"
	git -C "$R" init -q -b main 2>/dev/null || return 1
	git -C "$R" config user.email b@b; git -C "$R" config user.name b
	printf 'base\n' > "$R/a.txt"; git -C "$R" add -A >/dev/null 2>&1
	git -C "$R" commit -qm base >/dev/null 2>&1
	git -C "$R" branch -f rescate/vacio main >/dev/null 2>&1
}
corre() { ( cd "$BANCO/repo" && OLIVARES_RESCUE_BASE=main OLIVARES_RESCUE_LEDGER="$BANCO/ledger.md" bash "$GATE" ) >"$BANCO/output" 2>&1; }

monta || { echo "test-rescue-backlog: ⛔ CANNOT INSPECT: could not stage the repository" >&2; exit 2; }

# 1 · un rescate cuyo parche NO está en la base: PENDIENTE
git -C "$BANCO/repo" checkout -q -b rescate/pendiente main
printf 'algo nuevo\n' > "$BANCO/repo/b.txt"
git -C "$BANCO/repo" add -A >/dev/null 2>&1; git -C "$BANCO/repo" commit -qm "trabajo sin integrar" >/dev/null 2>&1
git -C "$BANCO/repo" checkout -q main
corre; comprobar "a rescue with patches outside the baseline is RED" 1 "$?"
dice "and NAMES the branch" "rescate/pendiente"
dice "and reports how many patches" "1 unintegrated patch"
dice "and reports its AGE" "day(s)"

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
	printf '  FAIL %-56s the fixture did NOT produce another SHA: the case does not distinguish behaviors\n' "(positive-control premise)"
	fail_count=$((fail_count + 1))
else
	printf '  ok    %-56s the fixture DOES produce another SHA\n' "(positive-control premise)"
	pass_count=$((pass_count + 1))
fi
corre; comprobar "the SAME patch already in the baseline, with another SHA, is no longer counted" 0 "$?"

# 3 · la dirección que NO dispara: un rescate vacío
git -C "$BANCO/repo" branch -D rescate/pendiente >/dev/null 2>&1
corre; comprobar "no rescues with their own patches => CLEAN" 0 "$?"

# 4 · la salida explícita del libro mayor (integración NO idéntica: squash, revisión…)
git -C "$BANCO/repo" checkout -q -b rescate/aplastado main
printf 'otro\n' > "$BANCO/repo/c.txt"
git -C "$BANCO/repo" add -A >/dev/null 2>&1; git -C "$BANCO/repo" commit -qm "se integró aplastado" >/dev/null 2>&1
git -C "$BANCO/repo" checkout -q main
corre; comprobar "a squashed patch is still counted WITHOUT a declaration" 1 "$?"
printf 'rescue-integrated: rescate/aplastado abc1234 2026-09-02\n' > "$BANCO/ledger.md"
corre; comprobar "and with a ledger declaration, is no longer counted" 0 "$?"

# 5 · las dos formas de NO HE PODIDO MIRAR, que nunca son verde
( cd "$BANCO" && OLIVARES_RESCUE_BASE=main bash "$GATE" ) >"$BANCO/output" 2>&1
comprobar "outside a repository means CANNOT INSPECT" 2 "$?"
( cd "$BANCO/repo" && OLIVARES_RESCUE_BASE=no-existe-esta-base bash "$GATE" ) >"$BANCO/output" 2>&1
comprobar "a missing baseline means CANNOT INSPECT, not clean" 2 "$?"

echo "test-rescue-backlog: $pass_count passed, $fail_count failed"
[ "$fail_count" -eq 0 ] || exit 1
exit 0
