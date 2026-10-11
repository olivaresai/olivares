#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# test-ci-history-depth.sh — bateria de check-ci-history-depth.sh.
#
# ⛔ LOS DOS PRIMEROS CASOS SON EL DEFECTO QUE YA OCURRIO, no casos inventados: el 2026-08-27 el job
#    `control-plane` salio rojo porque `lint:addon-sets` deriva de historia profunda y su checkout
#    era superficial. Un gate se prueba con el fallo que ya paso.
#
# ⛔ Y LA DIRECCION QUE NO DEBE DISPARAR ESTA AQUI A PROPOSITO (caso 3): un guion con historia en un
#    job que SI trae fetch-depth: 0 tiene que salir limpio. Sin ese caso, un gate que dijera
#    siempre «sucio» pasaria la bateria entera.
#
# ⛔ EL DIRECTORIO DEL SENUELO SE LLAMA `g/` Y NO `scripts/`, Y NO ES CAPRICHO. Con `$d/scripts/…`
#    el senuelo quedaba escrito con el prefijo del directorio real de guiones, y `lint:export-closure` lo lee
#    como una dependencia del repositorio que no existe: me rechazo el push con
#    «1 reference(s) leave the published tree … dangling». El gate acertaba —un fichero de
#    `scripts/` nombrado y ausente es exactamente lo que vigila—, asi que la respuesta correcta es
#    quitarle el falso parecido al senuelo, no declarar una excepcion.
set -uo pipefail

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)" || exit 2
GATE="$ROOT/scripts/check-ci-history-depth.sh"
[ -x "$GATE" ] || { echo "test-ci-history-depth: ⛔ COULD NOT LOOK: cannot find $GATE" >&2; exit 2; }

PASS=0; FAIL=0
check() { # <nombre> <rc esperado> <rc real>
	if [ "$2" = "$3" ]; then PASS=$((PASS+1)); printf 'ok   %-56s rc=%s\n' "$1" "$3"
	else FAIL=$((FAIL+1)); printf 'FAIL %-56s expected rc=%s, got rc=%s\n' "$1" "$2" "$3"; fi
}

TMP="$(mktemp -d "${TMPDIR:-/tmp}/cihd.XXXXXX")" || exit 2
trap 'rm -rf "$TMP"' EXIT

# fixture: <dir> <fetch-depth o vacio> <guion con historia: si|no>
fixture() { # -> imprime el dir
	local d="$TMP/$1"; local depth="$2"; local deep="$3"
	rm -rf "$d"; mkdir -p "$d/g" "$d/wf"
	if [ "$deep" = "si" ]; then
		printf '#!/usr/bin/env bash\ngit log --follow --diff-filter=A --format=%%%%P -- README.md\n' > "$d/g/check-hondo.sh"
	elif [ "$deep" = "rango" ]; then
		# ⛔ ESTA LINEA ES LITERALMENTE LA DE check-int-12-no-land.sh:115, y por eso vale como
		#    senuelo: el patron viejo no la veia, asi que quitarle `fetch-depth: 0` a su job
		#    dejaba el gate en CLEAN. Un rango de dos puntos entre dos objetos NO existe en un
		#    clon superficial; el conteo falla y el guion contesta 2, no 1.
		printf '#!/usr/bin/env bash\n: python3 - <<%s\nrc, out, _ = git("rev-list", "--count", f"{pin58}..{ovl_pin}", cwd=hub_dir)\nPY\n' "PY" > "$d/g/check-hondo.sh"
	else
		printf '#!/usr/bin/env bash\necho superficial\n' > "$d/g/check-hondo.sh"
	fi
	printf 'version: "3"\ntasks:\n  lint:hondo:\n    cmds:\n      - bash g/check-hondo.sh\n' > "$d/Taskfile.yml"
	{
		printf 'name: t\non: [push]\njobs:\n  j1:\n    runs-on: ubuntu-latest\n    steps:\n'
		printf '      - uses: actions/checkout@v4\n        with:\n          persist-credentials: false\n'
		[ -n "$depth" ] && printf '          fetch-depth: %s\n' "$depth"
		printf '      - run: task lint:hondo\n'
	} > "$d/wf/ci.yml"
	printf '%s' "$d"
}

corre() { # <dir> -> rc
	( cd "$ROOT" && OLIVARES_CIHD_TASKFILE="$1/Taskfile.yml" OLIVARES_CIHD_WFDIR="$1/wf" \
		OLIVARES_CIHD_SCRIPTS="$1/g" bash "$GATE" >/dev/null 2>&1 ); echo $?
}

# 1 · EL DEFECTO QUE YA OCURRIO: historia profunda + checkout sin fetch-depth
d="$(fixture caso1 "" si)";   check "(1) script requiring history in a job WITHOUT fetch-depth" 1 "$(corre "$d")"
# 2 · el mismo, pero con una profundidad que NO es 0 — «tiene fetch-depth» no basta
d="$(fixture caso2 "1" si)";  check "(2) fetch-depth: 1 is insufficient; only 0 fetches history" 1 "$(corre "$d")"
# 3 · LA DIRECCION QUE NO DISPARA: con fetch-depth: 0 sale limpio
d="$(fixture caso3 "0" si)";  check "(3) with fetch-depth: 0 the gate does NOT flag" 0 "$(corre "$d")"
# 4 · sin guiones de historia: NO HE PODIDO MIRAR, no «limpio» por vacuidad
d="$(fixture caso4 "" no)";   check "(4) zero scripts requiring history -> could not look" 2 "$(corre "$d")"
# 5 · Taskfile ilegible -> no he podido mirar
d="$(fixture caso5 "0" si)"; rm -f "$d/Taskfile.yml"
                              check "(5) missing Taskfile -> could not look" 2 "$(corre "$d")"
# 6 · workflows ausentes -> no he podido mirar
d="$(fixture caso6 "0" si)"; rm -rf "$d/wf"
                              check "(6) missing workflow directory -> could not look" 2 "$(corre "$d")"
# 7 · YAML roto -> no he podido mirar, NUNCA limpio
d="$(fixture caso7 "0" si)"; printf 'jobs:\n  j1:\n   steps:\n  - - :\n' > "$d/wf/ci.yml"
                              check "(7) unparseable workflow -> could not look" 2 "$(corre "$d")"
# 8 · ningun job corre la tarea: el cruce no midio nada -> no he podido mirar
d="$(fixture caso8 "" si)"; printf 'name: t\non: [push]\njobs:\n  j1:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo nada\n' > "$d/wf/ci.yml"
                              check "(8) no job runs the task -> could not look" 2 "$(corre "$d")"
# 10 · LA FORMA QUE SOBREVIVIO AL PATRON VIEJO (a repository gate, 2026-08-29): un RANGO `A..B` de rev-list
#      en un job sin fetch-depth. Antes de ensanchar el patron este caso daba 2 —«cero guiones con
#      derivacion historica»— y no 1: es decir, el gate ni siquiera veia el guion.
d="$(fixture caso10 "" rango)"; check "(10) rev-list A..B in a job WITHOUT fetch-depth" 1 "$(corre "$d")"
# 11 · y su direccion de NO DISPARO, sin la cual un patron que acusara a todo pasaria el (10)
d="$(fixture caso11 "0" rango)"; check "(11) rev-list A..B with fetch-depth: 0 is NOT flagged" 0 "$(corre "$d")"
# 9 · el arbol REAL de este repositorio tiene que estar limpio
( cd "$ROOT" && bash "$GATE" >/dev/null 2>&1 ); check "(9) the real repository is clean" 0 "$?"


# Exercise the actual workflow guard, including history that static task discovery
# cannot follow through a dispatcher. The repositories are local fixtures.
GUARD="$TMP/checkout-history.sh"
if python3 - "$ROOT/.github/workflows/mainline-ci.yml" "$GUARD" <<'PY_GUARD'
import pathlib
import sys
import yaml

steps = yaml.safe_load(pathlib.Path(sys.argv[1]).read_text())["jobs"]["control-plane"]["steps"]
found = [i for i, step in enumerate(steps) if step.get("id") == "checkout-history"]
if len(found) != 1 or found[0] == 0:
    raise SystemExit("expected one checkout-history guard after checkout")
i = found[0]
checkout, guard = steps[i - 1], steps[i]
if not checkout.get("uses", "").startswith("actions/checkout@"):
    raise SystemExit("history guard must immediately follow checkout")
if checkout.get("with", {}).get("fetch-depth") != 0:
    raise SystemExit("history guard requires checkout fetch-depth 0")
if "if" in guard or guard.get("continue-on-error", False):
    raise SystemExit("history guard must run and fail the job on refusal")
body = guard.get("run")
if not isinstance(body, str) or not body.strip():
    raise SystemExit("history guard has no executable body")
pathlib.Path(sys.argv[2]).write_text(body)
PY_GUARD
then
    check "(12) runtime guard immediately follows full checkout" 0 0
    FULL="$TMP/full"
    SHALLOW="$TMP/shallow"
    OUTSIDE="$TMP/outside"
    FAKEBIN="$TMP/bin"
    mkdir -p "$OUTSIDE" "$FAKEBIN"
    git init -q "$FULL" &&
        git -C "$FULL" -c core.hooksPath=/dev/null -c user.name=Fixture \
            -c user.email=fixture@example.invalid commit -q --allow-empty -m first &&
        git -C "$FULL" -c core.hooksPath=/dev/null -c user.name=Fixture \
            -c user.email=fixture@example.invalid commit -q --allow-empty -m second &&
        git clone -q --depth 1 "file://$FULL" "$SHALLOW" || exit 2
    check "(13) shallow fixture is actually shallow" true \
        "$(git -C "$SHALLOW" rev-parse --is-shallow-repository)"
    ( cd "$FULL" && bash "$GUARD" >"$TMP/full.log" 2>&1 );
    check "(14) runtime guard accepts a complete repository" 0 "$?"
    ( cd "$SHALLOW" && bash "$GUARD" >"$TMP/shallow.log" 2>&1 );
    check "(15) runtime guard refuses a real shallow clone" 2 "$?"
    ( cd "$OUTSIDE" && GIT_CEILING_DIRECTORIES="$TMP" bash "$GUARD" >"$TMP/outside.log" 2>&1 );
    check "(16) runtime guard refuses a directory outside Git" 2 "$?"
    printf '#!/bin/sh\nprintf "false\\n"\nexit 1\n' > "$FAKEBIN/git"
    chmod +x "$FAKEBIN/git"
    ( cd "$FULL" && PATH="$FAKEBIN:$PATH" bash "$GUARD" >"$TMP/git-failed.log" 2>&1 );
    check "(17) failed Git with false output does not grant" 2 "$?"
    printf '#!/bin/sh\nprintf "unknown\\n"\n' > "$FAKEBIN/git"
    ( cd "$FULL" && PATH="$FAKEBIN:$PATH" bash "$GUARD" >"$TMP/git-unknown.log" 2>&1 );
    check "(18) successful Git with unknown output refuses" 2 "$?"
else
    check "(12) runtime guard immediately follows full checkout" 0 1
fi

echo
echo "check-ci-history-depth selftest: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
