#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-sqlstore-race-entry-partition.sh — the battery for scripts/sqlstore-race-entry-partition.sh
# and the way scripts/core-race-partition.sh consumes it. Every case states what it would catch.
#
# Discovery is expensive here (it compiles the -race binary), so the union cases run on a
# SYNTHETIC inventory through `--assign`, which is the same placement code the real selection
# uses. One case pays for real discovery, once.
set -euo pipefail

# Fixture selectors must not inherit the CI matrix's pair. Otherwise the
# half-pair refusal becomes a valid pair and the unpartitioned argv case runs
# one shard. Only this battery process is changed; the CI job keeps its pair.
unset OLIVARES_CORE_RACE_PARTITION OLIVARES_CORE_RACE_PARTITIONS

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SUT="${ROOT}/scripts/sqlstore-race-entry-partition.sh"
CORE="${ROOT}/scripts/core-race-partition.sh"
TASKFILE="${ROOT}/Taskfile.yml"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/sqlstore-race-XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT

PASSED=0
FAILED=0
DECLARED_CASES=38
ok() {
	printf 'ok    %-62s %s\n' "$1" "${2:-}"
	PASSED=$((PASSED + 1))
}
bad() {
	printf 'FAIL  %-62s %s\n' "$1" "${2:-}"
	FAILED=$((FAILED + 1))
}
blind() {
	printf 'test-sqlstore-race-entry-partition: COULD NOT LOOK — %s\n' "$*" >&2
	exit 2
}

[ -r "${SUT}" ] || blind "${SUT} is unreadable"
[ -r "${CORE}" ] || blind "${CORE} is unreadable"

# Real compiled-build controls for R1: four ordinary entries and one tagged entry.
# --flag-controls runs only these small regressions, including before the repair.
flag_controls() {
	local fixture="${TMP}/flags" real_go i rc count good flag
	real_go="$(command -v go)"
	mkdir -p "${fixture}/scripts" "${fixture}/core/internal/store/sqlstore" "${fixture}/chosen" "${fixture}/decoy"
	cp "${SUT}" "${CORE}" "${fixture}/scripts/"
	printf 'module fixture\n\ngo 1.24\n' >"${fixture}/core/go.mod"
	printf 'package sqlstore\nimport "testing"\nfunc TestAlpha(t *testing.T) {}\nfunc TestBeta(t *testing.T) {}\nfunc TestGamma(t *testing.T) {}\nfunc TestOmega(t *testing.T) {}\n' >"${fixture}/core/internal/store/sqlstore/base_test.go"
	printf '//go:build review_extra\n\npackage sqlstore\nimport "testing"\nfunc TestTaggedExtra(t *testing.T) {}\n' >"${fixture}/core/internal/store/sqlstore/tagged_test.go"
	# The selected executable delegates to the installed Go and records every invocation.
	printf '#!/bin/bash\nprintf "%%s\\n" "$*" >>"${FLAG_CALLS}"\nexec %q "$@"\n' "${real_go}" >"${fixture}/chosen/go"
	printf '#!/bin/sh\nexit 79\n' >"${fixture}/decoy/go"
	chmod +x "${fixture}/chosen/go" "${fixture}/decoy/go"
	local helper="${fixture}/scripts/sqlstore-race-entry-partition.sh"
	for kind in argv environment; do
		good=1
		: >"${fixture}/runs-${kind}"
		for i in 1 2 3 4; do
			local -a extra=() environment=(GOFLAGS=)
			if [ "${kind}" = argv ]; then extra=(-tags review_extra); else environment=(GOFLAGS=-tags=review_extra); fi
			env GOWORK=off GOPROXY=off "${environment[@]}" OLIVARES_CORE_RACE_PARTITION="${i}" OLIVARES_CORE_RACE_PARTITIONS=4 \
				bash "${helper}" "${real_go}" test -count=1 "${extra[@]}" >>"${fixture}/runs-${kind}" 2>&1 || good=0
		done
		count="$(grep -c '^--- PASS: Test' "${fixture}/runs-${kind}" || true)"
		for name in TestAlpha TestBeta TestGamma TestOmega TestTaggedExtra; do
			[ "$(grep -c "^--- PASS: ${name} (" "${fixture}/runs-${kind}" || true)" = 1 ] || good=0
		done
		if [ "${good}" = 1 ] && [ "${count}" = 5 ]; then
			ok "${kind} build tags: all five compiled entries run exactly once" "four shards exit 0"
		else
			bad "${kind} build tags: compiled inventory and execution diverged" "passes=${count}, expected five distinct entries"
		fi
	done
	: >"${fixture}/calls"
	rc=0
	env GOWORK=off GOFLAGS= GOPROXY=off FLAG_CALLS="${fixture}/calls" PATH="${fixture}/decoy:${PATH}" \
		OLIVARES_CORE_RACE_PARTITION=1 OLIVARES_CORE_RACE_PARTITIONS=4 \
		bash "${helper}" "${fixture}/chosen/go" test -count=1 -tags=review_extra >"${fixture}/alternate" 2>&1 || rc=$?
	if [ "${rc}" = 0 ] && grep -q '^env GOFLAGS$' "${fixture}/calls" &&
		grep -q 'test .*review_extra.*-list' "${fixture}/calls" && grep -q 'test .*review_extra.*-run' "${fixture}/calls"; then
		ok "the selected Go path handles effective flags, discovery and execution" "PATH Go deliberately exits 79"
	else
		bad "the selected Go path was not used throughout" "exit ${rc}"
	fi
	# Every spelling must refuse before any go-test invocation, not merely fail a build.
	good=1
	: >"${fixture}/calls"
	for flag in -run=. --run=. -test.run=. --test.run=. -skip=. --skip=. -test.skip=. --test.skip=. \
		-list=. -test.list=. -fuzz=. -test.fuzz=. -bench=. -test.bench=. -args -c --c -json -exec=false \
		-short -test.short -failfast -shuffle=on -count=0 --count=0 -test.count=0 -count=2 -timeout=0 \
		./... ./other -unknown -tags; do
		rc=0
		env GOWORK=off GOFLAGS= FLAG_CALLS="${fixture}/calls" OLIVARES_CORE_RACE_PARTITION=1 OLIVARES_CORE_RACE_PARTITIONS=4 \
			bash "${helper}" "${fixture}/chosen/go" test -count=1 "${flag}" >"${fixture}/refusal" 2>&1 || rc=$?
		if [ "${rc}" != 2 ] || ! grep -q 'COULD NOT LOOK' "${fixture}/refusal"; then good=0; printf 'flag control failed: %s rc=%s\n' "${flag}" "${rc}"; fi
	done
	# Separated spelling was the actual zero-test green witness.
	rc=0
	env GOWORK=off GOFLAGS= FLAG_CALLS="${fixture}/calls" OLIVARES_CORE_RACE_PARTITION=1 OLIVARES_CORE_RACE_PARTITIONS=4 \
		bash "${helper}" "${fixture}/chosen/go" test -count=1 -skip . >"${fixture}/skip" 2>&1 || rc=$?
	[ "${rc}" = 2 ] || good=0
	if [ "${good}" = 1 ] && ! grep -q '^test ' "${fixture}/calls"; then
		ok "selection, runtime, compile-only and extra-package argv are refused" "all exit 2 before go test, including -skip ."
	else
		bad "an incompatible argv escaped early refusal" "see flag controls above"
	fi
	good=1
	: >"${fixture}/calls"
	for flag in '-skip=.' '--test.run=.' '-count=0' '-tags="review_extra"' '-tags=review_extra -skip=.'; do
		rc=0
		env GOWORK=off GOFLAGS="${flag}" FLAG_CALLS="${fixture}/calls" OLIVARES_CORE_RACE_PARTITION=1 OLIVARES_CORE_RACE_PARTITIONS=4 \
			bash "${helper}" "${fixture}/chosen/go" test -count=1 >"${fixture}/env-refusal" 2>&1 || rc=$?
		if [ "${rc}" != 2 ] || ! grep -q 'COULD NOT LOOK' "${fixture}/env-refusal"; then good=0; fi
	done
	if [ "${good}" = 1 ] && ! grep -q '^test ' "${fixture}/calls"; then
		ok "GOFLAGS selection and unsupported quoting are refused before tests" "no hidden environment selector"
	else
		bad "GOFLAGS escaped validation" "a hidden selector can erase coverage"
	fi
	printf 'GOFLAGS=-skip=.\n' >"${fixture}/goenv"
	rc=0
	env -u GOFLAGS GOWORK=off GOENV="${fixture}/goenv" OLIVARES_CORE_RACE_PARTITION=1 OLIVARES_CORE_RACE_PARTITIONS=4 \
		bash "${helper}" "${real_go}" test -count=1 >"${fixture}/goenv-refusal" 2>&1 || rc=$?
	if [ "${rc}" = 2 ] && grep -q 'GOFLAGS' "${fixture}/goenv-refusal"; then
		ok "effective persisted Go configuration cannot hide -skip" "GOFLAGS is unset in the process environment"
	else
		bad "persisted GOFLAGS escaped validation" "exit ${rc}"
	fi
	rc=0
	env GOWORK=off GOFLAGS=-p=7 bash "${helper}" --validate-argv "${real_go}" test -race -count=1 -p 1 -timeout 90m \
		>"${fixture}/ci-preflight" 2>&1 || rc=$?
	if [ "${rc}" = 0 ]; then ok "the exact CI argv and build GOFLAGS pass preflight" "no test execution requested"; else bad "the exact CI argv was rejected" "exit ${rc}"; fi
	: >"${fixture}/calls"
	rc=0
	env GOWORK=off GOFLAGS= FLAG_CALLS="${fixture}/calls" OLIVARES_CORE_RACE_PARTITION=1 OLIVARES_CORE_RACE_PARTITIONS=4 \
		bash "${fixture}/scripts/core-race-partition.sh" "${fixture}/chosen/go" test -skip . >"${fixture}/core-refusal" 2>&1 || rc=$?
	if [ "${rc}" = 2 ] && grep -q 'COULD NOT LOOK.*unsupported' "${fixture}/core-refusal" && ! grep -q '^test ' "${fixture}/calls"; then
		ok "core preflight refuses incompatible argv before either owned leg" "no sqlstore or package test invocation"
	else
		bad "core reached discovery or a leg before its argv refusal" "exit ${rc}"
	fi
	# Package discovery must use the selected executable/build flags as well: the fifth
	# complement package does not exist in the untagged compiled program.
	for name in alpha beta gamma omega tagged; do
		mkdir -p "${fixture}/core/${name}"
		if [ "${name}" = tagged ]; then printf '//go:build review_extra\n\n'; fi >"${fixture}/core/${name}/p.go"
		printf 'package %s\n' "${name}" >>"${fixture}/core/${name}/p.go"
	done
	: >"${fixture}/calls"
	good=1
	for i in 1 2 3 4; do
		env GOWORK=off GOFLAGS=-p=4 GOPROXY=off FLAG_CALLS="${fixture}/calls" PATH="${fixture}/decoy:${PATH}" \
			OLIVARES_CORE_RACE_PARTITION="${i}" OLIVARES_CORE_RACE_PARTITIONS=4 \
			bash "${fixture}/scripts/core-race-partition.sh" "${fixture}/chosen/go" test --count=1 --tags=review_extra --timeout=90m \
			>>"${fixture}/core-tagged" 2>&1 || good=0
	done
	for name in alpha beta gamma omega tagged; do
		[ "$(grep '^test ' "${fixture}/calls" | tr ' ' '\n' | grep -cx "fixture/${name}" || true)" = 1 ] || good=0
	done
	count="$(grep -c '^--- PASS: Test' "${fixture}/core-tagged" || true)"
	if [ "${good}" = 1 ] && [ "${count}" = 5 ]; then
		ok "core build tags preserve all five entries and five complement packages" "same chosen Go, each package once, all four shards pass"
	else
		bad "core package discovery ignored the selected Go or build flags" "sqlstore passes=${count}"
	fi
}
flag_controls
if [ "${1:-}" = --flag-controls ]; then
	printf 'flag controls: %s passed, %s failed\n' "${PASSED}" "${FAILED}"
	[ "${FAILED}" = 0 ]
	exit
fi

# A synthetic inventory that carries the shape that matters: PREFIX PAIRS. TestAlpha is a
# prefix of TestAlphaLonger. If the helper ever drops an anchor, a partition owning TestAlpha
# runs TestAlphaLonger too and both partitions report it.
INV="${TMP}/inv"
{
	printf 'TestAlpha\n'
	printf 'TestAlphaLonger\n'
	printf 'TestBeta\n'
	printf 'TestGamma\n'
	printf 'ExampleDelta\n'
	printf 'FuzzEpsilon\n'
	printf 'TestZeta\n'
} >"${INV}"
TOTAL=7

# ── A. UNION, OWNERSHIP AND ANCHORING ────────────────────────────────────────────────────
for n in 2 3 4; do
	all="${TMP}/all-${n}"
	: >"${all}"
	empty=0
	i=1
	while [ "${i}" -le "${n}" ]; do
		if ! "${SUT}" --assign "${i}" "${n}" <"${INV}" >"${TMP}/p-${n}-${i}" 2>/dev/null; then
			empty=1
			break
		fi
		[ -s "${TMP}/p-${n}-${i}" ] || empty=1
		cat "${TMP}/p-${n}-${i}" >>"${all}"
		i=$((i + 1))
	done
	if [ "${empty}" = 1 ]; then
		bad "n=${n}: a partition was empty or --assign failed" "coverage cannot be established"
		continue
	fi
	got="$(grep -c . "${all}" || true)"
	uniq_got="$(LC_ALL=C sort -u "${all}" | grep -c . || true)"
	missing="$(LC_ALL=C sort "${INV}" | comm -23 - <(LC_ALL=C sort -u "${all}") | grep -c . || true)"
	if [ "${got}" = "${TOTAL}" ] && [ "${uniq_got}" = "${TOTAL}" ] && [ "${missing}" = 0 ]; then
		ok "n=${n}: exact disjoint union of the inventory" "${got} entries, 0 duplicated, 0 unowned"
	else
		bad "n=${n}: not a partition" "got ${got}, unique ${uniq_got}, unowned ${missing}"
	fi
done

# Every entry KIND is owned: an Example or a Fuzz seed dropped here is coverage lost silently.
allkinds="$(cat "${TMP}/p-4-1" "${TMP}/p-4-2" "${TMP}/p-4-3" "${TMP}/p-4-4" 2>/dev/null || true)"
for kind in ExampleDelta FuzzEpsilon; do
	case "
${allkinds}
" in
	*"
${kind}
"*)
		ok "the ${kind%%[A-Z]*} entry ${kind} is owned" "kind preserved"
		;;
	*)
		bad "${kind} is owned by nobody" "a non-Test entry point was dropped"
		;;
	esac
done

if "${SUT}" --assign 1 4 <"${INV}" >"${TMP}/sel" 2>/dev/null && [ -s "${TMP}/sel" ]; then
	ok "--assign returns a selection for partition 1 of 4" "$(grep -c . "${TMP}/sel") entry(ies)"
else
	bad "--assign failed for partition 1 of 4" "the placement is unusable"
fi

# ── B. REFUSALS ──────────────────────────────────────────────────────────────────────────
rcof() { # <expected> <label> ...env/argv
	local want="$1" label="$2" rc=0
	shift 2
	"$@" >/dev/null 2>&1 || rc=$?
	if [ "${rc}" = "${want}" ]; then
		ok "${label}" "exit ${rc}"
	else
		bad "${label}" "exit ${rc}, wanted ${want}"
	fi
}
rcof 2 "half a selector pair is refused" env OLIVARES_CORE_RACE_PARTITION=1 "${SUT}" --names
rcof 2 "a malformed selector (08) is refused" env OLIVARES_CORE_RACE_PARTITION=08 OLIVARES_CORE_RACE_PARTITIONS=4 "${SUT}" --names
rcof 2 "an out-of-range index is refused" env OLIVARES_CORE_RACE_PARTITION=5 OLIVARES_CORE_RACE_PARTITIONS=4 "${SUT}" --names
rcof 2 "an unknown mode is refused" "${SUT}" --nonsense
rcof 2 "no argv and no mode is refused" "${SUT}"

if printf '' | "${SUT}" --check 4 - >/dev/null 2>&1; then
	bad "an empty inventory was accepted" "a run that tests nothing would report success"
else
	rc=$?
	if [ "${rc}" = 2 ]; then
		ok "an empty inventory is COULD NOT LOOK, not an empty pass" "exit 2"
	else
		bad "an empty inventory exited ${rc}" "wanted 2"
	fi
fi

if printf 'TestOnly\n' | "${SUT}" --check 4 - >/dev/null 2>&1; then
	bad "4 partitions over 1 entry was accepted" "three legs would test nothing and exit 0"
else
	rc=$?
	[ "${rc}" = 1 ] && ok "more partitions than entries is a property failure" "exit 1" ||
		bad "more partitions than entries exited ${rc}" "wanted 1"
fi

# ── C. ANCHORING, DRIVEN THROUGH --regex AND A REAL RUN ──────────────────────────────────
SYN="${TMP}/syn"
mkdir -p "${SYN}/core/anchorfix" || blind "the synthetic package could not be created"
{
	printf 'module anchorsyn

go 1.24
'
} >"${SYN}/core/go.mod"
{
	printf 'package anchorfix

import (
	"fmt"
	"testing"
)

'
	printf 'func TestAlpha(t *testing.T) {}
'
	printf 'func TestAlphaLonger(t *testing.T) {}
'
	printf 'func TestBeta(t *testing.T) {}
'
	printf 'func TestXTestBeta(t *testing.T) {}
'
	printf 'func Example() {
    fmt.Println("x")
    // Output: x
}
'
	printf 'func FuzzThing(f *testing.F) { f.Add("s"); f.Fuzz(func(t *testing.T, s string) {}) }
'
} >"${SYN}/core/anchorfix/a_test.go"
printf 'package anchorfix
' >"${SYN}/core/anchorfix/a.go"

synhelper() { # <extra sed> -> path to a helper rooted at the synthetic tree
	local extra="${1:-}" out="${TMP}/syn-helper-${2:-base}.sh"
	sed -e "s|^ROOT=.*|ROOT=\"${SYN}\"|" -e 's|^PKG=.*|PKG=./anchorfix|' \
		-e 's|set -- go test -race -count=1|set -- go test -count=1|' ${extra:+-e "${extra}"} \
		"${SUT}" >"${out}"
	chmod +x "${out}"
	printf '%s' "${out}"
}

BASE_H="$(synhelper "" base)"
if OLIVARES_CORE_RACE_PARTITION=1 OLIVARES_CORE_RACE_PARTITIONS=2 \
	bash "${BASE_H}" --regex >"${TMP}/rx" 2>"${TMP}/rxerr"; then
	RX="$(cat "${TMP}/rx")"
	case "${RX}" in
	'^('*')$') ok "--regex anchors at BOTH ends" "${RX}" ;;
	*) bad "--regex is not anchored at both ends" "${RX}" ;;
	esac
else
	bad "--regex failed on the synthetic package" "$(tail -1 "${TMP}/rxerr")"
fi

OWNER=""
for i in 1 2; do
	if ! names="$(OLIVARES_CORE_RACE_PARTITION="${i}" OLIVARES_CORE_RACE_PARTITIONS=2 \
		bash "${BASE_H}" --names 2>/dev/null)"; then
		continue
	fi
	case "
${names}
" in
	*"
TestAlpha
"*)
		OWNER="${i}"
		break
		;;
	esac
done
if [ -n "${OWNER}" ]; then
	RUN_RC=0
	OLIVARES_CORE_RACE_PARTITION="${OWNER}" OLIVARES_CORE_RACE_PARTITIONS=2 \
		bash "${BASE_H}" go test -count=1 >"${TMP}/run" 2>&1 || RUN_RC=$?
	RAN_A="$(grep -cE -- '^--- PASS: TestAlpha \(' "${TMP}/run" || true)"
	BLEED="$(grep -cE -- '^--- PASS: TestAlphaLonger \(' "${TMP}/run" || true)"
	if [ "${RUN_RC}" = 0 ] && [ "${RAN_A}" = 1 ] && [ "${BLEED}" = 0 ]; then
		ok "the anchored run selects TestAlpha with NO sibling bleed" "partition ${OWNER}: 1 selected, 0 bled"
	else
		bad "the anchored run bled into a prefix sibling" "TestAlpha=${RAN_A} TestAlphaLonger=${BLEED}"
	fi
else
	bad "no partition owned TestAlpha" "the synthetic discovery is unusable"
fi

for m in start end; do
	if [ "${m}" = "start" ]; then
		H="$(synhelper 's|REGEX="\^(\${body})\\$"|REGEX="(${body})\\$"|' nostart)"
	else
		H="$(synhelper 's|REGEX="\^(\${body})\\$"|REGEX="^(${body})"|' noend)"
	fi
	if cmp -s "${BASE_H}" "${H}"; then
		bad "MUTATION DID NOT APPLY: the ${m} anchor" "the anchor moved; this case proves nothing"
		continue
	fi
	OLIVARES_CORE_RACE_PARTITION="${OWNER:-1}" OLIVARES_CORE_RACE_PARTITIONS=2 \
		bash "${H}" go test -count=1 >"${TMP}/mrun-${m}" 2>&1 || true
	if [ "${m}" = "start" ]; then
		MB="$(grep -cE -- '^--- PASS: TestXTestBeta \(' "${TMP}/mrun-${m}" || true)"
	else
		MB="$(grep -cE -- '^--- PASS: TestAlphaLonger \(' "${TMP}/mrun-${m}" || true)"
	fi
	if [ "${MB}" -gt 0 ]; then
		ok "mutant: dropping the ${m} anchor bleeds, and the check sees it" "bleed count ${MB}"
	else
		bad "dropping the ${m} anchor did not bleed" "this case cannot detect an unanchored regex"
	fi
done

mkdir -p "${TMP}/empty-go" "${TMP}/failed-go"
printf '#!/bin/sh\nif [ "$1" = env ]; then exit 0; fi\nprintf "ok fixture 0.001s\\n"\n' >"${TMP}/empty-go/go"
printf '#!/bin/sh\nif [ "$1" = env ]; then exit 0; fi\nprintf "owned-discovery-build-failure\\n" >&2\nexit 23\n' >"${TMP}/failed-go/go"
chmod +x "${TMP}/empty-go/go" "${TMP}/failed-go/go"
for kind in empty failed; do
	DISCOVERY_RC=0
	PATH="${TMP}/${kind}-go:${PATH}" "${SUT}" --inventory >"${TMP}/discovery-${kind}" 2>&1 || DISCOVERY_RC=$?
	if [ "${kind}" = empty ]; then SIGNAL='SUCCEEDED.*NO Test/Example/Fuzz'; else SIGNAL='DISCOVERY/BUILD FAILED.*exit 23'; fi
	if [ "${DISCOVERY_RC}" = 2 ] && grep -q 'COULD NOT LOOK' "${TMP}/discovery-${kind}" && grep -qE "${SIGNAL}" "${TMP}/discovery-${kind}"; then
		ok "${kind} discovery has the exact observation-failure status" "exit 2 with named cause"
	else
		bad "${kind} discovery lost its status or diagnostic" "exit ${DISCOVERY_RC}"
	fi
done

KINDS_OK=1
bash "${BASE_H}" --inventory >"${TMP}/synthetic-inventory" 2>&1 || KINDS_OK=0
: >"${TMP}/synthetic-runs"
for i in 1 2; do
	OLIVARES_CORE_RACE_PARTITION="${i}" OLIVARES_CORE_RACE_PARTITIONS=2 \
		bash "${BASE_H}" go test -count=1 >>"${TMP}/synthetic-runs" 2>&1 || KINDS_OK=0
done
for name in Example FuzzThing; do
	grep -qx "${name}" "${TMP}/synthetic-inventory" || KINDS_OK=0
	COUNT="$(grep -cF -- "--- PASS: ${name} (" "${TMP}/synthetic-runs" || true)"
	[ "${COUNT}" = 1 ] || KINDS_OK=0
done
if [ "${KINDS_OK}" = 1 ]; then
	ok "runnable Example and Fuzz seed corpus execute exactly once" "both partitions exit 0"
else
	bad "Example or Fuzz ownership or execution is incomplete" "inspect synthetic inventory and real terminal output"
fi

# ── D. RUN MODE ──────────────────────────────────────────────────────────────────────────
rcof 1 "a failing argv propagates its status" "${SUT}" false
if "${SUT}" echo >"${TMP}/argv" 2>/dev/null && grep -q -- './internal/store/sqlstore' "${TMP}/argv" && ! grep -q -- '-run' "${TMP}/argv"; then
	ok "no selector runs the COMPLETE package with no -run" "$(cat "${TMP}/argv")"
else
	bad "the unpartitioned argv is not the complete package" "$(cat "${TMP}/argv" 2>/dev/null)"
fi

# ── E. CORE HELPER CONSUMES THIS ONE ─────────────────────────────────────────────────────
# Unpartitioned core must NOT invoke this helper: that would race sqlstore twice locally.
if grep -q 'bash scripts/sqlstore-race-entry-partition.sh "$@"' "${CORE}"; then
	ok "core-race-partition invokes the sqlstore entry helper by exact path" "partitioned run mode"
else
	bad "core-race-partition does not invoke the sqlstore helper" "the four jobs would not shard sqlstore"
fi
if grep -q 'with-pg-env.sh bash scripts/core-race-partition.sh go test -race' "${TASKFILE}" &&
	! grep -q 'sqlstore-race-entry-partition.sh go test' "${TASKFILE}"; then
	ok "the Taskfile still enters core-race-partition through the PG wrapper once" "no second unpartitioned sqlstore recipe"
else
	bad "the Taskfile recipe no longer matches the single-wrapper contract" "local ./... would duplicate or drop PG"
fi

# Continue-after-failure: a failing sqlstore shard must still run owned packages, and a
# failing package argv must still run the sqlstore shard. A stub replaces the live entry
# helper so this does not compile sqlstore; the core copy still does live `go list`.
REC="${TMP}/record.sh"
{
	printf '%s\n' '#!/bin/sh'
	printf '%s\n' 'printf "RAN:%s\n" "$*"'
	printf '%s\n' 'case " $* " in'
	printf '%s\n' '*" -run "*) echo SQLSTORE_ARGV; exit "${SQLSTORE_RC:-0}" ;;'
	printf '%s\n' '*) echo PACKAGE_ARGV; exit "${PACKAGE_RC:-0}" ;;'
	printf '%s\n' 'esac'
} >"${REC}"
chmod +x "${REC}"
STUB="${TMP}/sqlstore-stub.sh"
{
	printf '%s\n' '#!/bin/sh'
	printf '%s\n' 'if [ "$1" = --validate-argv ]; then exit 0; fi'
	printf '%s\n' 'exec "$@" -v -p 1 -run "^(TestAlpha)$" ./internal/store/sqlstore'
} >"${STUB}"
chmod +x "${STUB}"
CORE_MUT="${TMP}/core-mut.sh"
sed -e "s|^ROOT=.*|ROOT=\"${ROOT}\"|" \
	-e "s|bash scripts/sqlstore-race-entry-partition.sh|bash ${STUB}|" \
	-e 's|select_packages "$@"|select_packages|' \
	"${CORE}" >"${CORE_MUT}"
if cmp -s "${CORE}" "${CORE_MUT}"; then
	bad "MUTATION DID NOT APPLY: stub the sqlstore helper" "continue-after-failure proves nothing"
else
	SQLSTORE_RC=1 PACKAGE_RC=0 OLIVARES_CORE_RACE_PARTITION=1 OLIVARES_CORE_RACE_PARTITIONS=2 \
		bash "${CORE_MUT}" "${REC}" >"${TMP}/cont-sql" 2>"${TMP}/cont-sql.err" || true
	if grep -q SQLSTORE_ARGV "${TMP}/cont-sql" && grep -q PACKAGE_ARGV "${TMP}/cont-sql"; then
		ok "a failing sqlstore shard still runs owned packages" "both argv ran"
	else
		bad "a failing sqlstore shard skipped owned packages" "$(tr '\n' ' ' <"${TMP}/cont-sql")"
	fi
	SQLSTORE_RC=0 PACKAGE_RC=1 OLIVARES_CORE_RACE_PARTITION=1 OLIVARES_CORE_RACE_PARTITIONS=2 \
		bash "${CORE_MUT}" "${REC}" >"${TMP}/cont-pkg" 2>"${TMP}/cont-pkg.err" || true
	if grep -q SQLSTORE_ARGV "${TMP}/cont-pkg" && grep -q PACKAGE_ARGV "${TMP}/cont-pkg"; then
		ok "a failing package argv still runs the sqlstore shard" "both argv ran"
	else
		bad "a failing package argv skipped the sqlstore shard" "$(tr '\n' ' ' <"${TMP}/cont-pkg")"
	fi
	cont_rc=0
	SQLSTORE_RC=0 PACKAGE_RC=0 OLIVARES_CORE_RACE_PARTITION=1 OLIVARES_CORE_RACE_PARTITIONS=2 \
		bash "${CORE_MUT}" "${REC}" >"${TMP}/cont-ok" 2>"${TMP}/cont-ok.err" || cont_rc=$?
	if [ "${cont_rc}" = 0 ] && grep -q SQLSTORE_ARGV "${TMP}/cont-ok" && grep -q PACKAGE_ARGV "${TMP}/cont-ok"; then
		ok "both owned invocations succeeding is exit 0" "sqlstore then packages"
	else
		bad "the combined partitioned run should be exit 0 when both succeed" "rc=${cont_rc}"
	fi
	both_rc=0
	SQLSTORE_RC=7 PACKAGE_RC=3 OLIVARES_CORE_RACE_PARTITION=1 OLIVARES_CORE_RACE_PARTITIONS=2 \
		bash "${CORE_MUT}" "${REC}" >"${TMP}/cont-both" 2>"${TMP}/cont-both.err" || both_rc=$?
	if [ "${both_rc}" = 7 ] && grep -q SQLSTORE_ARGV "${TMP}/cont-both" && grep -q PACKAGE_ARGV "${TMP}/cont-both"; then
		ok "both failing still runs both and returns the sqlstore status" "rc=7, not the later 3"
	else
		bad "combined failure lost a status or skipped work" "rc=${both_rc} $(tr '\n' ' ' <"${TMP}/cont-both")"
	fi
fi

# ── F. REAL DISCOVERY, PAID ONCE ─────────────────────────────────────────────────────────
if "${SUT}" --check 4 >"${TMP}/real" 2>&1; then
	n="$(grep -oE '[0-9]+ entry point' "${TMP}/real" | grep -oE '[0-9]+' | head -1 || true)"
	if [ -n "${n}" ] && [ "${n}" -gt 100 ]; then
		ok "real discovery under -race partitions the live package" "${n} entry points, union clean"
	else
		bad "real discovery returned an implausible inventory" "$(head -1 "${TMP}/real")"
	fi
else
	blind "real discovery failed: $(tail -1 "${TMP}/real")"
fi

TOTAL_RUN=$((PASSED + FAILED))
if [ "${TOTAL_RUN}" != "${DECLARED_CASES}" ]; then
	printf 'test-sqlstore-race-entry-partition: COULD NOT LOOK — %s case(s) ran, %s declared.\n' \
		"${TOTAL_RUN}" "${DECLARED_CASES}" >&2
	exit 2
fi
printf 'test-sqlstore-race-entry-partition: %s passed, %s failed (%s declared)\n' "${PASSED}" "${FAILED}" "${DECLARED_CASES}"
[ "${FAILED}" = 0 ]
