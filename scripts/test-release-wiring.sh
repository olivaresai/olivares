#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for the AUTHORIZATION WIRING of the two release workflows (R1 contrast P1-02 /
# P2-02 / P3-02). The §C.4 contract is structural — "the preflight is a read-only job
# that runs first, and every mutating job depends on it and consumes its outputs" — so a
# script-level battery cannot see it drift: the preflight script stayed green while the
# production caller ran it as a step INSIDE the already-privileged build job and no
# sibling depended on it. These checks pin the named structures in the YAML text; they
# are honest LINTS on declared shape (a determined edit can satisfy the string and break
# the semantics — the adversarial reviewer owns that class), but they turn the exact
# regression Codex found into a red battery instead of a re-discovery.
#
# OLIVARES_WIRING_WORKFLOWS overrides the workflow dir; the red-first proof points it at
# the pre-fix tree.
#
# `grep >/dev/null`, never `grep -q`, on anything PIPED here: under pipefail a -q that
# exits at the first match SIGPIPEs the producer and turns a MATCH into exit 141 — this
# battery's own first run failed its longest job block exactly that way.
#
# NO `set -e` (battery reports through check(); see test-pg-test-env.sh).
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WFDIR="${OLIVARES_WIRING_WORKFLOWS:-$ROOT/.github/workflows}"
R="$WFDIR/release.yml"
RR="$WFDIR/release-rehearsal.yml"

pass=0
fail=0
check() {
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1))
		printf '  ok    %-62s %s\n' "$1" "$2"
	else
		fail=$((fail + 1))
		printf '  FAIL  %-62s %s\n' "$1" "$2"
	fi
}

# job NAME FILE — print the block of one top-level job (2-space indent) up to the next.
job() {
	awk -v j="$1" '
		$0 ~ "^  "j":$" { p = 1; print; next }
		p && /^  [a-zA-Z_-]+:$/ { p = 0 }
		p { print }
	' "$2"
}

echo "release authorization wiring — §C.4 as structure, not narrative"

[ -f "$R" ]
check "the production release workflow exists" "release.yml" $?

# The rehearsal workflow is INTERNAL infrastructure and is excluded from the public
# export (scripts/export-public.sh GITHUB_BLOCK): in the exported tree its checks skip
# with the note below. In the dev tree the file exists and its checks always run —
# removing it there would surface here as the skip note appearing where it never does.
HAVE_RR=0
[ -f "$RR" ] && HAVE_RR=1

# --- release.yml: the preflight is a separate READ-ONLY job ----------------------------
pf="$(job preflight "$R")"
[ -n "$pf" ]
check "release.yml has a dedicated preflight JOB (not a step)" "P1-02 root" $?
printf '%s' "$pf" | grep 'contents: read' >/dev/null && ! printf '%s' "$pf" | grep -E ': write' >/dev/null
check "the production preflight job holds NO write permission" "read-only root" $?
printf '%s' "$pf" | grep 'outputs:' >/dev/null
check "the production preflight exposes job outputs" "§C.4.10" $?
printf '%s' "$pf" | grep "if: github.event_name == 'push'" >/dev/null &&
	! printf '%s' "$pf" | grep 'if:' | grep 'github.repository' >/dev/null
check "production preflight is event-scoped only — wrong repo fails RED, never skips" "P2-02" $?

# --- release.yml: every mutating job depends on preflight and is governed by it --------
for j in goreleaser provenance-binaries provenance-image promote-latest mirror-dockerhub; do
	job "$j" "$R" | grep 'needs: \[preflight' >/dev/null
	check "release.yml job '$j' needs the preflight" "authorization root" $?
done
job goreleaser "$R" | grep -c 'needs\.preflight\.outputs\.' | awk '{exit !($1 >= 5)}'
check "the build job consumes the validated outputs (not literals)" ">=5 refs" $?
job promote-latest "$R" | grep "publish_latest == 'true'" >/dev/null
check "PUBLISH_LATEST governs promote-latest" "load-bearing switch" $?
job provenance-binaries "$R" | grep "run_slsa == 'true'" >/dev/null &&
	job provenance-image "$R" | grep "run_slsa == 'true'" >/dev/null
check "RUN_SLSA governs both provenance jobs" "load-bearing switch" $?
job mirror-dockerhub "$R" | grep "publish_dockerhub != 'false'" >/dev/null
check "PUBLISH_DOCKERHUB governs the Docker Hub mirror" "load-bearing switch" $?
grep -q "publish_ota_stable == 'true'" "$R"
check "PUBLISH_OTA_STABLE governs the phase-1 stable draft upload" "load-bearing switch" $?
# The contract comment sits ABOVE the job key (block comments precede jobs), so this
# check is file-scoped on the marker string.
grep 'AUTHORIZATION CONTRACT OF THIS DISPATCH' "$R" >/dev/null
check "the OTA dispatch declares its separate authorization contract" "written, not implied" $?

# --- release.yml: promotion aliases through the digest-asserting script ----------------
job promote-latest "$R" | grep 'scripts/alias-image-digest.sh' >/dev/null
check "latest* aliases go through the digest-asserting alias script" "P1-01" $?
! job promote-latest "$R" | grep 'imagetools create' >/dev/null
check "no bare imagetools create remains in promote-latest" "no unasserted alias" $?

# --- release-rehearsal.yml: red guard + serialization (internal tree only) -------------
if [ "$HAVE_RR" -eq 1 ]; then
	rpf="$(job preflight "$RR")"
	[ -n "$rpf" ] && ! printf '%s' "$rpf" | grep '^    if:' >/dev/null
	check "rehearsal preflight carries NO job if: (wrong repo fails RED)" "P2-02" $?
	grep -q 'group: release-rehearsal-' "$RR" && grep -q 'cancel-in-progress: false' "$RR"
	check "rehearsal runs serialize per ref and never cancel a publisher" "P3-02" $?
	# The hard-guard literal must LIVE in this internal file (the public preflight is
	# deny-closed and embeds none). This battery ships publicly, so it must not embed
	# the internal name either: it EXTRACTS the injected expectation and the build
	# job's execution-time pin from the workflow and requires them non-empty, equal,
	# and never a production surface.
	exp="$(grep -o 'OLIVARES_REHEARSAL_EXPECTED_REPO: [^ ]*' "$RR" | head -1 | cut -d' ' -f2)"
	pin="$(job goreleaser-rehearsal "$RR" | grep -o '"\${RELEASE_GITHUB_REPO}" = "[^"]*"' | head -1 | sed 's/.*= "//; s/"$//')"
	[ -n "$exp" ] && [ -n "$pin" ] && [ "$exp" = "$pin" ] &&
		case "$exp" in *olivaresai* | *docker.io*) false ;; *) true ;; esac
	check "the rehearsal identity is pinned literal in the internal workflow" "hard guard home" $?
else
	echo "  note  release-rehearsal.yml not present — internal-only file (public export); its checks skip"
fi

echo ""
echo "release wiring battery: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
