#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for scripts/check-publish-target.sh — the destination fence of the side channels.
#
# Every accepted case is paired with the refusal that proves it was not accepted by accident, and
# the preprod identity is a NEUTRAL stand-in: this file ships in the exported tree, so the real
# preprod name must never appear here. It deliberately contains "olivares" but NOT "olivaresai",
# because the production-surface tripwire matches the production owner as a substring and a
# stand-in that tripped it would make the accepted case red for the wrong reason.
set -u

SCRIPT="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/check-publish-target.sh"
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

PRE_ID="preprod-org/olivares-preprod"
PROD_T="ghcr.io/olivaresai/charts"
PRE_T="ghcr.io/$PRE_ID/charts"

run() { env -i PATH="$PATH" "$@" sh "$SCRIPT" >/dev/null 2>"$errf"; rc=$?; }
errf="$(mktemp)"
trap 'rm -f "$errf"' EXIT HUP INT TERM

echo "check-publish-target — reviewed destinations and refused cross-products"

run RELEASE_PROFILE=production PUBLISH_TARGET="$PROD_T" PRODUCTION_TARGET="$PROD_T" GITHUB_REPOSITORY=olivaresai/olivares
check "production publishing to the reviewed coordinate is accepted" "exit 0" $([ "$rc" -eq 0 ] && echo 0 || echo 1)

run RELEASE_PROFILE=production PUBLISH_TARGET="ghcr.io/somewhere/else" PRODUCTION_TARGET="$PROD_T" GITHUB_REPOSITORY=olivaresai/olivares
[ "$rc" -ne 0 ] && grep -q 'reviewed production destination' "$errf"
check "production publishing anywhere else refuses" "exact coordinate" $?

run RELEASE_PROFILE=preprod PUBLISH_TARGET="$PRE_T" PRODUCTION_TARGET="$PROD_T" PREPROD_TARGET="$PRE_T" \
	PREPROD_EXPECTED_REPO="$PRE_ID" GITHUB_REPOSITORY="$PRE_ID"
check "preprod with an injected destination it runs in is accepted" "exit 0" $([ "$rc" -eq 0 ] && echo 0 || echo 1)
[ "$rc" -ne 0 ] && sed -n '1,2p' "$errf"

run RELEASE_PROFILE=preprod PUBLISH_TARGET="$PRE_T" PRODUCTION_TARGET="$PROD_T" \
	PREPROD_EXPECTED_REPO="$PRE_ID" GITHUB_REPOSITORY="$PRE_ID"
[ "$rc" -ne 0 ] && grep -q 'PREPROD_TARGET is required' "$errf"
check "preprod WITHOUT an injected destination refuses (deny-closed)" "no embedded name" $?

run RELEASE_PROFILE=preprod PUBLISH_TARGET="$PROD_T" PRODUCTION_TARGET="$PROD_T" PREPROD_TARGET="$PRE_T" \
	PREPROD_EXPECTED_REPO="$PRE_ID" GITHUB_REPOSITORY="$PRE_ID"
[ "$rc" -ne 0 ] && grep -q 'but the injected preprod destination is' "$errf"
check "preprod publishing to the PRODUCTION coordinate refuses" "declared vs injected" $?

run RELEASE_PROFILE=preprod PUBLISH_TARGET="$PRE_T" PRODUCTION_TARGET="$PROD_T" PREPROD_TARGET="$PRE_T" \
	PREPROD_EXPECTED_REPO="$PRE_ID" GITHUB_REPOSITORY="someone-else/other"
[ "$rc" -ne 0 ] && grep -q 'may only publish to itself' "$errf"
check "a preprod run in a repository that is not the expectation refuses" "self only" $?

run RELEASE_PROFILE=preprod PUBLISH_TARGET="ghcr.io/olivaresai/charts-preprod" PRODUCTION_TARGET="$PROD_T" \
	PREPROD_TARGET="ghcr.io/olivaresai/charts-preprod" PREPROD_EXPECTED_REPO="$PRE_ID" GITHUB_REPOSITORY="$PRE_ID"
[ "$rc" -ne 0 ] && grep -q 'names a production surface' "$errf"
check "an olivaresai-named preprod destination fails the tripwire" "tripwire" $?

run RELEASE_PROFILE=preprod PUBLISH_TARGET="docker.io/$PRE_ID/charts" PRODUCTION_TARGET="$PROD_T" \
	PREPROD_TARGET="docker.io/$PRE_ID/charts" PREPROD_EXPECTED_REPO="$PRE_ID" GITHUB_REPOSITORY="$PRE_ID"
[ "$rc" -ne 0 ] && grep -q 'names a production surface' "$errf"
check "a docker.io preprod destination fails the same tripwire" "tripwire" $?

run RELEASE_PROFILE=preprod PUBLISH_TARGET="$PRE_T" PRODUCTION_TARGET="$PROD_T" PREPROD_TARGET="$PRE_T" \
	PREPROD_EXPECTED_REPO="$PRE_ID" GITHUB_REPOSITORY="$PRE_ID" DOCKERHUB_USERNAME=someone DOCKERHUB_TOKEN=secret
[ "$rc" -ne 0 ] && grep -q 'holds Docker Hub credentials' "$errf"
check "a preprod repository holding Docker Hub credentials refuses" "no secrets" $?

run RELEASE_PROFILE=preprod PUBLISH_TARGET="$PRE_T" PRODUCTION_TARGET="$PROD_T" PREPROD_TARGET="$PRE_T" \
	PREPROD_EXPECTED_REPO="$PRE_ID" GITHUB_REPOSITORY="$PRE_ID" HOMEBREW_TAP_GITHUB_TOKEN=secret
[ "$rc" -ne 0 ] && grep -q 'Homebrew tap token' "$errf"
check "a preprod repository holding a Homebrew tap token refuses" "no secrets" $?

run RELEASE_PROFILE=rehearsal PUBLISH_TARGET="$PRE_T" PRODUCTION_TARGET="$PROD_T" GITHUB_REPOSITORY="$PRE_ID"
[ "$rc" -ne 0 ] && grep -q 'is not a reviewed profile' "$errf"
check "an unreviewed profile name refuses (deny-closed)" "two profiles only" $?

run RELEASE_PROFILE=preprod PRODUCTION_TARGET="$PROD_T" PREPROD_TARGET="$PRE_T" \
	PREPROD_EXPECTED_REPO="$PRE_ID" GITHUB_REPOSITORY="$PRE_ID"
[ "$rc" -ne 0 ] && grep -q 'PUBLISH_TARGET is required' "$errf"
check "a missing target refuses instead of defaulting" "no fallback" $?

echo ""
echo "check-publish-target battery: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
