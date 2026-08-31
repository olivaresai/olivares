#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-ci-env-reach.sh — envoltorio fino sobre cmd/olivares/tools/checkcienvreach.
#
# ⛔ POR QUÉ EXISTE EL GATE, y no es teórico. Medido el 2026-08-18 sobre `mainline-ci.yml`:
#
#     race-rest      EJECUTA `task test:cloud`  ·  no fija NINGUNA CLOUD_CP_*
#     control-plane  fija CLOUD_CP_INTEGRATION y CLOUD_CP_REQUIRE_INTEGRATION  ·  no ejecuta esos tests
#
# `GITHUB_ENV` es POR JOB y no cruza. El cableado nunca llegó a los tests para los que se escribió, y
# el backlog declaraba «los 21 TestIntegration* ya no se saltan» siendo falso durante días. Nadie
# mintió: se verificó que la variable ESTABA y se dedujo que los tests CORRÍAN. Son dos hechos
# distintos.
#
# ⭐ POR QUÉ YA NO ES PYTHON, corregido el 2026-08-19. La versión anterior hacía `import yaml`.
# **Ningún contenedor de este proyecto tiene PyYAML, y ninguno tiene `pip` ni `ensurepip`.** El gate
# contestaba `2 · NO HE PODIDO MIRAR` en todos ellos y por tanto **rechazaba TODOS los push de TODOS
# los carriles**, incluido uno de un solo fichero markdown. El 2 era la respuesta CORRECTA —es lo que
# se contesta cuando no se puede mirar— y ahí estaba lo insidioso: el contrato funcionaba y aun así
# el gate era inservible, porque no se podía mirar EN NINGUNA PARTE. Un gate que nadie puede correr
# en verde no protege nada; sólo enseña a usar `--no-verify`.
#
# Go está en todas partes donde corre el gate y la dependencia YAML ya estaba pagada en
# `cmd/olivares` (`gopkg.in/yaml.v3`), que es exactamente lo que hace `checkciports`. El camino feliz
# pasa de ser comprobable por NADIE a serlo por cualquiera — y la matriz de mutación
# (`scripts/test-ci-env-reach-gate.sh`, 8 casos con el feliz dentro) lo comprueba en cada push.
#
# Salida: 0 verde · 1 hallazgo · 2 NO HE PODIDO MIRAR.
set -uo pipefail
ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

# shellcheck source=lib/exec-workdir.sh
# lib/exec-workdir.sh PRUEBA que un candidato puede crear y EJECUTAR: no se elige el directorio, se
# demuestra. En estos contenedores /tmp está montado noexec y execve da 126 allí.
. "$ROOT/scripts/lib/exec-workdir.sh" || {
	echo "check-ci-env-reach: NO HE PODIDO MIRAR: falta scripts/lib/exec-workdir.sh" >&2
	exit 2
}
export CI_ENV_REACH_ROOT="${CI_ENV_REACH_ROOT:-$ROOT}"

BINDIR="$(olivares_pick_exec_workdir gatebin)" || {
	echo "check-ci-env-reach: NO HE PODIDO MIRAR: no puedo crear el directorio del binario" >&2
	exit 2
}
cleanup() { rm -rf "$BINDIR"; }
trap cleanup EXIT HUP INT TERM

# El `cd` va GUARDADO: sin esto `set -e` cortaría con el error crudo del shell y rc 1, es decir, un
# veredicto de ceguera degradado a hallazgo — la misma distinción que este gate defiende.
if ! cd "$ROOT/cmd/olivares" 2>/dev/null; then
	echo "check-ci-env-reach: NO HE PODIDO MIRAR: falta $ROOT/cmd/olivares, así que no puedo" >&2
	echo "check-ci-env-reach: construir la herramienta que juzga." >&2
	exit 2
fi

# ⛔ `go build` y NO `go run`: `go run` COLAPSA el código de salida de la herramienta (imprime
# «exit status 2» y sale 1), y con él la TERCERA RESPUESTA. Medido el 2026-08-15 y documentado en
# check-ci-ports.sh.
if ! go build -o "$BINDIR/checkcienvreach" ./tools/checkcienvreach; then
	echo "check-ci-env-reach: NO HE PODIDO MIRAR: la herramienta no compila" >&2
	exit 2
fi
"$BINDIR/checkcienvreach"
exit $?
