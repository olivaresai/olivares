#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-cli-ref-wrapper.sh — the battery for the STAGES of scripts/check-cli-ref-docs.sh.
#
# WHY IT EXISTS, measured 2026-08-16 by mutation. scripts/cli-ref-docs carries a
# 28-case fixture battery, and it is thorough: seven mutants of the generator
# (fixed-list roster, disarmed population floor, self-comparison, neutered prose
# sweep, CANNOT LOOK downgraded to 0, unchecked schema, one-directional exit-code
# cross-check) were each killed by a NAMED case. But that battery links the
# generator directly and never runs the wrapper, and the wrapper is where the
# decision "could I look at all?" is actually taken. Two mutants of it survived the
# whole 28 cases untouched:
#
#   M8  `go test … || true` + `test_rc=0`     — 28/28 green, real tree green
#   M9  the `[ ! -s "$DUMP" ]` guard removed  — 28/28 green, real tree green
#
# M8 is not academic and it is not an equivalent mutant. TestCLIRefDump reports its
# environment-independence and universal-`--help` assertions with t.Errorf, which
# does NOT stop the test, so the dump is written anyway. Measured with that failure
# planted: the real wrapper answers 2 (CANNOT LOOK) and the M8 wrapper answers 0
# and prints "OK — the page matches the binary", while the walk's own assertions
# had failed. A gate that certifies is worse than no gate, and `… || true` on a
# sentinel is a defect this repository has already paid for once (CLAUDE.md,
# "un `fetch … || true` se traga el fallo").
#
# HOW IT TESTS THE STAGES WITHOUT A HOOK IN THEM. There is no environment variable
# that makes the wrapper substitute its walk — an override like that would be an
# escape hatch in the production path, which is the opposite of what this is for.
# Instead the battery runs the wrapper UNMODIFIED, from the real repository root,
# with a throwaway `go` shim FIRST ON PATH. The shim forwards everything except
# `go test` to the real toolchain; for `go test` it plays the failure the case is
# about. The wrapper cannot tell, and nothing in it changed.
#
# THE GREEN CONTROL IS NOT FILLER. Case 1 lets the shim forward the walk to the
# real toolchain and keeps the dump it produced as the golden one for the rest, so
# every red case below runs against an enumeration that is genuinely valid. Without
# that control a wrapper that failed unconditionally would satisfy the whole red
# column.
#
# Exit 0 all cases behaved / 1 a case did not / 2 the battery could not run.
set -uo pipefail
export LC_ALL=C

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
WRAPPER="$ROOT/scripts/check-cli-ref-docs.sh"

die_cannot() {
	echo "cli-ref wrapper battery: CANNOT LOOK — $1" >&2
	echo "  A battery that could not run is not a battery that passed." >&2
	exit 2
}

[ -f "$WRAPPER" ] || die_cannot "the wrapper under test is missing at $WRAPPER."
REAL_GO="$(command -v go 2>/dev/null)" || true
[ -n "$REAL_GO" ] || die_cannot "no Go toolchain on PATH, so the wrapper's own stages were never exercised."

[ -n "${TMPDIR:-}" ] && mkdir -p "$TMPDIR" 2>/dev/null
SCRATCH="$(mktemp -d 2>/dev/null)" || die_cannot "could not create a scratch dir (TMPDIR=${TMPDIR:-unset})."
trap 'rm -rf "$SCRATCH"' EXIT

GOLDEN="$SCRATCH/golden.json"
SHIMDIR="$SCRATCH/shim"
mkdir -p "$SHIMDIR" || die_cannot "could not create the shim directory under $SCRATCH."

# The shim. Everything that is not `go test` is the real toolchain, so the
# wrapper's own `go build` of the generator is untouched and real.
cat >"$SHIMDIR/go" <<'SHIM'
#!/bin/sh
if [ "${1:-}" != "test" ]; then
	exec "$CLIREF_REAL_GO" "$@"
fi
case "${CLIREF_SHIM_MODE:-}" in
passthrough)
	"$CLIREF_REAL_GO" "$@"
	rc=$?
	if [ -s "${OLIVARES_CLIREF_DUMP_OUT:-}" ]; then
		cp "$OLIVARES_CLIREF_DUMP_OUT" "$CLIREF_GOLDEN" || exit 3
	fi
	exit $rc
	;;
good)
	cp "$CLIREF_GOLDEN" "$OLIVARES_CLIREF_DUMP_OUT" || exit 3
	echo "ok  	github.com/olivaresai/olivares/cmd/olivares	0.02s"
	exit 0
	;;
fail-but-write)
	# The shape that matters: t.Errorf does not stop the test, so a walk whose
	# assertions FAILED still writes a complete, valid dump. Only the exit code
	# testifies, which is exactly what mutant M8 threw away.
	cp "$CLIREF_GOLDEN" "$OLIVARES_CLIREF_DUMP_OUT" || exit 3
	echo "--- FAIL: TestCLIRefDump (simulated: a flag takes its default from the environment)"
	echo "FAIL	github.com/olivaresai/olivares/cmd/olivares	0.02s"
	exit 1
	;;
pass-write-nothing)
	echo "ok  	github.com/olivaresai/olivares/cmd/olivares	0.01s"
	exit 0
	;;
pass-write-empty)
	: >"$OLIVARES_CLIREF_DUMP_OUT" || exit 3
	echo "ok  	github.com/olivaresai/olivares/cmd/olivares	0.01s"
	exit 0
	;;
drift)
	# A valid enumeration that disagrees with the published page in ONE cell. The
	# verdict must be 1 (drift), never 2 and never 0: the wrapper has to pass the
	# generator's three answers through distinctly.
	sed 's/"default": ""/"default": "zz-wrapper-battery-drift"/' "$CLIREF_GOLDEN" \
		>"$OLIVARES_CLIREF_DUMP_OUT" || exit 3
	if cmp -s "$CLIREF_GOLDEN" "$OLIVARES_CLIREF_DUMP_OUT"; then
		# The substitution found nothing to change, so this case would be testing
		# nothing. Fail loudly rather than hand back an accidental green.
		echo "drift shim: no empty-string default left in the dump to perturb" >&2
		exit 3
	fi
	echo "ok  	github.com/olivaresai/olivares/cmd/olivares	0.02s"
	exit 0
	;;
*)
	echo "shim: unknown CLIREF_SHIM_MODE '${CLIREF_SHIM_MODE:-}'" >&2
	exit 3
	;;
esac
SHIM
chmod +x "$SHIMDIR/go" || die_cannot "could not make the shim executable under $SCRATCH (a noexec TMPDIR?)."

fails=0
cases=0

# report <name> <want-rc> <got-rc> <needle> <output>
report() {
	cases=$((cases + 1))
	local name="$1" want="$2" got="$3" needle="$4" out="$5" bad=0
	[ "$got" = "$want" ] || bad=1
	case "$out" in *"$needle"*) ;; *) bad=1 ;; esac
	if [ "$bad" -eq 0 ]; then
		printf '  ok   %-38s want rc=%s got rc=%s\n' "$name" "$want" "$got"
		return
	fi
	fails=$((fails + 1))
	printf '  FAIL %-38s want rc=%s got rc=%s\n' "$name" "$want" "$got"
	case "$out" in *"$needle"*) ;; *) printf '       expected the output to name "%s"\n' "$needle" ;; esac
	printf '%s\n' "$out" | sed 's/^/       | /'
}

# run_shim <name> <mode> <want-rc> <needle>
run_shim() {
	local name="$1" mode="$2" want="$3" needle="$4" out rc
	out="$(cd "$ROOT" && PATH="$SHIMDIR:$PATH" CLIREF_REAL_GO="$REAL_GO" \
		CLIREF_GOLDEN="$GOLDEN" CLIREF_SHIM_MODE="$mode" \
		bash "$WRAPPER" 2>&1)"
	rc=$?
	report "$name" "$want" "$rc" "$needle" "$out"
}

# ── 1. the green control, which also mints the golden dump ────────────────────────────
run_shim "walk-succeeds-page-in-sync" passthrough 0 "matches the binary"
[ -s "$GOLDEN" ] || die_cannot "the forwarded walk produced no golden dump, so every case below would have been testing the shim rather than the wrapper."

# ── 2. the two mutants the generator's own battery could not kill ─────────────────────
run_shim "walk-fails-but-wrote-a-valid-dump" fail-but-write 2 "the command-tree walk failed"
run_shim "walk-passes-and-writes-nothing" pass-write-nothing 2 "wrote no command tree"
run_shim "walk-passes-and-writes-empty" pass-write-empty 2 "wrote no command tree"

# ── 3. the three verdicts stay distinct ───────────────────────────────────────────────
run_shim "valid-walk-that-disagrees-is-drift" drift 1 "out of date with the command tree"

# ── 4. the wrapper's other refusals ───────────────────────────────────────────────────
out="$(cd "$ROOT" && bash "$WRAPPER" --not-a-mode 2>&1)"
report "unknown-argument" 2 "$?" "unknown argument" "$out"

# A PATH built by DROPPING every directory that holds a `go`, rather than a
# hardcoded /usr/bin:/bin: the wrapper still needs dirname, mktemp and sed to reach
# its toolchain check at all, and where Go is installed is a property of the machine.
nogo_path=""
IFS=':' read -r -a path_dirs <<<"$PATH"
for d in "${path_dirs[@]}"; do
	[ -n "$d" ] || continue
	[ -x "$d/go" ] && continue
	nogo_path="${nogo_path:+$nogo_path:}$d"
done
out="$(cd "$ROOT" && PATH="$nogo_path" bash "$WRAPPER" 2>&1)"
report "no-go-toolchain" 2 "$?" "no Go toolchain on PATH" "$out"

out="$(cd "$ROOT" && TMPDIR=/proc/zz-cli-ref-battery-nowhere bash "$WRAPPER" 2>&1)"
report "scratch-dir-uncreatable" 2 "$?" "could not create a scratch dir" "$out"

mkdir -p "$SCRATCH/orphan/scripts" && cp "$WRAPPER" "$SCRATCH/orphan/scripts/"
out="$(bash "$SCRATCH/orphan/scripts/check-cli-ref-docs.sh" 2>&1)"
report "generator-source-missing" 2 "$?" "the gate's own source is missing" "$out"

echo "cli-ref wrapper battery: $cases cases, $fails failed"
[ "$fails" -eq 0 ] || exit 1
exit 0
