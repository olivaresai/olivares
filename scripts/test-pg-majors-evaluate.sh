#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Mutation battery for scripts/pg-majors-evaluate.py — every row is a failure mode
# the evaluator must catch, plus the green baseline it must accept. A gate that
# has only ever been seen passing has never been seen working (the repo's
# standing rule for lint:cosign-pins and lint:ci-ports); the pg-majors workflow
# runs this battery in the same job before trusting the evaluator's verdict.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-pgmaj-eval.XXXXXX")" || exit 1
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

pass=0
fail=0
check() {
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1))
		printf '  ok    %-58s %s\n' "$1" "$2"
	else
		fail=$((fail + 1))
		printf '  FAIL  %-58s %s\n' "$1" "$2"
	fi
}

# baseline fixture: 2 packages, floors 2 and 1, four clean passes
setup() { # setup <casedir>
	local d="$1"
	mkdir -p "$d/ci"
	cat >"$d/ci/pg-majors-packages.txt" <<'EOF'
# comment line
pkg/alpha
pkg/beta
EOF
	cat >"$d/ci/pg-majors-expectations.json" <<'EOF'
{"floors": {"pkg/alpha": 2, "pkg/beta": 1}}
EOF
	for m in 15 16 17 18; do
		cat >"$d/gotest-$m.json" <<EOF
{"Action":"pass","Package":"github.com/olivaresai/olivares/pkg/alpha","Test":"TestOne"}
{"Action":"pass","Package":"github.com/olivaresai/olivares/pkg/alpha","Test":"TestTwo"}
{"Action":"pass","Package":"github.com/olivaresai/olivares/pkg/beta","Test":"TestB"}
EOF
		echo "$m 0" >>"$d/pg-majors-exits.txt"
		echo "PG_MAJOR_DSN_VERIFIED|major=$m|port=4000$m|server_version_num=${m}0000" >>"$d/pg-majors-receipts.txt"
	done
}

run_case() { # run_case <name>  (case dir must exist) -> sets rc
	(cd "$WORK/$1" && python3 "$ROOT/scripts/pg-majors-evaluate.py" >out.log 2>&1)
	rc=$?
}

# ---- green baseline -------------------------------------------------------
setup "$WORK/green"
run_case green
check "clean four-major run is ACCEPTED" "baseline" "$rc"

# ---- mutations: every one must turn the verdict red -----------------------
setup "$WORK/m-exit"
sed -i 's/^17 0$/17 1/' "$WORK/m-exit/pg-majors-exits.txt"
run_case m-exit
[ "$rc" -ne 0 ] && grep -q "pg17: go test exit 1" "$WORK/m-exit/out.log"
check "a non-zero pass exit is RED" "exit status" "$?"

setup "$WORK/m-floor"
sed -i '0,/TestTwo/{/TestTwo/d}' "$WORK/m-floor/gotest-16.json"
run_case m-floor
[ "$rc" -ne 0 ] && grep -q "pkg/alpha passed 1 < floor 2" "$WORK/m-floor/out.log"
check "a pass below its floor is RED" "under-execution" "$?"

setup "$WORK/m-skip"
echo '{"Action":"skip","Package":"github.com/olivaresai/olivares/pkg/beta","Test":"TestSkipped"}' >>"$WORK/m-skip/gotest-18.json"
run_case m-skip
[ "$rc" -ne 0 ] && grep -q "SKIP in matrix packages" "$WORK/m-skip/out.log"
check "a single SKIP in a matrix package is RED" "no-skip rule" "$?"

setup "$WORK/m-receipt"
sed -i '/major=16/d' "$WORK/m-receipt/pg-majors-receipts.txt"
run_case m-receipt
[ "$rc" -ne 0 ] && grep -q "receipts do not cover exactly" "$WORK/m-receipt/out.log"
check "a missing major receipt is RED" "receipt coverage" "$?"

setup "$WORK/m-dupe"
sed -i 's/major=16/major=15/' "$WORK/m-dupe/pg-majors-receipts.txt"
run_case m-dupe
[ "$rc" -ne 0 ] && grep -q "receipts do not cover exactly" "$WORK/m-dupe/out.log"
check "two receipts for the SAME major are RED" "same-server trap" "$?"

setup "$WORK/m-missing"
rm "$WORK/m-missing/gotest-17.json"
run_case m-missing
[ "$rc" -ne 0 ] && grep -q "gotest-17.json missing" "$WORK/m-missing/out.log"
check "a pass that never ran is RED" "absent stream" "$?"

setup "$WORK/m-stowaway"
printf 'pkg/gamma\n' >>"$WORK/m-stowaway/ci/pg-majors-packages.txt"
run_case m-stowaway
[ "$rc" -ne 0 ] && grep -q "floors mismatch" "$WORK/m-stowaway/out.log"
check "a listed package with NO floor is RED" "stowaway package" "$?"

setup "$WORK/m-zerofloor"
sed -i 's/"pkg\/beta": 1/"pkg\/beta": 0/' "$WORK/m-zerofloor/ci/pg-majors-expectations.json"
run_case m-zerofloor
[ "$rc" -ne 0 ] && grep -q "every floor must be > 0" "$WORK/m-zerofloor/out.log"
check "a zero floor is RED" "test-less package trap" "$?"

echo
echo "pg-majors-evaluate: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ] || exit 1
