#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Mutation matrix for the blocking SAST gate (scripts/sast.sh).
#
# WHY THIS EXISTS. On 2026-08-01 the gate was found carrying four defects at once, and
# every one of them had survived because the gate had only ever been observed saying
# "clean" or "1 finding". Measured, with the pinned gosec v2.28.0:
#
#   1. Under -quiet, a CLEAN module and a module that DOES NOT COMPILE produce the same
#      thing: exit 0 and zero bytes of stdout. The gate parsed that as zero findings and
#      printed "sast: clean". A security gate that could not run said it had passed.
#   2. The per-finding printer died with "SyntaxError: f-string expression part cannot
#      include a backslash" on EVERY run, swallowed by `2>/dev/null || true`, so the gate
#      could report a count and never which finding — on CI, where the tree is not at
#      hand. Run 30666187624 burned a red `sast` job saying exactly "1 SAST finding(s)".
#   3. The advisory --report mode dropped unreadable scans with `except Exception: pass`,
#      so a module that stopped building vanished from the totals and the report improved.
#   4. gosec's exit status was treated as meaningful. It is not: under -no-fail it is 0
#      for clean, for findings, and for a module that does not compile.
#
# So the rows below assert the two things a count alone cannot: that a scan which did not
# happen is REFUSED rather than called clean, and that a finding is NAMED (rule, file,
# line) and not merely counted. Green rows assert the reported FILE COUNT, so a gate that
# silently stopped seeing source cannot stay green — the failure mode of defect 1.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-sast-gate.XXXXXX")" || exit 1
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

pass=0
fail=0

# The gate resolves its own root from $0, so each case is a miniature repository: a copy
# of the gate, the real .gosec.json (never a divergent fixture), a go.work naming the
# module under test, and the out-of-workspace cloud module the gate always scans.
begin() { # begin <module-dir-name>  -> echoes the case root
	local mod="$1" case_dir
	case_dir="$(mktemp -d "$WORK/case.XXXXXX")"
	mkdir -p "$case_dir/scripts" "$case_dir/$mod" "$case_dir/cloud/control-plane"
	cp "$ROOT/scripts/sast.sh" "$case_dir/scripts/sast.sh"
	cp "$ROOT/.gosec.json" "$case_dir/.gosec.json"
	cat >"$case_dir/$mod/go.mod" <<EOF
module example.com/$mod

go 1.24
EOF
	cat >"$case_dir/cloud/control-plane/go.mod" <<'EOF'
module example.com/cloudstub

go 1.24
EOF
	cat >"$case_dir/cloud/control-plane/stub.go" <<'EOF'
package cloudstub

// Stub is the minimum a module needs for gosec to have something to scan.
func Stub() int { return 0 }
EOF
	cat >"$case_dir/go.work" <<EOF
go 1.24

use ./$mod
EOF
	printf '%s' "$case_dir"
}

run_gate() { # run_gate <case_dir> ; sets OUT and RC
	OUT="$(cd "$1" && bash scripts/sast.sh 2>&1)"
	RC=$?
}

red() { # red <name> <expected-rc> <expected-substring>
	local name="$1" want_rc="$2" want_sub="$3"
	if [ "$RC" -eq 0 ]; then
		printf 'FAIL  %s: gate exited 0; it must refuse\n' "$name"
		fail=$((fail + 1))
		return
	fi
	if [ "$RC" -ne "$want_rc" ]; then
		printf 'FAIL  %s: exit %s, want %s (a refusal for the wrong reason is a regression)\n' \
			"$name" "$RC" "$want_rc"
		printf '%s\n' "$OUT" | sed 's/^/        /'
		fail=$((fail + 1))
		return
	fi
	case "$OUT" in
	*"$want_sub"*)
		printf 'ok    %s (exit %s, said %q)\n' "$name" "$RC" "$want_sub"
		pass=$((pass + 1))
		;;
	*)
		printf 'FAIL  %s: exit %s but never said %q\n' "$name" "$RC" "$want_sub"
		printf '%s\n' "$OUT" | sed 's/^/        /'
		fail=$((fail + 1))
		;;
	esac
}

green() { # green <name> <expected-substring>
	local name="$1" want_sub="$2"
	if [ "$RC" -ne 0 ]; then
		printf 'FAIL  %s: exit %s, want 0\n' "$name" "$RC"
		printf '%s\n' "$OUT" | sed 's/^/        /'
		fail=$((fail + 1))
		return
	fi
	case "$OUT" in
	*"$want_sub"*)
		printf 'ok    %s (clean, said %q)\n' "$name" "$want_sub"
		pass=$((pass + 1))
		;;
	*)
		printf 'FAIL  %s: passed but never said %q\n' "$name" "$want_sub"
		printf '%s\n' "$OUT" | sed 's/^/        /'
		fail=$((fail + 1))
		;;
	esac
}

# ---------------------------------------------------------------- red rows

# The defect that started this file. Before the fix this case printed "sast: clean".
C="$(begin broken)"
cat >"$C/broken/main.go" <<'EOF'
package main

func main() {
	this is not valid go
}
EOF
run_gate "$C"
red "a module that does not compile is refused, not called clean" 2 "did not compile for gosec"

# gosec absent: the gate must say so rather than scan nothing and pass.
#
# ⛔ ESTE CASO ERA VACUO EN CI, Y SU FALSO ROJO TUMBABA EL JOB `sast` DE CUALQUIER RAMA.
# `sast.sh:38-46` resuelve el binario por DOS vias: `command -v "$GOSEC"` y, si falla,
# `$(go env GOPATH)/bin/gosec`. La version anterior solo neutralizaba la primera, con
# `PATH=/usr/bin:/bin GOSEC=/nonexistent/gosec`, y por eso su resultado dependia del ENTORNO:
#
#   en esta caja      `go` NO esta en /usr/bin -> la 2a via tampoco resuelve -> rehusa -> ok
#   en el runner      `go` SI esta            -> la 2a via encuentra el gosec REAL -> escanea
#                                                y sale 0 -> el caso reporta FALSO ROJO
#
# Medido el 2026-08-26: aqui 6/0, en la corrida 32970144977 `5 passed, 1 failed` sobre un
# escaneo LIMPIO («0 blocking findings across 13 modules, 2262 files, 692210 lines») y una rama
# con CERO ficheros Go. Un caso que no puede crear su condicion no prueba nada: informa del
# entorno, no del sujeto.
#
# La condicion se crea ahora en CUALQUIER entorno, cerrando las DOS vias:
#   * `GOSEC=/nonexistent/gosec`      cierra `command -v`
#   * `GOPATH=<dir vacio y escribible>` cierra el respaldo — `go env GOPATH` lo devuelve tal cual
#     y `$GOPATH/bin/gosec` no existe. Un GOPATH INEXISTENTE no vale: `go env` muere («could not
#     create module cache») y se vuelve al camino de antes.
# Y se corre SIN restringir `PATH`, o sea con `go` disponible: entorno tipo RUNNER, que es donde
# fallaba.
C="$(begin absent)"
cat >"$C/absent/main.go" <<'EOF'
package main

func main() {}
EOF
GP_VACIO="$(mktemp -d "${TMPDIR:-/tmp}/sast-nogopath.XXXXXX")"
OUT="$(cd "$C" && GOPATH="$GP_VACIO" GOSEC=/nonexistent/gosec bash scripts/sast.sh 2>&1)"
RC=$?
red "a missing gosec is refused, in a runner-like env (go on PATH)" 1 "gosec not found"

# ⛔ EL OTRO ESTADO DE LA MUTACION, y sin el lo de arriba no prueba el mecanismo: con el MISMO
# fixture y el MISMO GOPATH vacio, pero con un gosec que SI resuelve, el gate tiene que pasar.
# Si este saliera rojo, el caso de arriba estaria rehusando por el GOPATH y no por el binario.
if _gs="$(command -v gosec 2>/dev/null)" && [ -x "$_gs" ]; then
  C="$(begin present)"
  cat >"$C/present/main.go" <<'EOF'
package main

func main() {}
EOF
  OUT="$(cd "$C" && GOPATH="$GP_VACIO" GOSEC="$_gs" bash scripts/sast.sh 2>&1)"
  RC=$?
  green "con gosec resoluble y el mismo GOPATH vacio, pasa (control del par)" "sast: clean"
else
  printf 'NO-MIRADO  el par del caso ausente: no hay gosec en esta caja\n' >&2
fi
rm -rf "$GP_VACIO"

# A finding must be NAMED. This is the row that fails if the printer regresses to the
# f-string form: the count would still be right and the gate would still be red.
C="$(begin weakrand)"
cat >"$C/weakrand/main.go" <<'EOF'
package main

import (
	"fmt"
	"math/rand"
)

func main() {
	fmt.Println(rand.Float64())
}
EOF
run_gate "$C"
red "a finding is named with its rule id" 1 "G404"

# ---------------------------------------------------------------- green rows

# Clean, and the count of scanned files is published so a collapse to zero is visible.
C="$(begin clean)"
cat >"$C/clean/main.go" <<'EOF'
package main

import "fmt"

func main() { fmt.Println("nothing weak here") }
EOF
run_gate "$C"
green "a clean module passes and reports what it measured" "sast: clean"
case "$OUT" in
*"0 files"* | *" 0 lines"*)
	printf 'FAIL  clean run reported zero files or lines; the measure is the point\n'
	printf '%s\n' "$OUT" | sed 's/^/        /'
	fail=$((fail + 1))
	;;
*)
	printf 'ok    clean run published a non-zero file and line count\n'
	pass=$((pass + 1))
	;;
esac

# A justified suppression is honoured — the gate must not become unusable to escape
# defect 1. #nosec carries the rule id and a reason, as -nosec-require-* demand.
C="$(begin justified)"
cat >"$C/justified/main.go" <<'EOF'
package main

import (
	"fmt"
	"math/rand"
)

func main() {
	// #nosec G404 -- jitter for retry decorrelation, not a secret
	fmt.Println(rand.Float64())
}
EOF
run_gate "$C"
green "a justified #nosec suppression still passes" "sast: clean"

printf '\ntest-sast-gate: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
