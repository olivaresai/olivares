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

# --- release.yml: the phase-2 dispatch is a PUBLICATION ceremony (QA07) ----------------
# The wording is load-bearing rather than cosmetic: the `ota-release-ceremony` environment's
# reviewers are the only human gate on this operation, and until QA07 what they approved was
# signing and attachment. Now approving makes a release public. A prompt that still described
# the old meaning would be asking a person to consent to something else.
p2="$(job publish-ota-manifest "$R")"
[ -n "$p2" ]
check "release.yml has the phase-2 publication job" "the ceremony exists" $?
printf '%s' "$p2" | grep 'actions: read' >/dev/null
check "phase 2 may read the run its candidate came from" "actions: read, bounded" $?
printf '%s' "$p2" | grep -c ': write' | awk '{exit !($1 == 1)}'
check "and it still holds exactly ONE write permission" "no widening beside it" $?
printf '%s' "$p2" | grep '^          bash scripts/release-finalize-stable.sh "' >/dev/null
check "phase 2 invokes the publication finalizer" "the ceremony finishes its own job" $?
[ -f "$ROOT/scripts/release-finalize-stable.sh" ]
check "the finalizer script exists in the tree" "not a dangling invocation" $?
# ORDER, not mere presence: publishing before the custody upload would publish a release
# whose verified pair is not on it yet.
# ⛔ THE INVOCATION, NOT A MENTION OF IT. Both script names also appear in prose and inside
# a diagnostic `echo` — the reconciliation step prints the finalizer command for an operator
# — so a loose grep found the ECHO first and reported the order backwards. The anchor is the
# run-block indentation plus the exact argument list, which only an invocation has.
_att="$(printf '%s' "$p2" | grep -n '^          bash scripts/release-attach-stable-pair.sh "' | head -1 | cut -d: -f1)"
_fin="$( { printf '%s' "$p2" | grep -n '^          bash scripts/release-finalize-stable.sh "' || true; } | awk -F: 'NR == 1 { print $1 }')"
[ -n "$_att" ] && [ -n "$_fin" ] && [ "$_att" -lt "$_fin" ]
check "publication comes AFTER the verified pair is attached" "order is the contract" $?
# The reconciliation step must precede the signer: re-signing and clobbering a published
# release is the one effect in this chain that cannot be undone.
_rec="$(printf '%s' "$p2" | grep -n 'name: reconcile the release state before signing' | head -1 | cut -d: -f1)"
_sign="$(printf '%s' "$p2" | grep -n 'name: sign the OTA manifest in-job' | head -1 | cut -d: -f1)"
[ -n "$_rec" ] && [ -n "$_sign" ] && [ "$_rec" -lt "$_sign" ]
check "the published-state reconciliation precedes the signer" "no re-sign of a public release" $?
# --- the SLSA verifier prerequisite, and WHERE it may sit (root return 03) ---------------
# scripts/verify-release.sh --strict-publication FAILS when slsa-verifier is absent, so the
# tool is a deployment prerequisite of this job rather than a convenience. Its placement is
# the load-bearing part: every later `run:` consumes the tree inside the untouched-checkout
# assertion, and no step boundary — therefore no `uses:` — may be scheduled inside that
# window. This pins the ordering, not the presence.
printf '%s' "$p2" | grep 'uses: slsa-framework/slsa-verifier/actions/installer@[0-9a-f]\{40\} #' >/dev/null
check "phase 2 installs slsa-verifier from a SHA-pinned action" "the strict-mode prerequisite" $?
_inst="$(printf '%s' "$p2" | grep -n 'uses: slsa-framework/slsa-verifier/actions/installer@' | head -1 | cut -d: -f1)"
_guard="$(printf '%s' "$p2" | grep -n 'name: assert the checkout is untouched before anything consumes it (phase 2)' | head -1 | cut -d: -f1)"
[ -n "$_inst" ] && [ -n "$_guard" ] && [ "$_inst" -lt "$_guard" ]
check "the installer runs BEFORE the untouched-checkout guard" "never inside the custody window" $?
# LAST `uses:` OF THE JOB. A later action would reopen the window whatever its name is, so the
# rule is positional and is asserted positionally.
_lastuses="$(printf '%s' "$p2" | grep -n '^      - uses:' | tail -1 | cut -d: -f1)"
[ -n "$_lastuses" ] && [ "$_lastuses" -eq "$_inst" ]
check "and it is the LAST action of the job" "no step boundary after it" $?
# The installed binary is asserted by digest at execution time, like the cosign binary: a
# SHA-pinned installer still resolves its VERSION through the tags API.
_assert="$(printf '%s' "$p2" | grep -n 'name: assert the slsa-verifier binary is the reviewed artifact' | head -1 | cut -d: -f1)"
[ -n "$_assert" ] && [ "$_inst" -lt "$_assert" ] && [ "$_assert" -lt "$_guard" ]
check "its digest assertion runs between the installer and the guard" "resolved, hashed, compared" $?
printf '%s' "$p2" | grep "EXPECTED_SLSA_VERIFIER_SHA256: '946dbec729094195e88ef78e1734324a27869f03e2c6bd2f61cbc06bd5350339'" >/dev/null
check "and it pins the upstream published linux/amd64 digest" "the reviewed artifact, by bytes" $?
# SIGPIPE: `producer | grep -q` exits 141 when grep closes the pipe on a match, and under
# pipefail that reads as a failure on success. The check consumes its input whole.
! printf '%s' "$p2" | grep -E 'version_out.*\|.*grep" -q' >/dev/null
check "the version predicate drains its producer" "no 141 on a successful match" $?

grep -q 'description: "PUBLISH this draft release tag' "$R"
check "the dispatch input says it PUBLISHES" "the approver is told what they approve" $?
grep -q 'authorizes PUBLICATION of the release named by' "$R"
check "the authorization contract names publication" "written, not implied" $?
# The goreleaser recipe must still keep the draft — the finalizer is what undrafts it, and a
# config that auto-published would route around the protected environment entirely.
grep -qE '^  draft: true' "$ROOT/.goreleaser.yaml"
check "the build config still creates a DRAFT" "publication stays in the protected job" $?
# ONE DRAFT PER TAG. Without this line a re-run of phase 1 creates a SECOND draft for the same
# tag while the producers upload to another, which is what happened to v26.9.0 on 2026-09-17:
# the build's signed assets on one draft, the producers' output on the other, and no complete
# candidate anywhere. `replace_existing_draft` is a no-op on a tag's first run and only ever
# removes a draft, never a published release (GoReleaser v2.17.0, gated on `draft: true`).
grep -qE '^  replace_existing_draft: true' "$ROOT/.goreleaser.yaml"
check "a re-run replaces the previous draft instead of adding one" "one draft per tag" $?
# SAME-TAG SERIALIZATION SURVIVES THE NEW STEP. Phase 1 creates the draft and phase 2 now
# publishes it; without one concurrency group over BOTH, a tag run and a publication of the
# same release can interleave, and the QA contrast already measured what that buys — a
# ceremony finishing rc=0 over a draft another writer had just changed. `cancel-in-progress:
# false` because a publisher is never cancelled.
grep -q 'group: release-${{ github.event_name ==' "$R" && grep -q 'cancel-in-progress: false' "$R"
check "both phases share one same-tag concurrency group" "publication is serialized too" $?
grep -q 'release-build-context.json' "$ROOT/.goreleaser.yaml"
check "the build context is declared in the goreleaser recipe" "checksummed and uploaded" $?
[ "$(grep -c 'glob: release-build-context.json' "$ROOT/.goreleaser.yaml")" -eq 2 ]
check "and in BOTH extra_files declarations" "neither implies the other in v2.17.0" $?

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
