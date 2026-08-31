#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Bateria de scripts/check-taskfile-executable.sh.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
SUT="$HERE/check-taskfile-executable.sh"
PASS=0; FAIL=0; START=$SECONDS
TMPROOT=$(mktemp -d "${TMPDIR:-/tmp}/tfexec-bat.XXXXXX") || exit 2
trap 'rm -rf "$TMPROOT"' EXIT

AMB_ROOT=$(git -C "$HERE" rev-parse --show-toplevel 2>/dev/null || true)
[ -n "$AMB_ROOT" ] && AMB_HEAD=$(git -C "$AMB_ROOT" rev-parse HEAD 2>/dev/null || echo NONE)

check() { # check <label> <verdicto> <rc> [subcadena-en-stderr]
	local label="$1" wv="$2" wr="$3" must="${4:-}" ok=1
	[ "$VERD" = "$wv" ] || ok=0
	[ "$RC" -eq "$wr" ] || ok=0
	if [ -n "$must" ]; then case "$ERRS" in *"$must"*) ;; *) ok=0 ;; esac; fi
	if [ "$ok" -eq 1 ]; then
		PASS=$((PASS + 1)); printf 'ok   %-56s %s\n' "$label" "$VERD"
	else
		FAIL=$((FAIL + 1))
		printf 'FAIL %-56s got=%s/rc=%s want=%s/rc=%s\n' "$label" "${VERD:-<vacio>}" "$RC" "$wv" "$wr"
		[ -n "$must" ] && printf '     esperaba en stderr: %s\n' "$must"
		printf '     stdout: %s\n' "$(printf '%s' "$SALIDA" | head -1 | cut -c1-130)"
	fi
}

corre() { # corre <repo>
	# El guion hace `cd` a la raiz de SU repo, asi que se COPIA dentro de la fixture con su
	# libreria: apuntarlo desde fuera lo haria leer el arbol de esta sesion y el caso probaria otra
	# cosa. Me paso con la bateria hermana y salieron seis casos rojos por el motivo equivocado.
	mkdir -p "$1/scripts/lib"
	cp -- "$SUT" "$1/scripts/check-taskfile-executable.sh"
	cp -- "$HERE/lib/git-env.sh" "$1/scripts/lib/git-env.sh" 2>/dev/null || :
	SALIDA=$( cd "$1" && OLIVARES_TASKFILE_BASE=base bash scripts/check-taskfile-executable.sh 2>/dev/null ); RC=$?
	ERRS=$( cd "$1" && OLIVARES_TASKFILE_BASE=base bash scripts/check-taskfile-executable.sh 2>&1 >/dev/null )
	VERD="${SALIDA%%	*}"; VERD="${VERD%%$'\n'*}"
}

repo() { # repo <taskfile-base> ; deja la base en el tag `base`
	local d; d=$(mktemp -d "$TMPROOT/r.XXXXXX") || return 1
	git init -q "$d"
	git -C "$d" config user.email 'bateria@example.invalid'
	git -C "$d" config user.name 'bateria'
	printf '%s' "$1" > "$d/Taskfile.yml"
	git -C "$d" add Taskfile.yml >/dev/null
	git -C "$d" -c core.hooksPath=/dev/null commit -qm base
	git -C "$d" tag base
	printf '%s' "$d"
}

encima() { printf '%s' "$2" > "$1/Taskfile.yml"; git -C "$1" add Taskfile.yml >/dev/null; git -C "$1" -c core.hooksPath=/dev/null commit -qm cambio; }

SANO='version: "3"

tasks:
  a:
    desc: a
    cmds:
      - echo a

  z:
    desc: z
    cmds:
      - echo z
'

# 1 — nada cambia: limpio.
d=$(repo "$SANO"); corre "$d"; check "un Taskfile sano es limpio" clean 0

# 2 — la rama ROMPE una tarea (pierde su cmds): tiene que salir finding y NOMBRARLA.
d=$(repo "$SANO")
encima "$d" 'version: "3"

tasks:
  a:
    desc: a

  z:
    desc: z
    cmds:
      - echo z
'
corre "$d"; check "una tarea que pierde su cmds es finding" finding 1 "· a"

# 3 — MUTANTE del 2, y es el que evita el falso positivo que casi me hace NO escribir esto: una
#     tarea con solo `deps:` es LEGITIMA y no se marca. Exigir `cmds:` a secas la marcaria.
d=$(repo "$SANO")
encima "$d" 'version: "3"

tasks:
  a:
    desc: a
    deps: [z]

  z:
    desc: z
    cmds:
      - echo z
'
corre "$d"; check "MUTANTE: solo deps: es legitimo, no se marca" clean 0

# 4 — LA LINEA BASE, que es lo que separa un control de un bloqueo: si la base YA la traia rota, no
#     es de este push. `main` arrastraba 16 el dia que esto se escribio.
ROTO='version: "3"

tasks:
  a:
    desc: a

  z:
    desc: z
    cmds:
      - echo z
'
d=$(repo "$ROTO")
encima "$d" "$ROTO"'
  nueva:
    desc: n
    cmds:
      - echo n
'
corre "$d"; check "lo HEREDADO de la base no se cobra a este push" clean 0

# 5 — MUTANTE del 4: sobre esa MISMA base rota, romper una MAS si se cobra. Sin este caso, el 4
#     pasaria igual con el gate desactivado del todo.
d=$(repo "$ROTO")
encima "$d" 'version: "3"

tasks:
  a:
    desc: a

  z:
    desc: z
'
corre "$d"; check "MUTANTE: sobre base rota, una NUEVA rota si se cobra" finding 1 "· z"

# 6 — CANNOT LOOK: base inexistente. Sin base no se puede separar lo nuevo de lo heredado, y
#     «no puedo comparar» no es «esta limpio».
d=$(repo "$SANO")
corre "$d"   # ⚠ primero, para que el guion ESTE en la fixture: sin esto salia rc=127 y el caso
             #    fallaba por «no encuentro el guion», no por la base inexistente.
SALIDA=$( cd "$d" && OLIVARES_TASKFILE_BASE=no-existe bash scripts/check-taskfile-executable.sh 2>/dev/null ); RC=$?
ERRS=""; VERD="${SALIDA%%	*}"
check "una base inexistente es CANNOT LOOK, no limpio" unreadable 2

# 7 — CANNOT LOOK: gramatica que no encaja. Un fichero donde no reconoce NINGUNA tarea no puede
#     salir «limpio»: seria un cero por no haber mirado.
d=$(repo 'version: "3"
tasks: {}
')
corre "$d"; check "sin reconocer ninguna tarea es CANNOT LOOK" unreadable 2

# 8 — el mensaje EXPLICA las dos formas, porque el operador lee la linea de fallo y no este fichero.
d=$(repo "$SANO")
encima "$d" 'version: "3"

tasks:
  a:
    desc: a

  z:
    desc: z
    cmds:
      - echo z
'
corre "$d"; check "el mensaje nombra las dos formas (127 y el cero mudo)" finding 1 "sale 0"

if [ -n "${AMB_ROOT:-}" ]; then
	now=$(git -C "$AMB_ROOT" rev-parse HEAD 2>/dev/null || echo NONE)
	if [ "$now" = "$AMB_HEAD" ]; then
		PASS=$((PASS + 1)); printf 'ok   %-56s %s\n' "el repositorio anfitrion queda intacto" "HEAD"
	else
		FAIL=$((FAIL + 1)); printf 'FAIL %-56s %s\n' "host repo untouched: HEAD moved" "$AMB_HEAD -> $now"
	fi
fi

printf '\ncheck-taskfile-executable self-test: %d pasan, %d fallan, %ds\n' "$PASS" "$FAIL" "$((SECONDS - START))"
[ "$FAIL" -eq 0 ] || exit 1
