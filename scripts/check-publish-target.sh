#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-publish-target.sh — the destination fence for the SIDE channels (Helm chart, Terraform
# provider), which publish outside the main release workflow and therefore outside
# scripts/release-preflight.sh.
#
# WHY A SECOND, SMALLER SCRIPT INSTEAD OF A THIRD PROFILE IN THE FIRST ONE. release-preflight.sh
# validates the RELEASE act: a `v*` tag, cosign mode, the OTA switches, the two anchors, the SLSA
# acknowledgement. A chart runs on `chart-v*` and has none of that. Feeding it a profile it cannot
# satisfy would mean loosening its checks until they stopped meaning anything — the opposite of
# reusing it. What IS reused is the SHAPE, and the four invariants that carry the weight:
#
#   1. the profile is one of the reviewed names; anything else refuses (deny-closed);
#   2. production publishes to EXACTLY the reviewed production coordinate;
#   3. a non-production profile declares its destination through INJECTED expectations, and that
#      expectation must name the repository the run is executing in — a run can only publish to
#      itself (the §C.4.2 property, which is what makes an admin-mutable repository variable
#      harmless: the worst a tampered value achieves is making its own run refuse);
#   4. a non-production destination may NEVER name a production surface (§C.4.6), and the
#      repository may hold NO publication secret (§C.4.7).
#
# THE HOLE THIS CLOSES, measured 2026-08-29: `release-chart.yml` and `release-provider.yml` had a
# literal repository guard and NO preflight at all (`grep -c release-preflight` answered 0 in
# both), with their destinations hard-wired to production. Widening only the guard — which is what
# a first reading of orders 36/37 suggests — would have let a preprod repository push charts to
# ghcr.io/olivaresai/charts with a single variable in between. A gate that opens a door must be
# paired with the one that decides who walks through it.
#
# Inputs (env):
#   RELEASE_PROFILE     production | preprod (required)
#   PUBLISH_TARGET      the coordinate about to be written to (required)
#   PRODUCTION_TARGET   the reviewed production coordinate (required)
#   PREPROD_TARGET      the injected preprod coordinate (required when profile is preprod)
#   GITHUB_REPOSITORY   provided by the runner (required)
#   PREPROD_EXPECTED_REPO  the injected repository the preprod run must BE (required in preprod)
#   DOCKERHUB_USERNAME / DOCKERHUB_TOKEN / HOMEBREW_TAP_GITHUB_TOKEN
#                       presence-checked; must ALL be absent outside production
#
# Exit: 0 the destination is the reviewed one · 1 it is not
set -eu

fail() {
	printf '::error::check-publish-target: %s\n' "$*" >&2
	exit 1
}

req() {
	eval "v=\${$1:-}"
	[ -n "$v" ] || fail "$1 is required and unset — a missing value never falls back to production"
}

req RELEASE_PROFILE
req PUBLISH_TARGET
req PRODUCTION_TARGET
req GITHUB_REPOSITORY

case "$RELEASE_PROFILE" in
production)
	# 2 · production publishes to exactly the reviewed coordinate, and nothing else.
	[ "$PUBLISH_TARGET" = "$PRODUCTION_TARGET" ] ||
		fail "production profile publishes to '$PUBLISH_TARGET' but the reviewed production destination is '$PRODUCTION_TARGET'"
	;;
preprod)
	# 3 · DENY-CLOSED. This script ships in the exported tree, so it embeds no preprod name:
	# without the injected expectation there is nothing to fall back to, by design.
	req PREPROD_TARGET
	req PREPROD_EXPECTED_REPO
	[ "$PUBLISH_TARGET" = "$PREPROD_TARGET" ] ||
		fail "preprod profile publishes to '$PUBLISH_TARGET' but the injected preprod destination is '$PREPROD_TARGET'"
	# A run can only publish to itself.
	[ "$GITHUB_REPOSITORY" = "$PREPROD_EXPECTED_REPO" ] ||
		fail "this run executes in '$GITHUB_REPOSITORY' but the injected preprod expectation names '$PREPROD_EXPECTED_REPO' — a run may only publish to itself"
	;;
*)
	fail "RELEASE_PROFILE '$RELEASE_PROFILE' is not a reviewed profile — only 'production' and 'preprod' are accepted"
	;;
esac

if [ "$RELEASE_PROFILE" != "production" ]; then
	# 4a · no non-production destination may name a production surface.
	for v in "$PUBLISH_TARGET" "$PREPROD_EXPECTED_REPO"; do
		case "$v" in
		*olivaresai* | *docker.io* | *homebrew-tap*)
			fail "resolved $RELEASE_PROFILE destination '$v' names a production surface (olivaresai / docker.io / homebrew-tap) — refusing"
			;;
		esac
	done
	# 4b · and the repository may hold no publication secret. A side channel that publishes a
	# real-shaped artefact with a production credential in reach is one mistake from being real.
	[ -z "${DOCKERHUB_USERNAME:-}" ] && [ -z "${DOCKERHUB_TOKEN:-}" ] ||
		fail "the $RELEASE_PROFILE repository holds Docker Hub credentials — it must hold NO publication secret"
	[ -z "${HOMEBREW_TAP_GITHUB_TOKEN:-}" ] ||
		fail "the $RELEASE_PROFILE repository holds a Homebrew tap token — it must hold NO publication secret"
fi

printf 'check-publish-target: OK — %s profile publishes to %s from %s\n' \
	"$RELEASE_PROFILE" "$PUBLISH_TARGET" "$GITHUB_REPOSITORY"
