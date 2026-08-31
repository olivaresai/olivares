#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# release-preflight.sh — the mandatory fail-closed release-target preflight.
# The §C.x tags below number the reviewed release-safety invariants it enforces.
#
# It runs FIRST, before any registry login and before any job receives
# `id-token: write`, `contents: write` or `packages: write`, and it decides one
# question: is this run allowed to mutate the target it claims? The caller
# DECLARES the full profile in env; this script validates every declared value
# against the two reviewed tuples embedded below and re-emits the validated
# values as job outputs. Mutating jobs consume THOSE OUTPUTS, never the raw
# dispatch inputs (§C.4.10) — if this script did not run, the outputs are empty
# and every dependent job refuses to start.
#
# There is deliberately no way to pass an arbitrary destination: owner, name,
# registry and namespace are reviewed profile constants (§C.1), not inputs. A
# missing RELEASE_MODE or target variable is an ERROR, never a silent fallback
# to production (§C.2).
#
# THIS SCRIPT IS PUBLIC AND EMBEDS ONLY PRODUCTION DESTINATIONS. It ships with the
# exported tree (the production release.yml invokes it), and the export gate forbids any
# internal identity in shipped files — so the rehearsal tuple cannot live here. The
# resolution is DENY-CLOSED, which is stronger than a name list: in rehearsal mode the
# script refuses outright unless the INTERNAL rehearsal workflow (not exported; the
# hard-guard literals live fixed and reviewed in that versioned file, never in mutable
# repository variables) injects its expected tuple via OLIVARES_REHEARSAL_EXPECTED_*.
# Every property check then still applies to the injected tuple: the run repository must
# BE the declared destination (§C.4.2 — a run can only mutate itself), no rehearsal
# destination may name a production surface (§C.4.6), every publication switch must be
# false, signing must be key/no-tlog, and the tag must match the rehearsal grammar. An
# unclassified or expectation-less rehearsal cannot run at all.
#
# The ten §C.4 requirements, in order, are marked "§C.4.N" at their checks.
#
# Inputs (env):
#   RELEASE_MODE                production | preprod | rehearsal (required)
#   RELEASE_GITHUB_REPO         declared release destination owner/name
#   OCI_IMAGE_REPO              declared OCI destination
#   SOURCE_REPOSITORY_URL       declared OCI source label
#   RELEASE_TAG                 the tag under release (push ref or dispatch input)
#   COSIGN_MODE                 keyless | key
#   COSIGN_TLOG_UPLOAD          true | false
#   PUBLISH_LATEST PUBLISH_DOCKERHUB PUBLISH_HOMEBREW PUBLISH_OTA_STABLE
#                               true | false | auto
#   RUN_SLSA                    true | false
#   ACKNOWLEDGE_PUBLIC_SLSA_LOG preprod and rehearsal: must be exactly "true" — the
#                               SLSA generators write PUBLIC records naming the
#                               running repository, so a non-production act must
#                               say out loud that it accepts them
#   OLIVARES_LICENSE_PUBKEY / OLIVARES_OTA_PUBKEY
#                               production: required (identity-checked); preprod:
#                               required (sandbox pair, form only — design/ is not
#                               exported); rehearsal: optional (per-run pairs)
#   DOCKERHUB_USERNAME / DOCKERHUB_TOKEN / HOMEBREW_TAP_GITHUB_TOKEN
#                               presence-checked; must ALL be absent in rehearsal
#   GITHUB_REPOSITORY GITHUB_REF GITHUB_REF_NAME GITHUB_REF_TYPE GITHUB_SHA
#                               provided by the runner
#   GITHUB_OUTPUT               required — outputs ARE the contract (§C.4.10)
#   GITHUB_STEP_SUMMARY         optional — §C.4.9 summary when present
set -euo pipefail

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

fail() {
	printf '::error::release-preflight: %s\n' "$*" >&2
	exit 1
}

req() {
	# req VAR — the variable must be set and non-empty. Nothing here defaults.
	eval "v=\${$1:-}"
	[ -n "$v" ] || fail "$1 is required and unset/empty — the caller must declare the full profile; a missing value never falls back to production (§C.2)"
}

# --- the two reviewed tuples (§C.4.1). These constants ARE the profiles. -----------------
PROD_REPO="olivaresai/olivares"
PROD_OCI="ghcr.io/olivaresai/olivares"
PROD_SOURCE="https://github.com/olivaresai/olivares"
# PRERELEASE POLICY, PINNED DENY-CLOSED (R1 contrast P2-03). This grammar rejects
# vX.Y.Z-rc.N/-beta.N on purpose: the production cosign certificate identity — the
# default regexp in this workflow's OTA verification, in scripts/verify-release.sh and
# in the reviewed release profile (§C.2) — only matches clean vMAJOR.MINOR.PATCH tag refs, so a prerelease tag
# would build and publish artifacts whose checksums signature CANNOT verify downstream.
# Failing here, before any permission or publication, is strictly better than failing at
# the ceremony. Some docs (INSTALL.md's v26.7.0-beta.1 example, CHANGELOG-CADENCE's
# prerelease flow, core/release/version.go's parser) promise a prerelease flow this gate
# does not admit: enabling it requires WIDENING the release signing identity — a
# security decision that belongs to the maintainer, not to this script. Until that decision, the
# divergence is a declared residual and this line is the contract. Pinned by the
# "a prerelease tag is rejected in production" case of scripts/test-release-preflight.sh.
PROD_TAG_RE='^v[0-9]+\.[0-9]+\.[0-9]+$'

# NO rehearsal tuple here — see the header. The internal rehearsal caller injects it.
REH_TAG_RE='^v0\.0\.0-rehearsal\.[0-9]+$'

req RELEASE_MODE
req RELEASE_GITHUB_REPO
req OCI_IMAGE_REPO
req SOURCE_REPOSITORY_URL
req RELEASE_TAG
req COSIGN_MODE
req COSIGN_TLOG_UPLOAD
req PUBLISH_LATEST
req PUBLISH_DOCKERHUB
req PUBLISH_HOMEBREW
req PUBLISH_OTA_STABLE
req RUN_SLSA
req GITHUB_REPOSITORY
req GITHUB_REF
req GITHUB_REF_NAME
req GITHUB_REF_TYPE
req GITHUB_SHA
[ -n "${GITHUB_OUTPUT:-}" ] || fail "GITHUB_OUTPUT is unset — validated job outputs are the contract every mutating job consumes (§C.4.10); refusing to run without them"

case "$RELEASE_MODE" in
production)
	want_repo="$PROD_REPO" want_oci="$PROD_OCI" want_source="$PROD_SOURCE" tag_re="$PROD_TAG_RE"
	want_cosign_mode="keyless" want_tlog="true"
	want_latest="true" want_dockerhub="auto" want_homebrew="auto" want_ota="true"
	;;
rehearsal)
	# DENY-CLOSED: the rehearsal destination is internal infrastructure. Without the
	# internal caller's injected expectations this public script refuses — there is no
	# rehearsal name to fall back to, by design (export gate; see the header).
	want_repo="${OLIVARES_REHEARSAL_EXPECTED_REPO:-}"
	want_oci="${OLIVARES_REHEARSAL_EXPECTED_OCI:-}"
	want_source="${OLIVARES_REHEARSAL_EXPECTED_SOURCE:-}"
	if [ -z "$want_repo" ] || [ -z "$want_oci" ] || [ -z "$want_source" ]; then
		fail "rehearsal mode requires the internal rehearsal workflow to inject its expected destination tuple (OLIVARES_REHEARSAL_EXPECTED_REPO/_OCI/_SOURCE). This public script embeds no rehearsal destination and refuses an unclassified rehearsal (deny-closed)"
	fi
	tag_re="$REH_TAG_RE"
	want_cosign_mode="key" want_tlog="false"
	want_latest="false" want_dockerhub="false" want_homebrew="false" want_ota="false"
	;;
preprod)
	# DENY-CLOSED, exactly like rehearsal and for the same reason: this script SHIPS in the
	# exported tree and the export gate forbids naming an internal repository here (it blocks
	# the owner literal outright), so there is no preprod name to fall back to.
	#
	# WHERE THE TUPLE COMES FROM, AND WHY IT DIFFERS FROM REHEARSAL. A rehearsal is dispatched
	# by an INTERNAL, unexported workflow, which can carry the literals fixed and reviewed. A
	# preprod act runs the EXPORTED release.yml INSIDE the preprod repository itself, so there
	# is no unexported file to inject from: the only injection point is that repository's own
	# variables. A repository variable is admin-mutable without review, and here that is SAFE —
	# not because the variable is trusted, but because two checks below make a tampered value
	# useless: §C.4.2 forces the declared destination to BE the running repository (a run can
	# only mutate itself), and §C.4.6 refuses any destination naming a production surface. The
	# worst a tampered variable achieves is making its own run refuse.
	want_repo="${OLIVARES_PREPROD_EXPECTED_REPO:-}"
	want_oci="${OLIVARES_PREPROD_EXPECTED_OCI:-}"
	want_source="${OLIVARES_PREPROD_EXPECTED_SOURCE:-}"
	if [ -z "$want_repo" ] || [ -z "$want_oci" ] || [ -z "$want_source" ]; then
		fail "preprod mode requires the preprod repository to inject its expected destination tuple (OLIVARES_PREPROD_EXPECTED_REPO/_OCI/_SOURCE) — this public script embeds no preprod name, by design (§C.4.1)"
	fi
	# The REAL tag grammar, not the rehearsal one: a preprod act exists to rehearse v26.8.0
	# itself (order 36 — nothing is first tried in public), so the tag must be the real shape.
	tag_re="$PROD_TAG_RE"
	# ⛔ FIRMA IGUAL QUE PRODUCCIÓN: keyless, con log de transparencia. Y esto ES una
	# decisión sobre una fuga pública, así que va razonada y no por omisión.
	#
	# Mi primera versión ponía `key`+`no-tlog` para no escribir el nombre del repositorio de
	# preprod en el log público de Rekor. Era INCOHERENTE, medido en este mismo árbol:
	#   · `RUN_SLSA` es obligatorio — este script rehúsa `false` unas líneas más abajo, y el
	#     workflow lo fija incondicionalmente;
	#   · los jobs de provenance (`release.yml`, `provenance-binaries` / `provenance-image`) no
	#     llevan valla de repositorio y usan `slsa-github-generator`, que firma keyless por
	#     Fulcio y publica en Rekor.
	# O sea que el nombre acaba en un log público de todas formas, por una vía que este mismo
	# script hace obligatoria. El modo clave pagaba el precio entero y no compraba el beneficio.
	#
	# Y el precio era el acto completo, no una molestia: sin material de clave en `release.yml`
	# (`cosign-verified.sh` rehúsa firmar), `promote-latest` verificando keyless lo que se firmó
	# con clave, y `goreleaser` sin emitir el `.pem` que la fase 2 exige por fichero. Keyless
	# arregla los tres a la vez y ensaya la ruta REAL, que es lo que la orden 36 pide.
	#
	# LO QUE ESTO SIGNIFICA, DICHO PARA QUE NADIE SE SORPRENDA: un acto en preprod deja el
	# nombre del repositorio de preprod en registros públicos y permanentes. No es evitable
	# mientras SLSA sea obligatorio; es una consecuencia de ensayar el acto de verdad.
	want_cosign_mode="keyless" want_tlog="true"
	# The act is otherwise COMPLETE: the latest alias and the OTA channel are exactly what
	# order 36 wants rehearsed. Docker Hub and the Homebrew tap stay off: they are official
	# production surfaces with no preprod counterpart, and §C.4.6 refuses to name them anyway.
	want_latest="true" want_dockerhub="false" want_homebrew="false" want_ota="true"
	;;
*)
	fail "RELEASE_MODE '$RELEASE_MODE' is not a reviewed profile — only 'production', 'preprod' and 'rehearsal' tuples are accepted (§C.4.1)"
	;;
esac

# --- §C.4.1: only the exact reviewed tuples ---------------------------------------------
[ "$RELEASE_GITHUB_REPO" = "$want_repo" ] || fail "declared RELEASE_GITHUB_REPO '$RELEASE_GITHUB_REPO' is not the reviewed $RELEASE_MODE destination '$want_repo' — arbitrary targets are rejected (§C.4.1)"
[ "$OCI_IMAGE_REPO" = "$want_oci" ] || fail "declared OCI_IMAGE_REPO '$OCI_IMAGE_REPO' is not the reviewed $RELEASE_MODE registry '$want_oci' — arbitrary targets are rejected (§C.4.1)"
[ "$SOURCE_REPOSITORY_URL" = "$want_source" ] || fail "declared SOURCE_REPOSITORY_URL '$SOURCE_REPOSITORY_URL' is not the reviewed $RELEASE_MODE source '$want_source' (§C.4.1)"

# --- §C.4.2: the run repository must BE the declared destination ------------------------
[ "$GITHUB_REPOSITORY" = "$RELEASE_GITHUB_REPO" ] || fail "this run executes in '$GITHUB_REPOSITORY' but the $RELEASE_MODE profile targets '$RELEASE_GITHUB_REPO' — a run may only mutate the repository it runs in (§C.4.2)"

# --- §C.4.3: the ref and the declared tag must be the same tag ref ----------------------
[ "$GITHUB_REF_TYPE" = "tag" ] || fail "GITHUB_REF_TYPE is '$GITHUB_REF_TYPE', not 'tag' — a release/rehearsal only runs on a tag ref (§C.4.3)"
printf '%s' "$RELEASE_TAG" | grep -Eq "$tag_re" || fail "tag '$RELEASE_TAG' does not match the $RELEASE_MODE tag contract $tag_re (§C.4.3)"
[ "$GITHUB_REF" = "refs/tags/$RELEASE_TAG" ] || fail "dispatch/push ref '$GITHUB_REF' is not 'refs/tags/$RELEASE_TAG' — the ref and the declared tag must be identical tag refs (§C.4.3)"
[ "$GITHUB_REF_NAME" = "$RELEASE_TAG" ] || fail "GITHUB_REF_NAME '$GITHUB_REF_NAME' differs from the declared tag '$RELEASE_TAG' (§C.4.3)"

# --- §C.4.4: publication switches are a closed enum; rehearsal is all-false -------------
for switch in PUBLISH_LATEST PUBLISH_DOCKERHUB PUBLISH_HOMEBREW PUBLISH_OTA_STABLE; do
	eval "v=\$$switch"
	case "$v" in
	true | false | auto) ;;
	*) fail "$switch is '$v'; only exactly 'true', 'false' or 'auto' are accepted (§C.4.4)" ;;
	esac
done
[ "$PUBLISH_LATEST" = "$want_latest" ] || fail "PUBLISH_LATEST must be '$want_latest' in $RELEASE_MODE, got '$PUBLISH_LATEST' (§C.4.4)"
[ "$PUBLISH_DOCKERHUB" = "$want_dockerhub" ] || fail "PUBLISH_DOCKERHUB must be '$want_dockerhub' in $RELEASE_MODE, got '$PUBLISH_DOCKERHUB' (§C.4.4)"
[ "$PUBLISH_HOMEBREW" = "$want_homebrew" ] || fail "PUBLISH_HOMEBREW must be '$want_homebrew' in $RELEASE_MODE, got '$PUBLISH_HOMEBREW' (§C.4.4)"
[ "$PUBLISH_OTA_STABLE" = "$want_ota" ] || fail "PUBLISH_OTA_STABLE must be '$want_ota' in $RELEASE_MODE, got '$PUBLISH_OTA_STABLE' (§C.4.4)"

# --- §C.4.5: rehearsal signs with a per-run key and NEVER uploads to the tlog -----------
[ "$COSIGN_MODE" = "$want_cosign_mode" ] || fail "COSIGN_MODE must be '$want_cosign_mode' in $RELEASE_MODE, got '$COSIGN_MODE' (§C.4.5)"
[ "$COSIGN_TLOG_UPLOAD" = "$want_tlog" ] || fail "COSIGN_TLOG_UPLOAD must be '$want_tlog' in $RELEASE_MODE, got '$COSIGN_TLOG_UPLOAD' (§C.4.5)"

# --- SLSA: the official generators create NON-DELETABLE public transparency-log ---------
# records under the run identity. RUN_SLSA is a profile constant (true in both
# profiles); in rehearsal it requires the explicit per-run acknowledgement.
# There is deliberately NO RUN_SLSA=false input: that partial-rehearsal variant
# is escalated to the maintainer and NOT approved.
case "$RUN_SLSA" in
true | false) ;;
*) fail "RUN_SLSA is '$RUN_SLSA'; only 'true' or 'false' are accepted" ;;
esac
[ "$RUN_SLSA" = "true" ] || fail "RUN_SLSA must be 'true' — the RUN_SLSA=false partial rehearsal is elevated and unapproved; do not encode it as reachable"
if [ "$RELEASE_MODE" = "rehearsal" ] || [ "$RELEASE_MODE" = "preprod" ]; then
	[ "${ACKNOWLEDGE_PUBLIC_SLSA_LOG:-}" = "true" ] || fail "acknowledge_public_slsa_log must be exactly 'true': the official SLSA generator jobs create PERMANENT, NON-DELETABLE public Rekor transparency-log records under the disposable rehearsal identity. Without that explicit acknowledgement no rehearsal runs (§C.1)"
fi

# --- §C.4.6: no resolved non-production destination may name a production surface -------
if [ "$RELEASE_MODE" = "rehearsal" ] || [ "$RELEASE_MODE" = "preprod" ]; then
	for v in "$RELEASE_GITHUB_REPO" "$OCI_IMAGE_REPO" "$SOURCE_REPOSITORY_URL"; do
		case "$v" in
		*olivaresai* | *docker.io* | *homebrew-tap*)
			fail "resolved $RELEASE_MODE destination '$v' names a production surface (olivaresai / docker.io / homebrew-tap) — refusing (§C.4.6)"
			;;
		esac
	done
fi

# --- §C.4.7: Docker Hub both-or-neither; rehearsal holds NO publication secrets ---------
if [ -n "${DOCKERHUB_USERNAME:-}" ] && [ -z "${DOCKERHUB_TOKEN:-}" ]; then
	fail "DOCKERHUB_USERNAME is set without DOCKERHUB_TOKEN — credentials are both-or-neither (§C.4.7)"
fi
if [ -z "${DOCKERHUB_USERNAME:-}" ] && [ -n "${DOCKERHUB_TOKEN:-}" ]; then
	fail "DOCKERHUB_TOKEN is set without DOCKERHUB_USERNAME — credentials are both-or-neither (§C.4.7)"
fi
# A preprod repository is held to the SAME rule as a rehearsal one, and for a sharper
# reason: preprod publishes a REAL-shaped tag and a REAL update channel, so a publication
# secret sitting in it is the one ingredient needed to turn a dress rehearsal into an
# accidental production publication.
if [ "$RELEASE_MODE" = "rehearsal" ] || [ "$RELEASE_MODE" = "preprod" ]; then
	[ -z "${DOCKERHUB_USERNAME:-}" ] && [ -z "${DOCKERHUB_TOKEN:-}" ] || fail "the $RELEASE_MODE repository holds Docker Hub credentials — it must hold NO publication secret at all (§C.4.7)"
	[ -z "${HOMEBREW_TAP_GITHUB_TOKEN:-}" ] || fail "the $RELEASE_MODE repository holds a Homebrew tap token — it must hold NO publication secret at all (§C.4.7)"
fi

# --- §C.4.8: the two public release anchors --------------------------------------------
license_fp="per-run"
ota_fp="per-run"
if [ "$RELEASE_MODE" = "production" ]; then
	sh "$ROOT/scripts/check-release-pubkey.sh" || fail "release public-anchor validation failed (§C.4.8)"
	# ⛔ FORM IS NOT IDENTITY, and until 2026-08-28 this block only checked form and then PRINTED a
	# fingerprint. Printing a fingerprint is not comparing it. Measured that day: the production
	# anchors of olivaresai/olivares were the PRE-rotation pair for five days after commit
	# 9354dd555 rotated them "after a traced command exposed the private halves" — and this
	# preflight said `OK — production profile validated`, rc=0, with the exposed anchor injected.
	# The mutant survived, so the check was decoration. It now compares against the reviewed
	# anchor (the published table, and the ceremony record when the tree carries it).
	sh "$ROOT/scripts/check-release-anchor-identity.sh" ||
		fail "release public-anchor IDENTITY check failed (§C.4.8): the anchors in effect are not the reviewed ones — a well-formed anchor is not a correct anchor"
	license_fp="$(printf '%s' "${OLIVARES_LICENSE_PUBKEY}" | base64 -d | sha256sum | cut -c1-8)"
	ota_fp="$(printf '%s' "${OLIVARES_OTA_PUBKEY}" | base64 -d | sha256sum | cut -c1-8)"
elif [ "$RELEASE_MODE" = "preprod" ]; then
	# A preprod act carries the SANDBOX pair, declared as repository variables of the preprod
	# repository — not a per-run throwaway (it must survive between acts so a client can be
	# upgraded twice) and not the production pair (that one never leaves its custody).
	#
	# FORM IS CHECKED, IDENTITY CANNOT BE, and the reason is structural rather than a decision.
	#
	# ⛔ THE REASON WRITTEN HERE UNTIL 2026-08-30 WAS STALE, and it is corrected in place rather
	# than deleted, because a dead reason beside a live decision is what makes someone "fix" the
	# decision wrongly six months later. It used to say check-release-anchor-identity.sh compares
	# only against `an internal design note (not shipped)*.pub`, which `design/` does not export, "so the reviewed
	# table simply is not on disk there". Both halves are false today: the reviewed table IS
	# `docs/RELEASE-VERIFICATION.md`, it DOES travel (15 793 B in the exported tree), and that
	# script has read TWO homes since 2026-08-29 — measured by running it with the ceremony
	# directory pointed at a non-existent path: identical verdict.
	#
	# THE TRUE REASON is that in preprod the comparison has no valid subject. The reviewed table
	# carries exactly two anchor rows, both v26.8.0 PRODUCTION (license 5144ae08, OTA 1eee9d76)
	# and ZERO preprod rows, and its own prose says the sandbox licence key "is a third,
	# independent pair ... and never appears in release artifacts". A preprod act ships that
	# third pair by design, so contrasting it against the production row compares two
	# ENVIRONMENTS: the mismatch is an artefact of subject, not an impersonation. The enterprise
	# mirror learned this the hard way — the control was wired into its release step 10 on
	# 2026-08-30 and blocked the release with rc=2 until ent#156 removed it.
	#
	# WHEN THIS COMES BACK: the day the table gains a preprod row, the contrast acquires a
	# subject and this branch should call the control with OLIVARES_RELEASE_PROFILE=preprod,
	# which it has understood since 2026-08-30. Declaring the anchors is
	# therefore REQUIRED — a preprod act with no anchor would sign and publish an update
	# channel nothing pins — but what this check can assert is well-formedness, and saying
	# otherwise would be the same "printing a fingerprint is not comparing it" defect the
	# production branch above was built to kill.
	[ -n "${OLIVARES_LICENSE_PUBKEY:-}" ] && [ -n "${OLIVARES_OTA_PUBKEY:-}" ] ||
		fail "preprod requires BOTH anchor variables (OLIVARES_LICENSE_PUBKEY, OLIVARES_OTA_PUBKEY) — the sandbox pair; an act that publishes an update channel with no anchor pins nothing (§C.4.8)"
	sh "$ROOT/scripts/check-release-pubkey.sh" || fail "the preprod anchor variables are malformed (§C.4.8)"
	license_fp="sandbox/$(printf '%s' "${OLIVARES_LICENSE_PUBKEY}" | base64 -d | sha256sum | cut -c1-8)"
	ota_fp="sandbox/$(printf '%s' "${OLIVARES_OTA_PUBKEY}" | base64 -d | sha256sum | cut -c1-8)"
else
	# Rehearsal anchors are per-run throwaway pairs generated INSIDE the build job
	# (design §C.3), so they cannot exist yet at preflight time. If the disposable
	# repository nevertheless carries anchor variables they must at least be
	# well-formed — and the build job re-runs this same validator on the pair it
	# generates, immediately after generating it.
	if [ -n "${OLIVARES_LICENSE_PUBKEY:-}" ] || [ -n "${OLIVARES_OTA_PUBKEY:-}" ]; then
		sh "$ROOT/scripts/check-release-pubkey.sh" || fail "the rehearsal repository declares release anchor variables and they are malformed (§C.4.8). Remove them (per-run pairs are generated in the build job) or fix them"
		license_fp="$(printf '%s' "${OLIVARES_LICENSE_PUBKEY}" | base64 -d | sha256sum | cut -c1-8)"
		ota_fp="$(printf '%s' "${OLIVARES_OTA_PUBKEY}" | base64 -d | sha256sum | cut -c1-8)"
		license_fp="declared/${license_fp} (superseded by per-run pair)"
		ota_fp="declared/${ota_fp} (superseded by per-run pair)"
	fi
fi

release_version="${RELEASE_TAG#v}"

# --- §C.4.9: the non-secret resolved profile, visible BEFORE any approval ---------------
summary() {
	cat <<-EOF
		## release-preflight — resolved $RELEASE_MODE profile (validated)

		| field | value |
		|---|---|
		| mode | \`$RELEASE_MODE\` |
		| run repository | \`$GITHUB_REPOSITORY\` |
		| release destination | \`$RELEASE_GITHUB_REPO\` |
		| OCI destination | \`$OCI_IMAGE_REPO\` |
		| source label | \`$SOURCE_REPOSITORY_URL\` |
		| tag | \`$RELEASE_TAG\` (version \`$release_version\`) |
		| source SHA | \`$GITHUB_SHA\` |
		| cosign | mode \`$COSIGN_MODE\`, tlog upload \`$COSIGN_TLOG_UPLOAD\` |
		| license anchor | \`$license_fp\` |
		| OTA anchor | \`$ota_fp\` |
		| publish latest / dockerhub / homebrew / ota-stable | \`$PUBLISH_LATEST\` / \`$PUBLISH_DOCKERHUB\` / \`$PUBLISH_HOMEBREW\` / \`$PUBLISH_OTA_STABLE\` |
		| SLSA generators | \`$RUN_SLSA\`$([ "$RELEASE_MODE" != "production" ] && printf ' %s' '(public-log acknowledgement given)') |
	EOF
}
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
	summary >>"$GITHUB_STEP_SUMMARY"
fi
summary

# --- §C.4.10: validated values become the ONLY source downstream jobs may use -----------
{
	echo "release_mode=$RELEASE_MODE"
	echo "release_github_repo=$RELEASE_GITHUB_REPO"
	echo "release_github_owner=${RELEASE_GITHUB_REPO%%/*}"
	echo "release_github_name=${RELEASE_GITHUB_REPO#*/}"
	echo "oci_image_repo=$OCI_IMAGE_REPO"
	echo "source_repository_url=$SOURCE_REPOSITORY_URL"
	echo "release_tag=$RELEASE_TAG"
	echo "release_version=$release_version"
	echo "cosign_mode=$COSIGN_MODE"
	echo "cosign_tlog_upload=$COSIGN_TLOG_UPLOAD"
	echo "publish_latest=$PUBLISH_LATEST"
	echo "publish_dockerhub=$PUBLISH_DOCKERHUB"
	echo "publish_homebrew=$PUBLISH_HOMEBREW"
	echo "publish_ota_stable=$PUBLISH_OTA_STABLE"
	echo "run_slsa=$RUN_SLSA"
} >>"$GITHUB_OUTPUT"

echo "release-preflight: OK — $RELEASE_MODE profile validated for $GITHUB_REPOSITORY at $RELEASE_TAG"
