#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# Bateria de check-web-bundle-freshness.sh, sobre un clon de laboratorio. No toca el clon de verdad.
#
# ⛔ POR QUE NACE. Este sujeto LEE `OLIVARES_PUSH_REFS_FILE` —que el gancho EXPORTA— para saber QUE
# RANGO de commits mirar, y hasta hoy no tenia banco. Censando `scripts/check-*.sh` por «¿lee alguna
# de las siete variables del gancho?» salieron seis sujetos: cuatro con banco, y este y
# `check-unpublished-work.sh` sin ninguno.
#
# El uso es deliberado y correcto; lo que no habia era nada que fijara el DIFERENCIAL. Y ese
# diferencial es todo el punto: la variable decide el rango, o sea que decide QUE mira el gate.
# Sin un caso que exija que los dos pases difieran, una regresion que dejara de leer el fichero
# pasaria en verde — el gate seguiria contestando, pero sobre otro rango.
set -uo pipefail
export LC_ALL=C
# El banco no puede heredar la variable que redirige a su sujeto (a repository gate).
unset OLIVARES_PUSH_REFS_FILE

# ⛔ AISLAMIENTO DEL ENTORNO GIT. Este banco monta clones con `mktemp -d` y corre `git` dentro: con
# las GIT_* del entorno heredadas, esos `git` pueden resolver contra OTRO repositorio sin decirlo —
# y aqui eso significaria medir el clon de verdad creyendo medir el laboratorio. Lo exige
# `lint:git-env` para toda pata que junte `mktemp -d` con git, y la exigencia es correcta.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

RAIZ="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
SUT="$RAIZ/scripts/check-web-bundle-freshness.sh"
BASE="$(mktemp -d "${TMPDIR:-/tmp}/wbf.XXXXXX")" || exit 2
trap 'rm -rf "$BASE"' EXIT INT TERM

pasados=0; fallados=0
check() { # etiqueta esperado obtenido
	if [ "$2" = "$3" ]; then
		printf '  ok   %-56s %s\n' "$1" "$3"; pasados=$((pasados + 1))
	else
		printf '  FAIL %-56s esperado=%s obtenido=%s\n' "$1" "$2" "$3"; fallados=$((fallados + 1))
	fi
}
g() { git -c user.email=b@b -c user.name=b -c commit.gpgsign=false "$@"; }

# Un clon con `web/` y el `dist` empotrado, y un `origin/main` de verdad para la base comun.
CLON="$BASE/clon"; REMOTO="$BASE/remoto.git"
git init -q --bare "$REMOTO"
git init -q -b main "$CLON"
mkdir -p "$CLON/web/src" "$CLON/core/internal/webui/dist" "$CLON/scripts"
printf 'export const a = 1;\n' >"$CLON/web/src/a.ts"
printf 'bundle viejo\n' >"$CLON/core/internal/webui/dist/app.js"
g -C "$CLON" add -A >/dev/null; g -C "$CLON" commit -q -m base
g -C "$CLON" remote add origin "$REMOTO"; g -C "$CLON" push -q origin main 2>/dev/null
g -C "$CLON" fetch -q origin 2>/dev/null
BASE_SHA="$(git -C "$CLON" rev-parse HEAD)"
# Una punta que mueve FUENTE y no toca el bundle: es justo lo que este gate existe para cazar.
printf 'export const a = 2;\n' >"$CLON/web/src/a.ts"
g -C "$CLON" add -A >/dev/null; g -C "$CLON" commit -q -m "mueve fuente sin reconstruir"
TIP="$(git -C "$CLON" rev-parse HEAD)"

corre() { # corre [VAR=val ...] -> rc; salida en $BASE/out
	env OLIVARES_CLONE="$CLON" "$@" bash "$SUT" >"$BASE/out" 2>&1
	echo $?
}

# ─────────── (1) SIN la variable: el rango es base..HEAD y el bundle se queda atras ─────────────
rc=$(corre)
check "(1) sin el entorno del gancho, caza el bundle atrasado" 1 "$rc"

# ─── (2) CON la variable declarando un rango VACIO (la punta ya esta en el remoto declarado) ────
REFS="$BASE/refs.txt"
printf 'refs/heads/main %s refs/heads/main %s\n' "$TIP" "$TIP" >"$REFS"
rc_g=$(corre OLIVARES_PUSH_REFS_FILE="$REFS")
check "(2) con un rango vacio declarado, no hay nada que mirar" 0 "$rc_g"

# ⛔ EL DIFERENCIAL, caso obligatorio: la variable decide QUE RANGO mira el gate, asi que los dos
# pases tienen que dar distinto. Si dieran igual, la lectura no estaria haciendo nada y una
# regresion que la quitara pasaria en verde — el gate seguiria contestando, pero sobre otro rango.
check "(D) el diferencial limpio/gancho EXISTE" "1/0" "$rc/$rc_g"

# ─────────────────── (3) la variable puesta y el fichero ILEGIBLE: 2, nunca adivinar ────────────
rc=$(corre OLIVARES_PUSH_REFS_FILE="$BASE/no-existe.txt")
check "(3) fichero de refs ilegible -> 2" 2 "$rc"
grep -q 'NO HE PODIDO MIRAR' "$BASE/out" && d=si || d=no
check "(3) y lo dice como no he podido mirar" si "$d"

# ─────────────────────────────────── el mutante del lector ─────────────────────────────────────
# ⛔ El mutante necesita su `lib/` AL LADO: el sujeto carga `lib/git-env.sh` relativo a SU ruta, y
# una copia suelta muere en ese FATAL. Ese rojo seria por la ubicacion, no por lo que se le quito —
# un mutante que muere por otra causa no prueba nada.
mkdir -p "$BASE/lib"
cp "$RAIZ/scripts/lib/git-env.sh" "$BASE/lib/" 2>/dev/null
MUT="$BASE/mut.sh"
python3 - "$SUT" "$MUT" <<'MUTPY'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
v = 'if [ -n "${OLIVARES_PUSH_REFS_FILE:-}" ]; then'
if v not in s:
    sys.exit(3)
open(sys.argv[2], "w", encoding="utf-8").write(s.replace(v, 'if false; then', 1))
MUTPY
[ -s "$MUT" ] || { echo "  FAIL MUTANTE NO ESCRITO"; fallados=$((fallados + 1)); }
cmp -s "$SUT" "$MUT" && d=NO-DIFIERE || d=ok
check "(M) el mutante REALMENTE difiere" ok "$d"
rcm=$(env OLIVARES_CLONE="$CLON" OLIVARES_PUSH_REFS_FILE="$REFS" bash "$MUT" >"$BASE/out.mut" 2>&1; echo $?)
check "(M) sin leer los refs, vuelve a mirar OTRO rango" 1 "$rcm"

echo "check-web-bundle-freshness: $pasados passed, $fallados failed"
[ "$fallados" -eq 0 ] || exit 1
exit 0
