#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Batería de scripts/check-mid-operation.sh. Cada caso CONSTRUYE el estado que afirma —
# ningún caso lee el repo real, y ninguno da por bueno un veredicto que no haya provocado.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SUT="$HERE/scripts/check-mid-operation.sh"
# Ruta ABSOLUTA de bash, capturada ANTES de tocar el PATH. El caso «sin git en el PATH» invoca el
# SUT con un PATH vacío, y con `bash "$SUT"` el que no encontraba bash era el shell de FUERA: daba
# 127 (orden no encontrada) y parecía un fallo del gate. El 127 era del arnés.
BASH_BIN="$(command -v bash)"
# shellcheck source=scripts/lib/git-env.sh
. "$HERE/scripts/lib/git-env.sh"
olivares_git_env_isolate

pasa=0; falla=0
check() {
	local nombre="$1" esperado="$2" obtenido="$3" extra="${4:-}"
	if [ "$esperado" = "$obtenido" ] && { [ -z "$extra" ] || [ "$extra" = "ok" ]; }; then
		printf '  ok    %-58s rc=%s\n' "$nombre" "$obtenido"; pasa=$((pasa + 1))
	else
		printf '  FAIL  %-58s rc=%s (expected %s) %s\n' "$nombre" "$obtenido" "$esperado" "$extra"; falla=$((falla + 1))
	fi
}

W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT

# repo_con_conflicto <dir> — deja <dir> con dos ramas que chocan en la MISMA línea.
repo_con_conflicto() {
	local d="$1"
	mkdir -p "$d" && git -C "$d" init -q -b main
	printf 'linea original\n' >"$d/f.txt"
	git -C "$d" add f.txt && git -C "$d" commit -q -m "base"
	git -C "$d" checkout -q -b otra
	printf 'version de la rama\n' >"$d/f.txt"
	git -C "$d" commit -q -am "rama"
	git -C "$d" checkout -q main
	printf 'version de main\n' >"$d/f.txt"
	git -C "$d" commit -q -am "main"
}

echo "CLEAN — a tree with no incomplete operations passes"
R="$W/limpio"; mkdir -p "$R" && git -C "$R" init -q -b main
printf 'x\n' >"$R/f.txt"; git -C "$R" add f.txt; git -C "$R" commit -q -m "uno"
out="$(cd "$R" && bash "$SUT" 2>&1)"; rc=$?
check "a freshly committed repository has no incomplete operations" 0 "$rc"
case "$out" in *"OK — no incomplete"*) e=ok ;; *) e="does not SAY so: $out" ;; esac
check "and explicitly reports it" 0 0 "$e"

echo "INCOMPLETE REBASE — the case that caused a partial push to main"
R="$W/rebase"; repo_con_conflicto "$R"
git -C "$R" checkout -q otra
git -C "$R" rebase main >/dev/null 2>&1 || true
out="$(cd "$R" && bash "$SUT" 2>&1)"; rc=$?
check "a rebase stopped by a conflict is RED" 1 "$rc"
case "$out" in *"INCOMPLETE REBASE"*) e=ok ;; *) e="does not name the rebase: $out" ;; esac
check "and NAMES the operation, not just the total" 1 1 "$e"
case "$out" in *"UNRESOLVED CONFLICTS"*) e=ok ;; *) e="does not list the files" ;; esac
check "and lists the conflicting files" 1 1 "$e"
# Y el filo exacto: los gates de CONTENIDO pasan sobre ese mismo árbol.
if [ -f "$R/f.txt" ] && grep -q '<<<<<<<' "$R/f.txt"; then e=ok; else e="the fixture did not leave markers"; fi
check "the partial tree exists and has markers (real fixture)" 1 1 "$e"
git -C "$R" rebase --abort >/dev/null 2>&1 || true
out="$(cd "$R" && bash "$SUT" 2>&1)"; rc=$?
check "after --abort it is clean again" 0 "$rc"

echo "INCOMPLETE MERGE — uncommitted MERGE_HEAD"
R="$W/merge"; repo_con_conflicto "$R"
git -C "$R" merge otra >/dev/null 2>&1 || true
out="$(cd "$R" && bash "$SUT" 2>&1)"; rc=$?
check "a merge with an unresolved conflict is RED" 1 "$rc"
case "$out" in *MERGE_HEAD*) e=ok ;; *) e="does not name MERGE_HEAD: $out" ;; esac
check "and names MERGE_HEAD" 1 1 "$e"

echo "INCOMPLETE CHERRY-PICK"
R="$W/pick"; repo_con_conflicto "$R"
git -C "$R" cherry-pick otra >/dev/null 2>&1 || true
out="$(cd "$R" && bash "$SUT" 2>&1)"; rc=$?
check "a cherry-pick with a conflict is RED" 1 "$rc"
case "$out" in *CHERRY_PICK_HEAD*) e=ok ;; *) e="does not name CHERRY_PICK_HEAD" ;; esac
check "and names CHERRY_PICK_HEAD" 1 1 "$e"

echo "LINKED WORKTREE — state is NOT in the main clone .git"
R="$W/wt-base"; repo_con_conflicto "$R"
git -C "$R" worktree add -q "$W/wt-linked" otra >/dev/null 2>&1
git -C "$W/wt-linked" rebase main >/dev/null 2>&1 || true
out="$(cd "$W/wt-linked" && bash "$SUT" 2>&1)"; rc=$?
check "an incomplete rebase IN A LINKED WORKTREE is detected" 1 "$rc"
out="$(cd "$R" && bash "$SUT" 2>&1)"; rc=$?
check "and the clean main clone is NOT affected" 0 "$rc"

echo "COULD NOT LOOK — never a pass"
out="$(cd "$W" && bash "$SUT" 2>&1)"; rc=$?
check "outside a git repository returns 2, not 0" 2 "$rc"
FAKE="$W/bin"; mkdir -p "$FAKE"
out="$(cd "$R" && PATH="$FAKE" "$BASH_BIN" "$SUT" 2>&1)"; rc=$?
check "without git in PATH returns 2, not 0" 2 "$rc"

echo
echo "check-mid-operation self-test: $pasa passed, $falla failed"
[ "$falla" -eq 0 ]
