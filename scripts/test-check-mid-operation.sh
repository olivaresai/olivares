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
		printf '  FALLO %-58s rc=%s (esperaba %s) %s\n' "$nombre" "$obtenido" "$esperado" "$extra"; falla=$((falla + 1))
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

echo "LIMPIO — un árbol sin operaciones a medias pasa"
R="$W/limpio"; mkdir -p "$R" && git -C "$R" init -q -b main
printf 'x\n' >"$R/f.txt"; git -C "$R" add f.txt; git -C "$R" commit -q -m "uno"
out="$(cd "$R" && bash "$SUT" 2>&1)"; rc=$?
check "un repo recién commiteado no tiene nada a medias" 0 "$rc"
case "$out" in *"OK — ninguna"*) e=ok ;; *) e="no lo DICE: $out" ;; esac
check "y lo dice, en vez de callarse" 0 0 "$e"

echo "REBASE A MEDIAS — el caso que costó un push parcial a main"
R="$W/rebase"; repo_con_conflicto "$R"
git -C "$R" checkout -q otra
git -C "$R" rebase main >/dev/null 2>&1 || true
out="$(cd "$R" && bash "$SUT" 2>&1)"; rc=$?
check "un rebase detenido en conflicto es ROJO" 1 "$rc"
case "$out" in *"REBASE A MEDIAS"*) e=ok ;; *) e="no nombra el rebase: $out" ;; esac
check "y NOMBRA la operación, no sólo el total" 1 1 "$e"
case "$out" in *"CONFLICTO SIN RESOLVER"*) e=ok ;; *) e="no lista los ficheros" ;; esac
check "y lista los ficheros en conflicto" 1 1 "$e"
# Y el filo exacto: los gates de CONTENIDO pasan sobre ese mismo árbol.
if [ -f "$R/f.txt" ] && grep -q '<<<<<<<' "$R/f.txt"; then e=ok; else e="el fixture no dejó marcadores"; fi
check "el árbol parcial existe y tiene marcadores (fixture real)" 1 1 "$e"
git -C "$R" rebase --abort >/dev/null 2>&1 || true
out="$(cd "$R" && bash "$SUT" 2>&1)"; rc=$?
check "tras --abort vuelve a estar limpio" 0 "$rc"

echo "MERGE A MEDIAS — MERGE_HEAD sin commitear"
R="$W/merge"; repo_con_conflicto "$R"
git -C "$R" merge otra >/dev/null 2>&1 || true
out="$(cd "$R" && bash "$SUT" 2>&1)"; rc=$?
check "un merge con conflicto sin resolver es ROJO" 1 "$rc"
case "$out" in *MERGE_HEAD*) e=ok ;; *) e="no nombra MERGE_HEAD: $out" ;; esac
check "y nombra MERGE_HEAD" 1 1 "$e"

echo "CHERRY-PICK A MEDIAS"
R="$W/pick"; repo_con_conflicto "$R"
git -C "$R" cherry-pick otra >/dev/null 2>&1 || true
out="$(cd "$R" && bash "$SUT" 2>&1)"; rc=$?
check "un cherry-pick con conflicto es ROJO" 1 "$rc"
case "$out" in *CHERRY_PICK_HEAD*) e=ok ;; *) e="no nombra CHERRY_PICK_HEAD" ;; esac
check "y nombra CHERRY_PICK_HEAD" 1 1 "$e"

echo "WORKTREE ENLAZADO — el estado NO vive en el .git del clon principal"
R="$W/wt-base"; repo_con_conflicto "$R"
git -C "$R" worktree add -q "$W/wt-linked" otra >/dev/null 2>&1
git -C "$W/wt-linked" rebase main >/dev/null 2>&1 || true
out="$(cd "$W/wt-linked" && bash "$SUT" 2>&1)"; rc=$?
check "un rebase a medias EN UN WORKTREE ENLAZADO se ve" 1 "$rc"
out="$(cd "$R" && bash "$SUT" 2>&1)"; rc=$?
check "y el clon principal, que está limpio, NO se contamina" 0 "$rc"

echo "NO HE PODIDO MIRAR — nunca un verde"
out="$(cd "$W" && bash "$SUT" 2>&1)"; rc=$?
check "fuera de un repositorio git responde 2, no 0" 2 "$rc"
FAKE="$W/bin"; mkdir -p "$FAKE"
out="$(cd "$R" && PATH="$FAKE" "$BASH_BIN" "$SUT" 2>&1)"; rc=$?
check "sin git en el PATH responde 2, no 0" 2 "$rc"

echo
echo "check-mid-operation self-test: $pasa pasan, $falla fallan"
[ "$falla" -eq 0 ]
