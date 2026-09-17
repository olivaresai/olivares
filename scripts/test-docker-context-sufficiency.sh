#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for check-docker-context-sufficiency.sh, by MUTATION in both directions.
#
# The half that matters is the CATCHING one, and its reference case is not invented: it is the real
# Dockerfile.fips as it stood on 2026-09-01, a source build wired to a `dockers:` entry. That file
# made the release for v26.8.0 die at `COPY core/internal/webui/dist/index.html`. If this battery's
# case 1 ever goes green, the gate has stopped seeing the very defect it was written for.
#
# The other half — not-catching — is what keeps the gate from being a nuisance that people disable:
# a Dockerfile that legitimately COPYs something listed in extra_files must stay CLEAN, and so must
# a dual-mode file whose source stages are pruned by its build-arg.
#
# Each case builds a THROWAWAY git repo, because the subject derives its corpus from git.
set -uo pipefail

# ⛔ EL ENTORNO GIT AMBIENTAL GANA AL `cd`, y aqui eso escribiria en el repositorio VIVO.
#
# Cada caso hace `git init` y `git add` dentro de un `mktemp -d`. Un `GIT_DIR` heredado manda
# sobre el directorio de trabajo, y git lo exporta a todo hook `pre-push` — o sea que esta
# bateria, corrida DESDE EL GANCHO (que es donde su gate la corre), operaria sobre el arbol
# real en vez de sobre su caja de arena. No es teorico en esta casa: el mismo descuido dejo
# 21 commits en el repositorio equivocado.
#
# Lo cazo `lint:git-env` en el primer portON que atraveso este fichero, con el nombre y la
# razon: «pairs 'mktemp -d' with git, does not source lib/git-env.sh».
#
# Fail-closed: un saneador que no se puede cargar es «no he podido aislar», nunca «no hacia falta».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "test-docker-context-sufficiency: FATAL: no puedo cargar $_olivares_git_env (aislamiento git-env)" >&2
	exit 2
}
unset _olivares_git_env

export TMPDIR="${TMPDIR:-/workspace/.kernel-tmpdir}"
SUBJ="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-docker-context-sufficiency.sh"
pass=0; fail=0

caso() { # caso <nombre> <rc-esperado> <cuerpo-que-escribe-los-ficheros>
	nombre="$1"; esperado="$2"; cuerpo="$3"
	d="$(mktemp -d)" || { echo "no puedo crear temp"; exit 2; }
	( cd "$d" && git init -q . && eval "$cuerpo" && git add -A 2>/dev/null
	  bash "$SUBJ" >"$d/out.txt" 2>&1 ) ; rc=$?
	if [ "$rc" = "$esperado" ]; then
		pass=$((pass+1)); printf '  ok   %-46s rc=%s\n' "$nombre" "$rc"
	else
		fail=$((fail+1)); printf '  FAIL %-46s rc=%s (esperado %s)\n' "$nombre" "$rc" "$esperado"
		sed 's/^/         /' "$d/out.txt" | head -6
	fi
	rm -rf "$d"
}

# --- 1 · EL CASO REAL: source-build wired to a dockers entry. DEBE cazarlo. ---------------
caso "source-build sin ARG (el fallo de v26.8.0)" 1 '
cat > .goreleaser.yaml <<Y
builds:
  - id: olivares-fips
    binary: olivares-fips
dockers:
  - id: fips-image
    ids: [olivares-fips]
    dockerfile: Dockerfile.fips
    extra_files: [LICENSE]
Y
cat > Dockerfile.fips <<D
FROM node:26 AS web
COPY web/package.json ./web/
FROM golang:1.26 AS build
COPY go.work go.work.sum ./
COPY . .
FROM scratch
COPY --from=build /out/olivares /usr/local/bin/olivares
COPY LICENSE /usr/share/doc/
D
touch LICENSE'

# --- 2 · el mismo fichero en modo dual: el ARG poda las stages de fuente. LIMPIO. ---------
caso "modo dual con build-arg prebuilt" 0 '
cat > .goreleaser.yaml <<Y
builds:
  - id: olivares-fips
    binary: olivares-fips
dockers:
  - id: fips-image
    ids: [olivares-fips]
    dockerfile: Dockerfile.fips
    extra_files: [LICENSE]
    build_flag_templates:
      - "--build-arg=BIN_SOURCE=prebuilt"
Y
cat > Dockerfile.fips <<D
ARG BIN_SOURCE=source
FROM node:26 AS web
COPY web/package.json ./web/
FROM golang:1.26 AS build
COPY . .
FROM scratch AS bin-prebuilt
COPY olivares-fips /out/olivares
FROM build AS bin-source
FROM bin-\${BIN_SOURCE} AS bin
FROM scratch
COPY --from=bin /out/olivares /usr/local/bin/olivares
COPY LICENSE /usr/share/doc/
D
touch LICENSE'

# --- 3 · MUTANTE: el mismo fichero dual SIN el build-arg -> vuelve el defecto. ------------
# Es la mitad que prueba que el verde del caso 2 lo produce el ARG y no la forma del fichero.
caso "dual pero SIN el build-arg (el verde debe irse)" 1 '
cat > .goreleaser.yaml <<Y
builds:
  - id: olivares-fips
    binary: olivares-fips
dockers:
  - id: fips-image
    ids: [olivares-fips]
    dockerfile: Dockerfile.fips
    extra_files: [LICENSE]
Y
cat > Dockerfile.fips <<D
ARG BIN_SOURCE=source
FROM node:26 AS web
COPY web/package.json ./web/
FROM golang:1.26 AS build
COPY . .
FROM scratch AS bin-prebuilt
COPY olivares-fips /out/olivares
FROM build AS bin-source
FROM bin-\${BIN_SOURCE} AS bin
FROM scratch
COPY --from=bin /out/olivares /usr/local/bin/olivares
COPY LICENSE /usr/share/doc/
D
touch LICENSE'

# --- 4 · MUTANTE: build-arg que no nombra ninguna stage. ----------------------------------
caso "build-arg apunta a una stage inexistente" 1 '
cat > .goreleaser.yaml <<Y
builds:
  - id: olivares-fips
    binary: olivares-fips
dockers:
  - id: fips-image
    ids: [olivares-fips]
    dockerfile: Dockerfile.fips
    extra_files: [LICENSE]
    build_flag_templates:
      - "--build-arg=BIN_SOURCE=bogus"
Y
cat > Dockerfile.fips <<D
ARG BIN_SOURCE=source
FROM scratch AS bin-prebuilt
COPY olivares-fips /out/olivares
FROM bin-\${BIN_SOURCE} AS bin
FROM scratch
COPY --from=bin /out/olivares /usr/local/bin/olivares
COPY LICENSE /usr/share/doc/
D
touch LICENSE'

# --- 5 · NO-CAZAR: todo lo que se copia esta en extra_files. Debe quedar LIMPIO. ----------
caso "todo cubierto por extra_files (no debe saltar)" 0 '
cat > .goreleaser.yaml <<Y
builds:
  - id: olivares
    binary: olivares
dockers:
  - id: base
    ids: [olivares]
    dockerfile: Dockerfile.release
    extra_files: [LICENSE, NOTICE, LICENSES]
Y
cat > Dockerfile.release <<D
FROM scratch
COPY olivares /usr/local/bin/olivares
COPY LICENSE NOTICE /usr/share/doc/
COPY LICENSES /usr/share/doc/LICENSES
D
touch LICENSE NOTICE; mkdir -p LICENSES'

# --- 6 · NO-CAZAR: un directorio de extra_files cubre lo que hay debajo. ------------------
caso "extra_files como directorio cubre sus hijos" 0 '
cat > .goreleaser.yaml <<Y
builds:
  - id: olivares
    binary: olivares
dockers:
  - id: base
    ids: [olivares]
    dockerfile: Dockerfile.release
    extra_files: [LICENSES]
Y
cat > Dockerfile.release <<D
FROM scratch
COPY olivares /bin/olivares
COPY LICENSES/AGPL-3.0-only.txt /usr/share/doc/
D
mkdir -p LICENSES; touch LICENSES/AGPL-3.0-only.txt'

# --- 7 · NO PUDE MIRAR: la entrada apunta a un Dockerfile que no existe. rc 2, no 1. ------
# «No poder mirar» nunca se gasta como aprobado NI como hallazgo.
caso "dockerfile inexistente -> 2, no 1" 2 '
cat > .goreleaser.yaml <<Y
builds:
  - id: olivares
    binary: olivares
dockers:
  - id: base
    ids: [olivares]
    dockerfile: Dockerfile.noexiste
    extra_files: [LICENSE]
Y
touch LICENSE'

# --- 8 · NO PUDE MIRAR: config sin entradas dockers. -------------------------------------
caso "config sin dockers -> 2" 2 '
cat > .goreleaser.yaml <<Y
builds:
  - id: olivares
    binary: olivares
Y
true'

echo "test-docker-context-sufficiency: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
