#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Mutation matrix for the CI port policy gate
# (cmd/olivares/tools/checkciports, invoked through scripts/check-ci-ports.sh).
#
# WHY THIS EXISTS. The gate protects one property: no workflow in this repository may map
# a fixed HOST port — the shape that killed a job in "Initialize containers" with "Bind
# for :::5432 failed: port is already allocated" the moment a second job ran on the same
# runner host. The repository's own history (see scripts/test-cosign-pins-gate.sh) shows
# how YAML gates rot: a gate that has only ever been seen passing has never been seen
# working. Every red row below is a form the policy must refuse; every green row is a
# form it must not flag — including the Kubernetes `ports:` heredoc that a text scanner
# would false-positive on.
#
# Red rows assert a SEMANTIC diagnostic substring, not just a nonzero exit: the gate also
# exits nonzero on usage errors, and a rejection case satisfied by the wrong failure is a
# scope regression wearing a green coat. Green rows assert the reported PORT ENTRY COUNT,
# so a gate that silently stopped seeing entries cannot stay green.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$ROOT/scripts/check-ci-ports.sh"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-ci-ports.XXXXXX")" || exit 1
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

pass=0
fail=0
CASE_DIR=""

begin() {
	CASE_DIR="$(mktemp -d "$WORK/case.XXXXXX")"
	mkdir -p "$CASE_DIR/.github/workflows"
}

add() { # add <relative-path>  (body on stdin)
	local p="$CASE_DIR/$1"
	mkdir -p "$(dirname "$p")"
	cat >"$p"
}

check() {
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1))
		printf '  ok    %-64s %s\n' "$1" "$2"
	else
		fail=$((fail + 1))
		printf '  FAIL  %-64s %s\n' "$1" "$2"
	fi
}

# run_gate <expected: red|green> <diagnostic-or-count-substring> <label>
run_gate() {
	local expect="$1" needle="$2" label="$3" rc out
	out="$(CI_PORTS_ROOT="$CASE_DIR" bash "$GATE" 2>&1)"
	rc=$?
	case "$expect" in
	red)
		[ "$rc" -ne 0 ] && grep -q "$needle" <<<"$out"
		check "$label" "red + diagnostic" $?
		;;
	green)
		[ "$rc" -eq 0 ] && grep -q "$needle" <<<"$out"
		check "$label" "green + counted" $?
		;;
	esac
}

echo "ci-ports gate — mutation matrix"

# --- RED: every fixed-host-port form must be refused ------------------------------------

begin
add .github/workflows/wf.yml <<'YAML'
jobs:
  j:
    runs-on: x
    services:
      pg:
        image: img
        ports:
          - 5432:5432
YAML
run_gate red "only a bare container port is allowed" "block host mapping 5432:5432 is refused"

begin
add .github/workflows/wf.yml <<'YAML'
jobs:
  j:
    runs-on: x
    services:
      pg:
        image: img
        ports:
          - "5432:5432"
YAML
run_gate red "only a bare container port is allowed" "quoted host mapping \"5432:5432\" is refused"

begin
add .github/workflows/wf.yml <<'YAML'
jobs:
  j:
    runs-on: x
    services:
      pg:
        image: img
        ports: ["5432:5432"]
YAML
run_gate red "only a bare container port is allowed" "flow-sequence host mapping is refused"

begin
add .github/workflows/wf.yml <<'YAML'
jobs:
  j:
    runs-on: x
    services:
      pg:
        image: img
        ports:
          - 127.0.0.1:5432:5432
YAML
run_gate red "only a bare container port is allowed" "ip-qualified host mapping is refused"

begin
add .github/workflows/wf.yml <<'YAML'
jobs:
  j:
    runs-on: x
    services:
      pg:
        image: img
        ports:
          - 5432:5432/tcp
YAML
run_gate red "only a bare container port is allowed" "protocol-suffixed host mapping is refused"

# The bad entry arrives through an ALIAS anchored in an unvalidated spot (the service
# env). A walker that skips aliases either misses it or trips on the AliasNode; both
# must end red with the semantic diagnostic, so alias handling is proven, not assumed.
begin
add .github/workflows/wf.yml <<'YAML'
jobs:
  j:
    runs-on: x
    services:
      pg:
        image: img
        env:
          SEED: &hostmap "5432:5432"
        ports:
          - *hostmap
YAML
run_gate red "only a bare container port is allowed" "host mapping smuggled through an alias is refused"

begin
add .github/workflows/wf.yml <<'YAML'
jobs:
  j:
    runs-on: x
    services:
      pg:
        image: img
        ports:
          - ${{ vars.PGPORT }}:5432
YAML
run_gate red "only a bare container port is allowed" "expression-built host mapping is refused"

begin
add .github/workflows/wf.yml <<'YAML'
jobs:
  j:
    runs-on: x
    container:
      image: img
      ports:
        - 5432:5432
YAML
run_gate red "only a bare container port is allowed" "container.ports host mapping is refused"

# `- 5432: 5432` (with the space) is a genuine YAML MAPPING entry, not a scalar: an
# unmodelled construct must be refused, never silently skipped.
begin
add .github/workflows/wf.yml <<'YAML'
jobs:
  j:
    runs-on: x
    services:
      pg:
        image: img
        ports:
          - 5432: 5432
YAML
run_gate red "not a scalar" "a mapping ports entry is refused (fail-closed)"

# The offence hides in the SECOND YAML document; a gate that decodes only the first
# certifies half a file.
begin
add .github/workflows/wf.yml <<'YAML'
jobs:
  j:
    runs-on: x
---
jobs:
  k:
    runs-on: x
    services:
      pg:
        image: img
        ports:
          - 5432:5432
YAML
run_gate red "only a bare container port is allowed" "a second YAML document is inspected too"

# A merge key inside a walked mapping could carry a ports block the walker never visits.
begin
add .github/workflows/wf.yml <<'YAML'
jobs:
  j:
    runs-on: x
    services:
      pg: &svc
        image: img
        ports:
          - 5432:5432
      pg2:
        <<: *svc
        image: img2
YAML
run_gate red "merge key" "a YAML merge key in a service mapping is refused"

# An empty fixture tree must be a loud error, not a vacuous pass.
begin
run_gate red "no workflow files" "an empty tree cannot pass vacuously"

# --- GREEN: the closed allowlist, and the false-positive the grep design would have -----

begin
add .github/workflows/wf.yml <<'YAML'
jobs:
  j:
    runs-on: x
    services:
      pg:
        image: img
        ports:
          - 5432
YAML
run_gate green "1 port entries" "a bare container port passes"

begin
add .github/workflows/wf.yml <<'YAML'
jobs:
  j:
    runs-on: x
    services:
      pg:
        image: img
        ports:
          - "5432"
YAML
run_gate green "1 port entries" "a quoted bare container port passes"

begin
add .github/workflows/wf.yml <<'YAML'
jobs:
  j:
    runs-on: x
    services:
      pg:
        image: img
        ports:
          - 5432/tcp
YAML
run_gate green "1 port entries" "a protocol-suffixed bare port passes"

begin
add .github/workflows/wf.yml <<'YAML'
jobs:
  j:
    runs-on: x
    services:
      pg:
        image: img
YAML
run_gate green "0 port entries" "a service without ports passes"

# The pg-majors shape (an internal design note (not shipped) §2.7): flow style, bare.
begin
add .github/workflows/wf.yml <<'YAML'
jobs:
  j:
    runs-on: x
    services:
      pg15:
        image: img
        ports: [5432]
      pg16:
        image: img
        ports: [5432]
YAML
run_gate green "2 port entries" "flow-style bare ports pass (pg-majors shape)"

# The false positive a key scanner is born with: Kubernetes `ports:` inside a run: |
# heredoc (e2e-operator-kind.yml). A block scalar is a value, not a mapping.
begin
add .github/workflows/wf.yml <<'YAML'
jobs:
  e2e:
    runs-on: x
    steps:
      - name: apply manifests
        run: |
          kubectl apply -f - <<'MANIFEST'
          apiVersion: v1
          kind: Service
          spec:
            ports:
              - port: 8081
                targetPort: 8081
          MANIFEST
YAML
run_gate green "0 port entries" "k8s ports heredoc inside run is NOT flagged"

echo
# --- ⛔ LA TERCERA RESPUESTA, Y ESTA MATRIZ NO PODÍA VERLA. `run_gate` juzga «rojo» con
# `rc -ne 0`, así que un 2 («no he podido mirar») y un 1 («está roto») le daban lo mismo — y por
# ahí se coló que el envoltorio degradara el 2 a 1: `go run` NO propaga el código del programa
# (medido aislado con un `os.Exit(2)`: imprime «exit status 2» y sale 1). La herramienta decía
# la verdad y el envoltorio la degradaba a rojo, que es la distinción que este árbol lleva el
# día entero defendiendo. Esta casilla exige el código EXACTO.
begin
_out="$(CI_PORTS_ROOT="$CASE_DIR" bash "$GATE" 2>&1)"; _rc=$?
[ "$_rc" -eq 2 ]
check "un sujeto VACÍO es 2 (no he podido mirar), no 1" "rc=$_rc" $?

# ⛔ CONTROL NEGATIVO: sin él, la casilla de arriba se cumpliría haciendo que el gate devolviera
#    2 SIEMPRE. Un fixture válido tiene que seguir dando 0 exacto.
begin
add .github/workflows/ok.yml <<'YAML'
jobs:
  j:
    runs-on: x
    services:
      pg:
        image: img
        ports:
          - 5432
YAML
_out="$(CI_PORTS_ROOT="$CASE_DIR" bash "$GATE" 2>&1)"; _rc=$?
[ "$_rc" -eq 0 ]
check "y un fixture válido sigue dando 0 exacto" "rc=$_rc" $?

# --- ⛔ LA GUARDA QUE NO SE ALCANZABA. Esta casilla es TRANSVERSAL a los tres envoltorios Go
# porque comparten forma. `checkciports` ya sabía negarse bien —«no workflow files under … —
# wrong CI_PORTS_ROOT? refusing to report a vacuous pass», rc 2— pero si el layout no está, el
# `cd "$ROOT/cmd/olivares"` fallaba ANTES y `set -e` cortaba con el error crudo del shell
# (`line 29: cd: …: No such file or directory`, rc 1). Un veredicto perfecto, inalcanzable por la
# línea que tenía delante. Lo midió el carril de integración copiando el script a un árbol vacío — que es
# la única forma de cegarlo, porque la raíz se resuelve desde `$0` y un `cd` del llamante no le
# afecta.
_repo="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
for _g in check-ci-ports check-cosign-pins check-cosign-wiring; do
	_d="$(mktemp -d "$WORK/solo.XXXXXX")"; mkdir -p "$_d/scripts"
	cp "$_repo/scripts/$_g.sh" "$_d/scripts/"
	( cd "$_d" && bash "scripts/$_g.sh" >/dev/null 2>&1 ); _rc=$?
	[ "$_rc" -eq 2 ]
	check "$_g sin su layout es 2, no el error crudo del shell" "rc=$_rc" $?
done

# ⛔ CONTROL NEGATIVO de la casilla anterior: sin él se cumpliría haciendo que los envoltorios
#    devolvieran 2 SIEMPRE. Sobre el repositorio real tienen que seguir dando 0.
for _g in check-ci-ports check-cosign-pins check-cosign-wiring; do
	bash "$_repo/scripts/$_g.sh" >/dev/null 2>&1; _rc=$?
	[ "$_rc" -eq 0 ]
	check "$_g sobre el arbol real sigue dando 0" "rc=$_rc" $?
done

# --- ⛔ PROPAGACIÓN CON DOS COLAPSOS APILADOS (`check-install-manifest`). Esta matriz es la dueña
# de la propiedad «el envoltorio entrega el veredicto de su herramienta», así que el tercer caso
# vive aquí aunque el gate sea de otra familia. Localizado por el carril de integración con `file:line`:
#
#   ( cd … && go run ./cmd/checkinstallmanifest "$MANIFEST" ) || exit 1
#        ↑ `go run` convierte el 2 en 1        ↑ `|| exit 1` aplasta CUALQUIER código
#
# Se prueba con un señuelo COMPLETO cuya herramienta sale **3** — un código que ninguno de los dos
# colapsos puede producir por casualidad, así que el resultado no admite otra lectura.
_d="$(mktemp -d "$WORK/im.XXXXXX")"
mkdir -p "$_d/scripts" "$_d/deploy/manifests" "$_d/operator/cmd/checkinstallmanifest"
cp "$_repo/scripts/check-install-manifest.sh" "$_d/scripts/"
# Y su libreria: el senuelo prueba la PROPAGACION del codigo de la herramienta, asi que tiene
# que estar completo. El caso CIEGO de mas arriba es el que se queda sin ella a proposito.
mkdir -p "$_d/scripts/lib"
cp "$_repo/scripts/lib/exec-workdir.sh" "$_d/scripts/lib/"
printf 'apiVersion: v1\nkind: Service\n' > "$_d/deploy/manifests/install.yaml"
printf 'module operator\n\ngo 1.24\n' > "$_d/operator/go.mod"
printf 'package main\n\nimport "os"\n\nfunc main() { os.Exit(3) }\n' \
	> "$_d/operator/cmd/checkinstallmanifest/main.go"
# ⛔ GOWORK=off, y no es adorno: `/tmp` es noexec en los contenedores, asi que todo carril pone
# TMPDIR DENTRO del repo — y entonces este senuelo nace bajo el `go.work` del arbol, `go run` se
# niega («not one of the workspace modules») y el envoltorio contesta 2 en vez del 3 de su
# herramienta. El caso suspendia por el ENTORNO y se leia como una regresion de main.
( cd "$_d" && GOWORK=off sh scripts/check-install-manifest.sh >/dev/null 2>&1 ); _rc=$?
[ "$_rc" -eq 3 ]
check "check-install-manifest entrega el codigo de su herramienta (3)" "rc=$_rc" $?

# ⛔ CONTROL NEGATIVO: sobre el árbol real sigue dando 0 exacto — sin esto, la casilla de arriba
#    se cumpliría con un envoltorio que devolviera siempre lo que le apeteciera.
bash "$_repo/scripts/check-install-manifest.sh" >/dev/null 2>&1; _rc=$?
[ "$_rc" -eq 0 ]
check "check-install-manifest sobre el arbol real sigue dando 0" "rc=$_rc" $?

echo "ci-ports gate: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ] || exit 1
