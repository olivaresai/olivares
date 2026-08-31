#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# test-ci-env-reach-gate.sh — la matriz de mutación de check-ci-env-reach.
#
# ⛔ POR QUÉ, y es la regla de la casa escrita en lint:ci-ports: «un gate que sólo se ha visto pasar
# nunca se ha visto funcionar». Este gate en concreto lo necesita más que otros, porque acaba de ser
# PORTADO de python a Go (2026-08-19) y un port se juzga por lo que sigue cazando, no por compilar.
#
# Cada caso monta un árbol de juguete completo (fuentes + workflow) y exige un rc EXACTO. El caso
# feliz está incluido a propósito: sin él, un gate que devolviera 1 siempre pasaría la matriz entera.
set -uo pipefail
ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

if ! cd "$ROOT/cmd/olivares" 2>/dev/null; then
	echo "test-ci-env-reach: NO HE PODIDO MIRAR: falta $ROOT/cmd/olivares" >&2
	exit 2
fi

# El binario NO puede vivir en /tmp: en estos contenedores está montado noexec (execve da 126).
WORK="${TMPDIR:-$ROOT/.tmp}/cienvreach-matrix.$$"
mkdir -p "$WORK" || { echo "test-ci-env-reach: NO HE PODIDO MIRAR: no puedo crear $WORK" >&2; exit 2; }
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

if ! go build -o "$WORK/checkcienvreach" ./tools/checkcienvreach; then
	echo "test-ci-env-reach: NO HE PODIDO MIRAR: la herramienta no compila" >&2
	exit 2
fi
BIN="$WORK/checkcienvreach"

fallos=0
casos=0

# fixture <dir> <fuentes-si|no> <cuerpo-del-workflow>
fixture() {
	local d="$1" fuentes="$2" wf="$3"
	mkdir -p "$d/.github/workflows" "$d/cloud/control-plane"
	if [ "$fuentes" = si ]; then
		{
			printf 'package cp\n\nimport "os"\n\n'
			printf 'func a() string { return os.Getenv("CLOUD_CP_INTEGRATION") }\n'
			printf 'func b() string { return os.Getenv("CLOUD_CP_REQUIRE_INTEGRATION") }\n'
			printf 'func c() string { return os.Getenv("CLOUD_CP_TEST_DSN") }\n'
			printf 'func d() string { return os.Getenv("CLOUD_CP_INTEGRATION_CHILD") }\n'
		} > "$d/cloud/control-plane/env.go"
	fi
	printf '%s\n' "$wf" > "$d/.github/workflows/mainline-ci.yml"
}

corre() { # corre <nombre> <esperado> <dir>
	local nombre="$1" esperado="$2" dir="$3" rc
	casos=$((casos + 1))
	CI_ENV_REACH_ROOT="$dir" "$BIN" >"$WORK/out" 2>&1
	rc=$?
	if [ "$rc" = "$esperado" ]; then
		printf 'ok    %-46s rc=%s\n' "$nombre" "$rc"
	else
		printf 'FALLO %-46s rc=%s (esperado %s)\n' "$nombre" "$rc" "$esperado"
		sed 's/^/        /' "$WORK/out"
		fallos=$((fallos + 1))
	fi
}

WF_BUENO='name: ci
jobs:
  control-plane:
    runs-on: ubuntu-latest
    env:
      CLOUD_CP_INTEGRATION: "1"
      CLOUD_CP_REQUIRE_INTEGRATION: "1"
    steps:
      - run: task test:cloud
  race-rest:
    runs-on: ubuntu-latest
    steps:
      - run: task test:cloud
  web:
    runs-on: ubuntu-latest
    steps:
      - run: echo web'

# EL DEFECTO REAL DE 2026-08-18: las variables las fija un job que NO ejecuta la suite.
WF_SEPARADO='name: ci
jobs:
  control-plane:
    runs-on: ubuntu-latest
    env:
      CLOUD_CP_INTEGRATION: "1"
      CLOUD_CP_REQUIRE_INTEGRATION: "1"
    steps:
      - run: echo solo compila
  race-rest:
    runs-on: ubuntu-latest
    steps:
      - run: task test:cloud
  web:
    runs-on: ubuntu-latest
    steps:
      - run: echo web'

# Sólo UNA de las dos que encienden: media dosis NO enciende la suite.
WF_MEDIO='name: ci
jobs:
  control-plane:
    runs-on: ubuntu-latest
    env:
      CLOUD_CP_INTEGRATION: "1"
    steps:
      - run: task test:cloud
  race-rest:
    runs-on: ubuntu-latest
    steps:
      - run: echo nada
  web:
    runs-on: ubuntu-latest
    steps:
      - run: echo web'

WF_SIN_SUITE='name: ci
jobs:
  a:
    steps:
      - run: echo a
  b:
    steps:
      - run: echo b
  c:
    steps:
      - run: echo c'

WF_POCOS='name: ci
jobs:
  a:
    steps:
      - run: task test:cloud'

fixture "$WORK/f-ok"        si "$WF_BUENO";     corre "camino feliz"                            0 "$WORK/f-ok"
fixture "$WORK/f-sep"       si "$WF_SEPARADO";  corre "las fija un job que NO ejecuta"          1 "$WORK/f-sep"
fixture "$WORK/f-medio"     si "$WF_MEDIO";     corre "sólo una de las dos que encienden"       1 "$WORK/f-medio"
fixture "$WORK/f-sinsuite"  si "$WF_SIN_SUITE"; corre "nadie ejecuta test:cloud"                2 "$WORK/f-sinsuite"
fixture "$WORK/f-pocos"     si "$WF_POCOS";     corre "menos de 3 jobs"                         2 "$WORK/f-pocos"
fixture "$WORK/f-sinfuente" no "$WF_BUENO";     corre "sin fuentes: censo bajo el suelo"        2 "$WORK/f-sinfuente"

fixture "$WORK/f-roto"      si "$WF_BUENO"
printf 'jobs:\n  a: [b\n   c\n' > "$WORK/f-roto/.github/workflows/mainline-ci.yml"
corre "el workflow no parsea" 2 "$WORK/f-roto"

mkdir -p "$WORK/f-vacio"
corre "no hay workflow que mirar" 2 "$WORK/f-vacio"

echo
if [ "$fallos" -ne 0 ]; then
	echo "test-ci-env-reach: $fallos de $casos casos FALLAN" >&2
	exit 1
fi
echo "test-ci-env-reach: $casos/$casos — el gate se pone rojo por cada defecto y verde por el camino feliz"
