#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Selftest for examples/bring-your-own-protocol/smoke.sh, step "generate content-source
# skeleton". That step used `go run ./cmd/olivares`, which recompiles the whole
# engine with different flags than `task build:bin` (measured on the builder: 84 s for the
# build, 232 s for the `go run` that followed, 1 s for the built binary) and printed nothing
# until the 25-minute job timeout. The fixture replaces `go` and the engine with recorders,
# so the test checks what the smoke executes, not what it prints.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Fake executables need an exec mount; mirror build-a-connector/smoke.sh and the CI step
# that picks OLIVARES_SMOKE_TMPDIR on runners whose _work is noexec.
BASE="${OLIVARES_SMOKE_TMPDIR:-$ROOT/.examples-tmp}"
mkdir -p "$BASE"
TMPROOT="$(mktemp -d "$BASE/byop-selftest.XXXXXX")"
trap 'rm -rf -- "$TMPROOT"' EXIT

pass=0
fail=0

# fixture DIR: a repository root holding the smoke under test and the two recorders.
fixture() {
	local fx=$1
	mkdir -p "$fx/examples/bring-your-own-protocol/erp-fixture" \
		"$fx/examples/bring-your-own-protocol/fabworks-connector/scripts" \
		"$fx/examples/bring-your-own-protocol/admission-check" \
		"$fx/sdk" "$fx/fakebin"
	cp "$ROOT/examples/bring-your-own-protocol/smoke.sh" "$fx/examples/bring-your-own-protocol/smoke.sh"
	# The engine: records its argv, writes the file the smoke asserts on, or hangs.
	cat >"$fx/engine.sh" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FX_CAPTURE/engine.args"
[ "${FX_HANG_ENGINE:-}" = 1 ] && exec sleep 300
while [ "$#" -gt 0 ]; do
	[ "$1" = --dir ] && dir=$2
	shift
done
mkdir -p "$dir/cmd/acme-generated-fabworks"
: >"$dir/cmd/acme-generated-fabworks/main.go"
EOF
	# go: records argv; `build -o X` leaves the engine at X; the subcommand named by FX_HANG_GO
	# hangs, the one named by FX_KILL_GO dies by SIGKILL (what an OOM kill looks like).
	cat >"$fx/fakebin/go" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FX_CAPTURE/go.args"
[ "$1" = build ] && printf 'CGO_ENABLED=%s\n' "${CGO_ENABLED-<unset>}" >>"$FX_CAPTURE/go.cgo"
[ "$1" = "${FX_HANG_GO:-}" ] && exec sleep 300
[ "$1" = "${FX_KILL_GO:-}" ] && kill -9 $$
if [ "$1" = build ]; then
	while [ "$#" -gt 0 ]; do
		if [ "$1" = -o ]; then mkdir -p "$(dirname "$2")"; cp "$FX_ENGINE" "$2"; fi
		shift
	done
fi
exit 0
EOF
	printf '#!/usr/bin/env bash\nexit 0\n' >"$fx/examples/bring-your-own-protocol/fabworks-connector/scripts/check-boundary.sh"
	chmod +x "$fx/engine.sh" "$fx/fakebin/go"
}

# smoke FIXTURE [VAR=VALUE...]: run the smoke from a fixture, 30 s outer cap, log in FIXTURE/out.log.
smoke() {
	local fx=$1
	shift
	(
		cd "$fx"
		env FX_CAPTURE="$fx" FX_ENGINE="$fx/engine.sh" PATH="$fx/fakebin:$PATH" "$@" \
			timeout 30 bash examples/bring-your-own-protocol/smoke.sh >"$fx/out.log" 2>&1
	)
}

check() {
	local name=$1
	shift
	if "$@"; then
		printf 'ok    %s\n' "$name"
		pass=$((pass + 1))
	else
		printf 'FAIL  %s\n' "$name"
		fail=$((fail + 1))
	fi
}

# 1. With bin/olivares present the skeleton comes from it, the engine is never recompiled, and
#    the smoke runs to its last line.
case_built_engine() {
	local fx="$TMPROOT/built"
	fixture "$fx"
	mkdir -p "$fx/bin"
	cp "$fx/engine.sh" "$fx/bin/olivares"
	smoke "$fx" || return 1
	grep -q '^smoke ok$' "$fx/out.log" || return 1
	grep -q "^connector init acme.generated-fabworks .*--template content-source .*--sdk-path $fx/sdk\$" "$fx/engine.args" || return 1
	! grep -q 'cmd/olivares' "$fx/go.args"
}
check "skeleton comes from bin/olivares, no go run/build of cmd/olivares, smoke ok" case_built_engine

# 2. Without bin/olivares it is built once, into the run's work dir, and used.
case_builds_when_absent() {
	local fx="$TMPROOT/absent"
	fixture "$fx"
	smoke "$fx" || return 1
	grep -q '^smoke ok$' "$fx/out.log" || return 1
	[ "$(grep -c '^build -trimpath -o .*/olivares \./cmd/olivares$' "$fx/go.args")" -eq 1 ] || return 1
	grep -qx 'CGO_ENABLED=0' "$fx/go.cgo" || return 1
	! grep -q '^run ./cmd' "$fx/go.args" || return 1
	[ ! -e "$fx/bin/olivares" ] || return 1
	grep -q '^connector init acme.generated-fabworks ' "$fx/engine.args"
}
check "bin/olivares absent: built once outside bin/, then used" case_builds_when_absent

# 2b. OLIVARES_BIN points the smoke at another engine (a Business binary, a release build).
case_bin_override() {
	local fx="$TMPROOT/override"
	fixture "$fx"
	mkdir -p "$fx/custom"
	cp "$fx/engine.sh" "$fx/custom/olivares"
	smoke "$fx" OLIVARES_BIN="$fx/custom/olivares" || return 1
	grep -q '^smoke ok$' "$fx/out.log" || return 1
	! grep -q '^build .*cmd/olivares' "$fx/go.args" || return 1
	grep -q '^connector init acme.generated-fabworks ' "$fx/engine.args"
}
check "OLIVARES_BIN selects the engine and nothing is built" case_bin_override

# 3. A step that never answers fails by itself, names the step, and says how long it waited.
#    One row per bounded command: HANG_STEP is the label the failure must carry.
case_hang_is_bounded() {
	local label=$1 want_bin=$2
	shift 2
	local fx="$TMPROOT/hang-${label// /-}" rc=0
	fixture "$fx"
	if [ "$want_bin" = bin ]; then
		mkdir -p "$fx/bin"
		cp "$fx/engine.sh" "$fx/bin/olivares"
	fi
	smoke "$fx" OLIVARES_SMOKE_STEP_TIMEOUT=1 "$@" || rc=$?
	[ "$rc" -ne 0 ] && [ "$rc" -ne 124 ] || { printf '        rc=%s (124 = only the outer cap stopped it)\n' "$rc" >&2; return 1; }
	grep -q "^FAIL: $label.* timed out after 1s" "$fx/out.log"
}
check "hang in the skeleton step is bounded and named" \
	case_hang_is_bounded "generate content-source skeleton" bin FX_HANG_ENGINE=1 FX_HANG_GO=run
check "hang in the engine fallback build is bounded and named" \
	case_hang_is_bounded "build the olivares engine" nobin FX_HANG_GO=build
check "hang in go test is bounded and named" \
	case_hang_is_bounded "unit test fixture" bin FX_HANG_GO=test
check "hang in the plugin build is bounded and named" \
	case_hang_is_bounded "build plugin binary" bin FX_HANG_GO=build
check "hang in the admission check is bounded and named" \
	case_hang_is_bounded "offline sign and admission check" bin FX_HANG_GO=run

# 4. A command killed from outside is reported as killed, not as a timeout.
case_kill_is_not_timeout() {
	local fx="$TMPROOT/kill" rc=0
	fixture "$fx"
	mkdir -p "$fx/bin"
	cp "$fx/engine.sh" "$fx/bin/olivares"
	smoke "$fx" FX_KILL_GO=test || rc=$?
	[ "$rc" -ne 0 ] || return 1
	grep -q '^FAIL: unit test fixture was killed (rc 137)' "$fx/out.log"
}
check "an external kill is reported as killed, not as a timeout" case_kill_is_not_timeout

printf 'bring-your-own-protocol smoke selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
