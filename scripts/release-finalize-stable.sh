#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# release-finalize-stable.sh — the VERIFY-AND-PUBLISH half of the supported stable
# Community ceremony. It runs after the phase-2 signing step has attached the verified
# OTA pair, inside the same guarded execution window, and it is the only sanctioned way a
# stable Community release stops being a draft.
#
#   usage: bash scripts/release-finalize-stable.sh <release-tag> <release-commit>
#
#   exit 0  the release was published by its retained ID, read back, and every
#           postcondition held — or, in reconciliation mode, an already-published
#           candidate was inspected and verified without repeating any effect
#   exit 1  REFUSED. No PATCH was issued; nothing was published
#   exit 2  NO HE PODIDO MIRAR. A required tool or input was missing or unreadable.
#           No PATCH was issued
#   exit 3  PUBLICATION_UNKNOWN. A PATCH was issued and its outcome could not be
#           established. NOTHING is compensated: no undraft, delete, overwrite or
#           second publish
#   exit 4  PUBLISHED, POSTCONDITION FAILED. The release IS published and a delivery
#           or pointer check afterwards did not hold. This is a failed postcondition,
#           never a claim that publication did not occur
#
# ⛔ WHAT THIS FIXES, MEASURED. QA2334 observed the live v26.8.0 stable channel serving a
# manifest (200) whose REQUIRED detached OTA signature answered 404, while the cosign
# pipeline signature answered 200 — and the shipped product's own `upgrade --check` and its
# live preparation gate both fail on it. The cause of that particular release is unknown
# (unrun, unapproved, or failed ceremony) and this script does not repair it. What it
# changes is that the SUPPORTED route can no longer produce that state: publication stops
# being a human clicking a button in a web UI, which no workflow can gate, and becomes one
# operation that verifies the complete candidate immediately before it flips the draft.
#
# ⛔ WHAT IT DOES NOT CLAIM, AND THESE ARE NOT HEDGES.
#
#   · It is NOT a transaction over the release. GitHub documents that a writer can edit a
#     release through the UI or the API, and the update endpoint offers no compare-and-swap
#     over a verified asset set. The inventory is re-read immediately before the PATCH and
#     any observed change refuses; that DETECTS an out-of-band writer, it does not exclude
#     one. A repository-wide hostile-writer guarantee belongs to credential custody and is
#     not delivered here.
#   · Same-tag Actions concurrency serializes the workflow runs that participate in it, not
#     arbitrary writers.
#   · It closes neither Community signer custody nor the OGL2 credential-domain work, does
#     not migrate OTA anchors or TUF, and does not qualify the security or LTS channels.
#
# ⛔ NO TRUST SEAM. There is no environment variable, flag or file that makes this script
# skip a verification, accept a previous job's success string, or write to a destination it
# did not derive itself. The battery drives it with PATH-stubbed tools and a copy whose
# TRUSTED_BIN line is redirected — a control must not be configurable by the thing it
# guards against (the rule the phase-2 workflow steps already carry).
set -euo pipefail
export LC_ALL=C

# ABSOLUTE, WITH NO OVERRIDE — the same rule as every phase-2 guard. This script runs in a
# job where third-party actions have already executed, and GITHUB_PATH lets any of them
# prepend a directory for every later step. A `gh` or `git` chosen by a shim would be
# choosing what this script gets to see.
TRUSTED_BIN="/usr/bin"

ROOT="$(unset CDPATH; cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"

# THE PRODUCTION LITERAL LIVES HERE ON PURPOSE. It is the public repository name, already
# written in this tree (scripts/verify-release.sh DEFAULT_CERT_IDENTITY, and the job guard
# in .github/workflows/release.yml), so `task lint:export` has nothing to object to. The
# preprod destination is NOT written: that name is private and this file is exported.
PRODUCTION_REPOSITORY="olivaresai/olivares"
CERT_OIDC_ISSUER_DEFAULT="https://token.actions.githubusercontent.com"
RELEASE_WORKFLOW_PATH=".github/workflows/release.yml"
BUILD_CONTEXT_SCHEMA="olivares.ai/release-build-context/v1"
BUILD_CONTEXT_MAX_BYTES=4096

# --- the release recipe, DERIVED AND DRIFT-TESTED ------------------------------------------
# These are not a copy of a past release's asset list. They are the shape .goreleaser.yaml
# and .github/workflows/release.yml declare, and scripts/test-release-finalize-stable.sh
# re-reads both files and fails if either moves without this block moving with it. That is
# the difference between an inventory that stays true and a count someone once wrote down.
#
# Why the matrix must be enforced at all: `CrossCheckChecksums` deliberately tolerates a
# checksums.txt that covers MORE files than the manifest (core/release/manifest.go), so a
# signed manifest that lists only linux/amd64 passes every existing check while three
# quarters of the platforms this release promises are simply absent.
RECIPE_GOOS=(linux darwin)
RECIPE_GOARCH=(amd64 arm64)
RECIPE_PACKAGE_FORMATS=(deb rpm apk)
RECIPE_PACKAGE_GOARCH=(amd64 arm64)

# Architecture spellings a native packager may legitimately use for the SAME goarch. The
# finalizer never invents a package FILENAME — GoReleaser's conventional spelling is a
# property of that tool, not of this tree — it requires format×architecture COVERAGE over
# the members the signed checksums already names. See the honest limit in the report.
arch_aliases() {
	case "$1" in
	amd64) printf '%s\n' amd64 x86_64 ;;
	arm64) printf '%s\n' arm64 aarch64 ;;
	*) printf '%s\n' "$1" ;;
	esac
}

# --- verdict helpers ------------------------------------------------------------------
# The three pre-publication verdicts are distinct because the operator's next action
# differs: a refusal means the candidate is wrong, a blind means the machine could not
# answer, and they must never collapse into each other.
PATCH_ISSUED=0
EVIDENCE_JSON=""

_say() { printf 'release-finalize-stable: %s\n' "$*"; }
_err() { printf 'release-finalize-stable: %s\n' "$*" >&2; }

refuse() {
	_err "REFUSED — $1"
	shift || true
	for line in "$@"; do _err "  $line"; done
	_err "No PATCH was issued. The release is untouched and remains a draft."
	exit 1
}

blind() {
	_err "NO HE PODIDO MIRAR — $1"
	shift || true
	for line in "$@"; do _err "  $line"; done
	_err "A check that could not run is not a check that passed. No PATCH was issued."
	exit 2
}

unknown() {
	_err "⛔ PUBLICATION_UNKNOWN — $1"
	shift || true
	for line in "$@"; do _err "  $line"; done
	_err "A publish PATCH was issued and its outcome is NOT established."
	_err "NOTHING has been compensated: no undraft, no delete, no overwrite, no second"
	_err "publish. Inspect the release by hand before any further action; a blind retry"
	_err "here is how one uncertain effect becomes two."
	exit 3
}

postcondition_failed() {
	_err "⛔ PUBLISHED, POSTCONDITION FAILED — $1"
	shift || true
	for line in "$@"; do _err "  $line"; done
	_err "The release IS published: this is a failed postcondition, not a claim that the"
	_err "publication did not happen. Do not re-run the ceremony to 'fix' it."
	exit 4
}

# ⛔ AFTER THE PATCH, AN UNEXPECTED FAILURE IS NOT A REFUSAL. `set -e` turns any unhandled
# non-zero into an exit, and exit 1 from this script means "nothing was published". Once
# the PATCH is in flight that sentence is false, so the trap reclassifies.
# ⛔ ONE TRAP, AND IT OWNS THE CLEANUP. Chaining `trap 'cleanup; on_exit' EXIT` looks
# equivalent and is not: by the time the second function runs, `$?` is the exit status of
# the CLEANUP, so a script that died refusing would be reclassified as a success. The
# status is captured first, in one handler.
WORK=""
on_exit() {
	local rc=$?
	[ -z "$WORK" ] || rm -rf "$WORK"
	if [ "$rc" -ne 0 ] && [ "$PATCH_ISSUED" -eq 1 ] && [ "$rc" -ne 3 ] && [ "$rc" -ne 4 ]; then
		_err "⛔ PUBLICATION_UNKNOWN — the script stopped with exit ${rc} after the publish PATCH."
		_err "Exit 1 would have meant 'nothing was published', and that is not known to be true."
		exit 3
	fi
	exit "$rc"
}
trap on_exit EXIT

# --- tools ------------------------------------------------------------------------------
# git, gh and sha256sum are resolved by ABSOLUTE PATH from the trusted location, like every
# other phase-2 control. jq, tar, curl and go are resolved through PATH — go is installed by
# setup-go into the tool cache and is never in /usr/bin — but a resolution INSIDE the
# checkout is refused: that is the one case where "on PATH" means "written by the tree this
# script is auditing".
require_trusted() {
	local name="$1" path="${TRUSTED_BIN}/$1" resolved
	[ -x "$path" ] || blind "no trusted ${name} at ${path}"
	resolved="$(command -v "$name" || true)"
	if [ "$resolved" != "$path" ]; then
		refuse "PATH resolves ${name} to ${resolved:-<none>}, not ${path}." \
			"Something earlier in this job put another one ahead of it, so every question" \
			"this script asks would be answered by a tool it did not choose."
	fi
	printf '%s' "$path"
}

require_path_tool() {
	local name="$1" resolved
	resolved="$(command -v "$name" || true)"
	[ -n "$resolved" ] || blind "required tool ${name} is not installed" \
		"Strict finalization treats a missing required tool as a failure, never as a skip."
	case "$resolved" in
	/*) ;;
	*) refuse "${name} resolves to a relative path (${resolved})" ;;
	esac
	if [ -n "${GITHUB_WORKSPACE:-}" ]; then
		case "$resolved" in
		"${GITHUB_WORKSPACE}"/*)
			refuse "${name} resolves inside the checkout (${resolved})" \
				"A tool supplied by the tree under verification cannot verify it."
			;;
		esac
	fi
	printf '%s' "$resolved"
}

GIT_BIN="$(require_trusted git)"
GH_BIN="$(require_trusted gh)"
SHA_BIN="$(require_trusted sha256sum)"
JQ_BIN="$(require_path_tool jq)"
TAR_BIN="$(require_path_tool tar)"
CURL_BIN="$(require_path_tool curl)"
GO_BIN="$(require_path_tool go)"
# base64 decodes the provenance envelope and cmp decides whether the inventory moved: both
# produce a VERDICT, so their absence is a refusal to look, not a detail.
B64_BIN="$(require_path_tool base64)"
CMP_BIN="$(require_path_tool cmp)"

# ⛔ COSIGN AND slsa-verifier ALSO DECIDE A VERDICT, SO THEIR ABSENCE IS A BLIND (root return
# 02, F1). These two were the only verdict-producing tools this script did not resolve for
# itself: it delegated both to children and reported any non-zero child as "the candidate is
# wrong". Exit 1 and exit 2 must never collapse into each other, and here they did, in the
# direction that costs the most — the operator was sent to inspect artifacts that were fine.
#
# THE COSIGN PATH IS THE ISOLATED ONE, AND IT IS NOT A FALLBACK. The phase-2 job runs
# scripts/assert-cosign-binary.sh --isolate before the ceremony: it authenticates the binary
# against the upstream published digests, MOVES it to a private directory, refuses to report
# success while the bare name still resolves, and exports OLIVARES_COSIGN_BIN. Every consumer
# then runs it through scripts/cosign-verified.sh, which re-authenticates the bytes
# immediately before each invocation. This script never executes the named binary itself,
# never accepts an unverified one, and never restores the name isolation removed.
COSIGN_WRAPPER="$ROOT/scripts/cosign-verified.sh"
[ -f "$COSIGN_WRAPPER" ] ||
	blind "the verified cosign wrapper is not in this checkout: ${COSIGN_WRAPPER}"
COSIGN_BIN="${OLIVARES_COSIGN_BIN:-}"
[ -n "$COSIGN_BIN" ] ||
	blind "OLIVARES_COSIGN_BIN is not set, so no authenticated cosign is available" \
		"scripts/assert-cosign-binary.sh must run earlier in this job: it authenticates the" \
		"binary against the upstream published digests and exports its path. Falling back to" \
		"whatever 'cosign' resolves to would make that authentication decorative."
case "$COSIGN_BIN" in
/*) ;;
*) refuse "OLIVARES_COSIGN_BIN is '${COSIGN_BIN}', which is not an absolute path" ;;
esac
[ -x "$COSIGN_BIN" ] ||
	blind "OLIVARES_COSIGN_BIN names ${COSIGN_BIN}, which is not an executable file"
# Resolved for its verdict, not for its path: scripts/verify-release.sh runs it, and a
# provenance signature nobody checked is the requirement root return 01 restored.
require_path_tool slsa-verifier >/dev/null

sha_of() {
	local out
	out="$("$SHA_BIN" "$1" 2>/dev/null)" || return 1
	printf '%s' "${out%% *}"
}

# --- arguments ----------------------------------------------------------------------------
RELEASE_TAG="${1-}"
RELEASE_COMMIT="${2-}"
if [ -z "$RELEASE_TAG" ] || [ -z "$RELEASE_COMMIT" ] || [ "$#" -ne 2 ]; then
	_err "usage: release-finalize-stable.sh <release-tag> <release-commit>"
	_err "Called without both, this operation would have to choose a candidate for itself."
	exit 2
fi

# THE SAME STRICT GRAMMAR THE §C.4 PREFLIGHT PINS. Prereleases are outside this contract by
# policy (scripts/release-preflight.sh PROD_TAG_RE), so an accepted tag here is exactly the
# shape the whole stable chain is written for.
[[ "$RELEASE_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
	refuse "release_tag must be vMAJOR.MINOR.PATCH, got '${RELEASE_TAG}'"
[[ "$RELEASE_COMMIT" =~ ^[0-9a-f]{40}$ ]] ||
	refuse "release_commit must be a full lowercase 40-hex OID"
VERSION="${RELEASE_TAG#v}"

# --- profile ------------------------------------------------------------------------------
# The destination is DERIVED, never supplied. The two accepted profiles are the production
# literal and an independently validated preprod tuple; anything else is refused rather than
# guessed, because a destination this script did not derive is a destination nobody reviewed.
REPOSITORY="${GITHUB_REPOSITORY:-}"
[ -n "$REPOSITORY" ] || blind "GITHUB_REPOSITORY is empty; there is no destination to derive"
[[ "$REPOSITORY" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]] ||
	refuse "GITHUB_REPOSITORY is not OWNER/NAME: ${REPOSITORY}"

if [ "$REPOSITORY" = "$PRODUCTION_REPOSITORY" ]; then
	PROFILE="production"
	# Production stable is the channel `latest` must name. The value is the documented
	# string enum of the update endpoint, not a boolean.
	MAKE_LATEST="true"
elif [ "${OLIVARES_RELEASE_PROFILE:-}" = "preprod" ]; then
	PROFILE="preprod"
	# ⛔ PREPROD MUST STATE ITS OWN POINTER BEHAVIOUR, and there is no default. Inheriting
	# production's `make_latest` would let an isolated rehearsal decide what "latest" means
	# on a surface nobody reviewed for it; defaulting it to false would silently make the
	# preprod act stop exercising the pointer the production act depends on. Declared or
	# refused.
	case "${OLIVARES_PREPROD_MAKE_LATEST:-}" in
	true | false | legacy) MAKE_LATEST="${OLIVARES_PREPROD_MAKE_LATEST}" ;;
	*)
		refuse "the preprod profile did not declare OLIVARES_PREPROD_MAKE_LATEST" \
			"Accepted values are true, false or legacy — the update endpoint's own enum." \
			"An isolated profile must specify its pointer behaviour instead of inheriting" \
			"production's."
		;;
	esac
	# §C.4.6 in this script's own terms: a preprod run may not name a production surface.
	case "$REPOSITORY" in
	"$PRODUCTION_REPOSITORY" | */olivares)
		refuse "the preprod profile resolved to a production-shaped destination: ${REPOSITORY}"
		;;
	esac
else
	refuse "this repository is neither the production destination nor a declared preprod one" \
		"GITHUB_REPOSITORY=${REPOSITORY}" \
		"OLIVARES_RELEASE_PROFILE=${OLIVARES_RELEASE_PROFILE:-<unset>}" \
		"There is no third profile: an unrecognised destination is refused, never promoted."
fi

OTA_PUBKEY="${OLIVARES_OTA_PUBKEY:-}"
[ -n "$OTA_PUBKEY" ] ||
	blind "OLIVARES_OTA_PUBKEY is not configured" \
		"The configured public anchor is one of the two independent OTA checks; without it" \
		"only the shipped binary's embedded anchor would be exercised, and this script does" \
		"not silently reduce to a single check."
[ -n "${GH_TOKEN:-}" ] || blind "GH_TOKEN is not set; the release API is unreachable"

CERT_OIDC_ISSUER="${CERT_OIDC_ISSUER:-$CERT_OIDC_ISSUER_DEFAULT}"

# ⛔ THE CERTIFICATE MUST NAME THIS EXACT TAG. The existing phase-2 anchor accepts ANY
# SemVer tag in this repository's release workflow, which is the right identity for "a
# release of ours" and the wrong one for "the release we are about to publish": a
# checksums.txt legitimately signed for v26.7.0 satisfies it. The tag is escaped first —
# it contains dots, and an unescaped dot matches any character exactly where the anchor
# has to be strict (the same class as aws-images.yml:315).
# shellcheck disable=SC2016 # the sed pattern needs literal regex metacharacters
repo_rx="$(printf '%s' "$REPOSITORY" | sed 's/[.[\*^$()+?{}|]/\\&/g')"
# shellcheck disable=SC2016
tag_rx="$(printf '%s' "$RELEASE_TAG" | sed 's/[.[\*^$()+?{}|]/\\&/g')"
CERT_IDENTITY_REGEXP="^https://github\.com/${repo_rx}/\.github/workflows/release\.yml@refs/tags/${tag_rx}\$"

# --- work directory -------------------------------------------------------------------
# A NEW OWNED DIRECTORY, outside the checkout. `ota-dist/` already holds the pair this
# ceremony produced and the files it downloaded earlier; verifying the REMOTE candidate out
# of those leftovers would be verifying this job's own copy of what it hopes is there.
WORKBASE="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"
[ -d "$WORKBASE" ] || blind "the temporary base ${WORKBASE} does not exist"
WORK="$(mktemp -d "${WORKBASE}/olivares-finalize.XXXXXX")" ||
	blind "could not create an owned working directory under ${WORKBASE}"
DL="$WORK/remote"
mkdir -p "$DL" || blind "could not create ${DL}"

_say "profile ${PROFILE}, destination ${REPOSITORY}, candidate ${RELEASE_TAG} (${RELEASE_COMMIT})"

# --- 1. resolve the candidate ONCE, by immutable identity ---------------------------------
tag_oid="$("$GIT_BIN" rev-parse --verify "refs/tags/${RELEASE_TAG}^{commit}" 2>/dev/null)" ||
	refuse "refs/tags/${RELEASE_TAG} does not resolve to a commit in this checkout"
head_oid="$("$GIT_BIN" rev-parse --verify "HEAD^{commit}" 2>/dev/null)" ||
	blind "could not read HEAD"
if [ "$tag_oid" != "$RELEASE_COMMIT" ] || [ "$head_oid" != "$RELEASE_COMMIT" ]; then
	refuse "the tag or the checkout is not the commit this finalization names" \
		"named    : ${RELEASE_COMMIT}" \
		"tag       : ${tag_oid}" \
		"checkout  : ${head_oid}" \
		"A moved tag is repaired by a human, never by this script: it does not create a" \
		"release, move a tag, or change a destination as repair."
fi

gh_api() { "$GH_BIN" api -H "Accept: application/vnd.github+json" "$@"; }

release_json="$WORK/release.json"
set +e
gh_api "repos/${REPOSITORY}/releases/tags/${RELEASE_TAG}" >"$release_json" 2>"$WORK/release.err"
api_rc=$?
set -e
if [ "$api_rc" -ne 0 ]; then
	blind "could not read the release for ${RELEASE_TAG} (gh exit ${api_rc})" \
		"$(head -3 "$WORK/release.err" 2>/dev/null | tr '\n' ' ')" \
		"A ceremony that cannot see the candidate must not publish it."
fi
"$JQ_BIN" -e 'type == "object"' "$release_json" >/dev/null 2>&1 ||
	blind "the release response is not a JSON object"

jqr() { "$JQ_BIN" -r "$1" "$release_json"; }
RELEASE_ID="$(jqr '.id // empty')"
[[ "$RELEASE_ID" =~ ^[1-9][0-9]*$ ]] ||
	refuse "the release has no usable numeric id (got '${RELEASE_ID:-<absent>}')"
rel_tag="$(jqr '.tag_name // empty')"
[ "$rel_tag" = "$RELEASE_TAG" ] ||
	refuse "the release resolved for ${RELEASE_TAG} reports tag_name '${rel_tag:-<absent>}'"

# ONLY the literal booleans. `null`, absent or anything else is the ABSENCE of evidence, and
# the adjacent custody script already learned that lesson the expensive way.
# ⛔ `// empty` IS THE WRONG OPERATOR FOR A BOOLEAN, and it fails in the dangerous
# direction quietly: jq's alternative operator treats `false` as absent, so
# `.prerelease // empty` answers "" for a release that is correctly NOT a prerelease and the
# switch below reads it as `<absent>`. Every legitimate draft would be refused — measured on
# the first run of this script's own battery. The posture is read as a STRING, with a
# distinct word for "the field is not there".
boolfield() { "$JQ_BIN" -r --arg k "$1" 'if has($k) then (.[$k] | tostring) else "absent" end' "$release_json"; }
rel_draft="$(boolfield draft)"
rel_prerelease="$(boolfield prerelease)"
rel_immutable="$(boolfield immutable)"

case "$rel_prerelease" in
false) ;;
*) refuse "the release reports prerelease=${rel_prerelease:-<absent>}" \
	"This finalizer publishes ordinary stable releases only." ;;
esac
case "$rel_immutable" in
false | absent) ;;
*) refuse "the release reports immutable=${rel_immutable}" \
	"An immutable release cannot accept the publication this step performs, and an" \
	"unknown posture is not a yes." ;;
esac

# `target_commitish` IS NOT THE PEELED TAG. It is a branch name or a commitish recorded at
# creation time and it is not the object the tag resolves to now; it is read for the record
# and never used as identity.
TARGET_COMMITISH="$(jqr '.target_commitish // "<absent>"')"

RECONCILE=0
case "$rel_draft" in
true) ;;
false)
	# ⛔ AN ALREADY-PUBLISHED CANDIDATE IS INSPECTED, NOT RE-PUBLISHED. Re-running the
	# ceremony over a published release would re-sign and clobber delivered bytes, which is
	# the one effect in this chain that cannot be undone. Everything below still runs; the
	# PATCH does not.
	RECONCILE=1
	_say "the release is ALREADY PUBLISHED — reconciliation mode: verify, publish nothing"
	;;
*)
	refuse "the release reports draft=${rel_draft:-<absent>}" \
		"Neither a draft to publish nor a published release to reconcile."
	;;
esac

_say "retained release id ${RELEASE_ID} (target_commitish ${TARGET_COMMITISH}, not used as identity)"

# --- 2. the complete remote asset inventory, every page -----------------------------------
assets_json="$WORK/assets.json"
set +e
"$GH_BIN" api --paginate -H "Accept: application/vnd.github+json" \
	"repos/${REPOSITORY}/releases/${RELEASE_ID}/assets?per_page=100" \
	--jq '.[]' >"$WORK/assets.ndjson" 2>"$WORK/assets.err"
assets_rc=$?
set -e
[ "$assets_rc" -eq 0 ] ||
	blind "could not enumerate the release assets (gh exit ${assets_rc})" \
		"$(head -3 "$WORK/assets.err" 2>/dev/null | tr '\n' ' ')" \
		"A partial inventory cannot establish a complete release."
"$JQ_BIN" -s '.' "$WORK/assets.ndjson" >"$assets_json" 2>/dev/null ||
	blind "the asset pages are not decodable JSON"

asset_count="$("$JQ_BIN" 'length' "$assets_json")"
[ "$asset_count" -gt 0 ] || refuse "the release carries no assets at all"

# UNIQUE ids AND names, uploaded state, safe basenames. A duplicate name means two different
# answers to one question and the reader picks by accident; an asset still uploading is not
# a published byte; a name with a path separator or a leading dash is a name this ceremony
# will not hand to a shell or a filesystem.
mapfile -t dup_names < <("$JQ_BIN" -r '[.[].name] | group_by(.) | map(select(length > 1) | .[0]) | .[]' "$assets_json")
[ "${#dup_names[@]}" -eq 0 ] || refuse "the release carries duplicate asset names:" "${dup_names[@]}"
mapfile -t dup_ids < <("$JQ_BIN" -r '[.[].id | tostring] | group_by(.) | map(select(length > 1) | .[0]) | .[]' "$assets_json")
[ "${#dup_ids[@]}" -eq 0 ] || refuse "the release carries duplicate asset ids:" "${dup_ids[@]}"
mapfile -t not_uploaded < <("$JQ_BIN" -r '.[] | select(.state != "uploaded") | "\(.name) state=\(.state // "<absent>")"' "$assets_json")
[ "${#not_uploaded[@]}" -eq 0 ] || refuse "assets are not in the uploaded state:" "${not_uploaded[@]}"

: >"$WORK/names.txt"
while IFS= read -r name; do
	[ -n "$name" ] || refuse "the release carries an asset with an empty name"
	case "$name" in
	*/* | *..* | -* | .*)
		refuse "unsafe asset basename: '${name}'"
		;;
	esac
	[[ "$name" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]] ||
		refuse "asset name contains a character this ceremony will not handle: '${name}'"
	printf '%s\n' "$name" >>"$WORK/names.txt"
done < <("$JQ_BIN" -r '.[].name' "$assets_json")

have_asset() { command grep -qxF -- "$1" "$WORK/names.txt"; }
asset_id_of() { "$JQ_BIN" -r --arg n "$1" 'map(select(.name == $n)) | .[0].id // empty' "$assets_json"; }
asset_size_of() { "$JQ_BIN" -r --arg n "$1" 'map(select(.name == $n)) | if (.[0] | type) == "object" and (.[0].size != null) then (.[0].size | tostring) else "" end' "$assets_json"; }
asset_url_of() { "$JQ_BIN" -r --arg n "$1" 'map(select(.name == $n)) | .[0].browser_download_url // empty' "$assets_json"; }
asset_digest_of() { "$JQ_BIN" -r --arg n "$1" 'map(select(.name == $n)) | .[0].digest // ""' "$assets_json"; }

# FETCH BY EXACT ASSET ID. `gh release download` selects by pattern against whatever the
# release carries now, and `/releases/latest` or a browser URL is a MUTABLE selector: the
# inventory above is the set this script ruled on, so the bytes must come from those exact
# ids and nothing else.
fetch_asset() {
	local name="$1" id dest rc
	id="$(asset_id_of "$name")"
	[[ "$id" =~ ^[1-9][0-9]*$ ]] || refuse "no usable asset id for ${name}"
	dest="$DL/$name"
	set +e
	"$GH_BIN" api -H "Accept: application/octet-stream" \
		"repos/${REPOSITORY}/releases/assets/${id}" >"$dest" 2>"$WORK/fetch.err"
	rc=$?
	set -e
	[ "$rc" -eq 0 ] || blind "could not download ${name} by asset id ${id} (gh exit ${rc})" \
		"$(head -2 "$WORK/fetch.err" 2>/dev/null | tr '\n' ' ')"
	[ -s "$dest" ] || refuse "the downloaded ${name} is empty"
	# The metadata size and digest are CORROBORATION; the bytes on disk are the subject.
	local want_size have_size want_digest have_digest
	want_size="$(asset_size_of "$name")"
	have_size="$(wc -c <"$dest" | tr -d ' ')"
	if [[ "$want_size" =~ ^[0-9]+$ ]] && [ "$want_size" != "$have_size" ]; then
		refuse "${name}: the release says ${want_size} bytes, ${have_size} arrived"
	fi
	have_digest="$(sha_of "$dest")" || blind "could not digest ${name}"
	want_digest="$(asset_digest_of "$name")"
	case "$want_digest" in
	sha256:*)
		[ "${want_digest#sha256:}" = "$have_digest" ] ||
			refuse "${name}: the release metadata digest disagrees with the downloaded bytes" \
				"metadata ${want_digest#sha256:}" "bytes    ${have_digest}"
		;;
	esac
	printf '%s' "$have_digest"
}

# --- 3. authenticate source and origin BEFORE admitting anything --------------------------
for required in checksums.txt checksums.txt.sig checksums.txt.pem release-commit.txt release-build-context.json; do
	have_asset "$required" ||
		refuse "the release is missing ${required}" \
			"Phase 1 published no verifiable evidence under that name, so nothing below could" \
			"be authenticated."
done
fetch_asset checksums.txt >/dev/null
fetch_asset checksums.txt.sig >/dev/null
fetch_asset checksums.txt.pem >/dev/null
fetch_asset release-commit.txt >/dev/null
fetch_asset release-build-context.json >/dev/null

cosign_verify_blob() { # cosign_verify_blob <cert> <sig> <subject>
	set +e
	bash "$COSIGN_WRAPPER" verify-blob \
		--certificate "$1" --signature "$2" \
		--certificate-identity-regexp "$CERT_IDENTITY_REGEXP" \
		--certificate-oidc-issuer "$CERT_OIDC_ISSUER" \
		--certificate-github-workflow-repository "$REPOSITORY" \
		"$3" >"$WORK/cosign.out" 2>&1
	local rc=$?
	set -e
	return $rc
}

cosign_verify_blob "$DL/checksums.txt.pem" "$DL/checksums.txt.sig" "$DL/checksums.txt" ||
	refuse "checksums.txt is not signed by this repository's release workflow on ${RELEASE_TAG}" \
		"identity required: ${CERT_IDENTITY_REGEXP}" \
		"$(tail -3 "$WORK/cosign.out" 2>/dev/null | tr '\n' ' ')"
_say "checksums.txt carries this workflow's signature for this exact tag"

# The signed checksum rows, parsed once and strictly. Everything admitted below is measured
# against THIS table, which is the only document in the release a signature covers end to end.
# THE TABLE IS AN EXACT MAP, NOT A PATTERN SEARCH. Looking a name up with a regex means
# escaping it correctly every time, and a name that is a valid artifact and an accidental
# metacharacter is exactly the input a control must not get wrong.
declare -A SUMS=()
: >"$WORK/sum-names.txt"
while IFS= read -r row || [ -n "$row" ]; do
	row="${row%$'\r'}"
	[ -n "$row" ] || refuse "checksums.txt contains an empty row"
	[[ "$row" =~ ^([0-9a-f]{64})\ \ ([A-Za-z0-9][A-Za-z0-9._+-]*)$ ]] ||
		refuse "malformed or unsafe checksum row: '${row}'"
	sum_digest="${BASH_REMATCH[1]}"
	sum_name="${BASH_REMATCH[2]}"
	[ -z "${SUMS[$sum_name]+x}" ] || refuse "checksums.txt lists ${sum_name} twice"
	SUMS["$sum_name"]="$sum_digest"
	printf '%s\n' "$sum_name" >>"$WORK/sum-names.txt"
done <"$DL/checksums.txt"
[ -s "$WORK/sum-names.txt" ] || refuse "checksums.txt lists nothing"
SIGNED_MEMBERS="$(wc -l <"$WORK/sum-names.txt" | tr -d ' ')"

sum_digest_of() { printf '%s' "${SUMS[$1]:-}"; }
in_checksums() { [ -n "${SUMS[$1]+x}" ]; }

bind_to_checksums() { # bind_to_checksums <name> <observed-digest>
	local want
	in_checksums "$1" || refuse "${1} is not listed in the signed checksums"
	want="$(sum_digest_of "$1")"
	[ "$want" = "$2" ] ||
		refuse "${1} does not match its entry in the signed checksums" \
			"signed  ${want}" "bytes   ${2}"
}

ev_digest="$(sha_of "$DL/release-commit.txt")" || blind "could not digest release-commit.txt"
bind_to_checksums release-commit.txt "$ev_digest"
IFS= read -r phase1_oid <"$DL/release-commit.txt" || true
phase1_oid="${phase1_oid%$'\r'}"
[[ "$phase1_oid" =~ ^[0-9a-f]{40}$ ]] ||
	refuse "the commit evidence is not a bare 40-hex OID: '${phase1_oid}'"
want_ev="$(printf '%s\n' "$phase1_oid" | "$SHA_BIN")"
want_ev="${want_ev%% *}"
[ "$want_ev" = "$ev_digest" ] ||
	refuse "release-commit.txt is not exactly one OID and a newline"
[ "$phase1_oid" = "$RELEASE_COMMIT" ] ||
	refuse "phase 1 recorded ${phase1_oid}, this finalization names ${RELEASE_COMMIT}"
_say "source bound: phase 1 built ${phase1_oid}, and the signed checksums say so"

# --- the build context, and the run it names ----------------------------------------------
ctx="$DL/release-build-context.json"
ctx_digest="$(sha_of "$ctx")" || blind "could not digest release-build-context.json"
bind_to_checksums release-build-context.json "$ctx_digest"
ctx_bytes="$(wc -c <"$ctx" | tr -d ' ')"
[ "$ctx_bytes" -le "$BUILD_CONTEXT_MAX_BYTES" ] ||
	refuse "release-build-context.json is ${ctx_bytes} bytes, over the ${BUILD_CONTEXT_MAX_BYTES}-byte cap"

# AUTHENTICATED BEFORE PARSED. The digest above is the authentication; the parse below is
# the only thing allowed to read the bytes, and it is strict about exactly which fields may
# be present — a document with an extra field is a document written by something else.
"$JQ_BIN" -e '
  type == "object"
  and (keys_unsorted | sort) == (["schema","schema_version","repository_id","repository","event","ref","commit","run_id","run_attempt"] | sort)
  and (.schema_version == 1)
  and (.repository_id | type == "string")
  and (.run_id | type == "string")
  and (.run_attempt | type == "string")
' "$ctx" >/dev/null 2>&1 ||
	refuse "release-build-context.json does not carry exactly the v1 fields with their declared types"

ctx_get() { "$JQ_BIN" -r --arg k "$1" '.[$k]' "$ctx"; }
[ "$(ctx_get schema)" = "$BUILD_CONTEXT_SCHEMA" ] ||
	refuse "the build context declares schema '$(ctx_get schema)', not ${BUILD_CONTEXT_SCHEMA}"
[ "$(ctx_get event)" = "push" ] ||
	refuse "the build context records event '$(ctx_get event)', not push"
[ "$(ctx_get ref)" = "refs/tags/${RELEASE_TAG}" ] ||
	refuse "the build context records ref '$(ctx_get ref)', not refs/tags/${RELEASE_TAG}"
[ "$(ctx_get commit)" = "$RELEASE_COMMIT" ] ||
	refuse "the build context records commit '$(ctx_get commit)', not ${RELEASE_COMMIT}"
[ "$(ctx_get repository)" = "$REPOSITORY" ] ||
	refuse "the build context records repository '$(ctx_get repository)', not ${REPOSITORY}"
CTX_REPO_ID="$(ctx_get repository_id)"
CTX_RUN_ID="$(ctx_get run_id)"
CTX_RUN_ATTEMPT="$(ctx_get run_attempt)"
for pair in "repository_id=${CTX_REPO_ID}" "run_id=${CTX_RUN_ID}" "run_attempt=${CTX_RUN_ATTEMPT}"; do
	[[ "${pair#*=}" =~ ^([1-9][0-9]*|0)$ ]] ||
		refuse "the build context's ${pair%%=*} is not a canonical decimal string: '${pair#*=}'"
done

# THE RUN IS READ, NOT INFERRED. Nothing here looks for the newest or the nearest run: the
# context names one, and that one must agree and must have SUCCEEDED. An unrelated
# successful run of the same workflow is exactly what this refuses.
run_json="$WORK/run.json"
set +e
gh_api "repos/${REPOSITORY}/actions/runs/${CTX_RUN_ID}/attempts/${CTX_RUN_ATTEMPT}" \
	>"$run_json" 2>"$WORK/run.err"
run_rc=$?
set -e
[ "$run_rc" -eq 0 ] ||
	blind "could not read run ${CTX_RUN_ID} attempt ${CTX_RUN_ATTEMPT} (gh exit ${run_rc})" \
		"$(head -3 "$WORK/run.err" 2>/dev/null | tr '\n' ' ')" \
		"This read needs the actions: read permission the phase-2 job declares."

runq() { "$JQ_BIN" -r "$1" "$run_json"; }
[ "$(runq '.repository.full_name // empty')" = "$REPOSITORY" ] ||
	refuse "run ${CTX_RUN_ID} belongs to '$(runq '.repository.full_name // "<absent>"')', not ${REPOSITORY}"
[ "$("$JQ_BIN" -r 'if (.repository | type) == "object" and (.repository.id != null) then (.repository.id | tostring) else "absent" end' "$run_json")" = "$CTX_REPO_ID" ] ||
	refuse "run ${CTX_RUN_ID} reports repository id '$(runq '.repository.id // "<absent>"')', the context says ${CTX_REPO_ID}"
[ "$(runq '.path // empty')" = "$RELEASE_WORKFLOW_PATH" ] ||
	refuse "run ${CTX_RUN_ID} is workflow '$(runq '.path // "<absent>"')', not ${RELEASE_WORKFLOW_PATH}"
[ "$(runq '.event // empty')" = "push" ] ||
	refuse "run ${CTX_RUN_ID} was triggered by '$(runq '.event // "<absent>"')', not push"
[ "$(runq '.head_sha // empty')" = "$RELEASE_COMMIT" ] ||
	refuse "run ${CTX_RUN_ID} built '$(runq '.head_sha // "<absent>"')', not ${RELEASE_COMMIT}"
[ "$("$JQ_BIN" -r 'if has("run_attempt") then (.run_attempt | tostring) else "absent" end' "$run_json")" = "$CTX_RUN_ATTEMPT" ] ||
	refuse "the API reports attempt '$(runq '.run_attempt // "<absent>"')', the context says ${CTX_RUN_ATTEMPT}"
[ "$(runq '.status // empty')" = "completed" ] ||
	refuse "run ${CTX_RUN_ID} attempt ${CTX_RUN_ATTEMPT} is '$(runq '.status // "<absent>"')', not completed" \
		"An artifact set from an unfinished run is not a candidate."
[ "$(runq '.conclusion // empty')" = "success" ] ||
	refuse "run ${CTX_RUN_ID} attempt ${CTX_RUN_ATTEMPT} concluded '$(runq '.conclusion // "<absent>"')', not success"
_say "origin bound: run ${CTX_RUN_ID} attempt ${CTX_RUN_ATTEMPT} completed successfully on ${RELEASE_WORKFLOW_PATH}"

# --- 4. the security channel still refuses ------------------------------------------------
# BOTH DIRECTIONS, exactly as the attachment step does. The tag's own declaration is the
# immutable authority; the remote inventory can only ADD red.
set +e
declaration="$(bash "$ROOT/scripts/release-ota-channel.sh" "$VERSION" 2>&1)"
decl_rc=$?
set -e
[ "$decl_rc" -eq 0 ] ||
	refuse "the OTA channel gate refused for ${VERSION}: ${declaration}" \
		"A finalizer that cannot tell whether this is a security release must not finish it."
case "${declaration%%$'\t'*}" in
none) ;;
security)
	refuse "${RELEASE_TAG} DECLARED a security release: ${declaration#*$'\t'}" \
		"This finalizer publishes the stable channel only. Cryptographic verification of" \
		"the security channel is #644; until it lands, a release that declared advisories" \
		"is not published here. Nothing was published."
	;;
*)
	refuse "unknown verdict from the OTA channel gate: ${declaration}"
	;;
esac
for sec in security-manifest.json security-manifest.json.sig; do
	! have_asset "$sec" ||
		refuse "the release carries ${sec}, and this finalizer cannot certify it" \
			"A name is not a verified signature. Do not publish this release by hand either."
done
_say "security channel: this release declared none and carries none"

# --- 5. derive the required inventory and admit it ----------------------------------------
BASE_ARCHIVES=()
FIPS_ARCHIVES=()
for goos in "${RECIPE_GOOS[@]}"; do
	for goarch in "${RECIPE_GOARCH[@]}"; do
		BASE_ARCHIVES+=("olivares_${VERSION}_${goos}_${goarch}.tar.gz")
		FIPS_ARCHIVES+=("olivares_${VERSION}_fips_${goos}_${goarch}.tar.gz")
	done
done
ALL_ARCHIVES=("${BASE_ARCHIVES[@]}" "${FIPS_ARCHIVES[@]}")
INSTALLER="olivares-install-${VERSION}.sh"

# Members the signed checksums MUST list, derived from the recipe above.
REQUIRED_CHECKSUMMED=(release-commit.txt release-build-context.json "$INSTALLER")
for a in "${ALL_ARCHIVES[@]}"; do
	REQUIRED_CHECKSUMMED+=("$a" "${a}.spdx.sbom.json" "${a}.cdx.sbom.json")
done

# Members the release must carry that the checksums deliberately do NOT cover: they are
# produced after goreleaser has written checksums.txt, by the workflow steps named beside
# each group. They are required all the same — an unattested archive is not a candidate.
REQUIRED_UNCHECKSUMMED=(
	checksums.txt.sig checksums.txt.pem
	stable-manifest.json stable-manifest.json.sig
	stable-manifest.json.pipeline.sig stable-manifest.json.pipeline.pem
	olivares.vex.openvex.json image.spdx.sbom.json
)
for a in "${ALL_ARCHIVES[@]}"; do
	REQUIRED_UNCHECKSUMMED+=("${a}.sbom.sigstore.json" "${a}.vex.sigstore.json" "${a}.vex.openvex.json")
done

missing=()
for name in "${REQUIRED_CHECKSUMMED[@]}"; do
	in_checksums "$name" || missing+=("${name} (absent from the signed checksums)")
	have_asset "$name" || missing+=("${name} (absent from the release)")
done
for name in "${REQUIRED_UNCHECKSUMMED[@]}"; do
	have_asset "$name" || missing+=("${name} (absent from the release)")
done
if [ "${#missing[@]}" -gt 0 ]; then
	refuse "the candidate is not a complete release" "${missing[@]}" \
		"The required set is DERIVED from this repository's GoReleaser recipe and release" \
		"workflow for ${VERSION}: ${#BASE_ARCHIVES[@]} base archives, ${#FIPS_ARCHIVES[@]} FIPS archives and their" \
		"generated sidecars. A signed manifest that describes fewer platforms than the" \
		"release promises is exactly what this refuses."
fi

# PACKAGES BY COVERAGE, NOT BY SPELLING. The native package filename is GoReleaser's
# conventional form, which this tree cannot derive offline; what it CAN derive is the recipe
# — three formats over two architectures, built from the base binary only — so the rule is
# that the signed checksums carry exactly one member per (format, architecture) and that
# every combination is covered. A missing .rpm for arm64 is caught; the exact spelling is
# not asserted, and that limit is written down rather than papered over.
pkg_unclassified=()
for fmt in "${RECIPE_PACKAGE_FORMATS[@]}"; do
	for goarch in "${RECIPE_PACKAGE_GOARCH[@]}"; do
		found=0
		while IFS= read -r alias; do
			while IFS= read -r cand; do
				case "$cand" in
				*."$fmt")
					case "$cand" in
					*"$alias"*) found=$((found + 1)) ;;
					esac
					;;
				esac
			done <"$WORK/sum-names.txt"
		done < <(arch_aliases "$goarch")
		case "$found" in
		1) ;;
		0) pkg_unclassified+=("no ${fmt} package for ${goarch} in the signed checksums") ;;
		*) pkg_unclassified+=("${found} ${fmt} packages match ${goarch}; the ceremony cannot choose") ;;
		esac
	done
done
[ "${#pkg_unclassified[@]}" -eq 0 ] ||
	refuse "the native package set is incomplete or ambiguous" "${pkg_unclassified[@]}"

# EXTRA ORDINARY ARTIFACTS ARE NAMED, NOT TOLERATED. Every checksums row must be one of the
# derived members or an admitted package; anything else is a file in the signed set that
# this recipe does not explain, and "I do not recognise it" is not "it is fine".
unexplained=()
while IFS= read -r name; do
	explained=0
	for want in "${REQUIRED_CHECKSUMMED[@]}"; do
		[ "$name" = "$want" ] && explained=1 && break
	done
	if [ "$explained" -eq 0 ]; then
		for fmt in "${RECIPE_PACKAGE_FORMATS[@]}"; do
			case "$name" in *."$fmt") explained=1 ;; esac
		done
	fi
	[ "$explained" -eq 1 ] || unexplained+=("$name")
done <"$WORK/sum-names.txt"
[ "${#unexplained[@]}" -eq 0 ] ||
	refuse "the signed checksums list members this release recipe does not explain:" "${unexplained[@]}" \
		"An unexplained signed member is either recipe drift or a substitution; both are" \
		"answered by a human, not by publishing."

# THE PROVENANCE ASSET, SELECTED BEFORE THE INVENTORY IS ADMITTED. RUN_SLSA is a profile
# constant that scripts/release-preflight.sh refuses to set false, so a release without
# provenance is a release that did not follow the reviewed recipe. Exactly one, never a first
# match — and naming it here is what lets the admission below admit ONE provenance document
# instead of any file whose name happens to end in .intoto.jsonl.
prov_names=()
while IFS= read -r name; do
	case "$name" in *.intoto.jsonl) prov_names+=("$name") ;; esac
done <"$WORK/names.txt"
case "${#prov_names[@]}" in
1) ;;
0) refuse "the release carries no SLSA provenance (*.intoto.jsonl)" \
	"RUN_SLSA is a profile constant this repository's preflight refuses to disable, so" \
	"its absence means the release did not follow the reviewed recipe." ;;
*) refuse "the release carries ${#prov_names[@]} provenance documents; an ambiguous selection is refused:" "${prov_names[@]}" ;;
esac
PROVENANCE="${prov_names[0]}"

# ⛔ EVERY ASSET IS ADMITTED BY NAME, NOT BY EXTENSION (root return 02, F5). This block used
# to ask whether a name ended in one of five archive or package extensions and, if so, whether
# the signed checksums covered it. Everything else on the release was published unexamined: an
# extra unsigned `.sh` — and the recipe itself ships an installer with that extension, so the
# name class is ordinary — an extra `.json`, or a `checksums.txt.bak` sitting next to the
# signed table. The rule is now the same shape as the one two blocks above, and it is total:
# an asset is either a member of the signed checksums, a required sidecar this recipe derives,
# the signed table itself, or the one admitted provenance document. Anything else is named and
# refused, because "I do not recognise it" is not "it is fine".
declare -A ADMITTED_ASSETS=()
ADMITTED_ASSETS["checksums.txt"]=1
while IFS= read -r name; do
	ADMITTED_ASSETS["$name"]=1
done <"$WORK/sum-names.txt"
for name in "${REQUIRED_UNCHECKSUMMED[@]}"; do
	ADMITTED_ASSETS["$name"]=1
done
ADMITTED_ASSETS["$PROVENANCE"]=1
stray=()
while IFS= read -r name; do
	[ -n "${ADMITTED_ASSETS[$name]+x}" ] || stray+=("$name")
done <"$WORK/names.txt"
[ "${#stray[@]}" -eq 0 ] ||
	refuse "the release carries assets this ceremony does not admit:" "${stray[@]}" \
		"Every asset must be a member of the signed checksums, a sidecar the release recipe" \
		"requires, the signed checksums themselves, or the single SLSA provenance document." \
		"An unadmitted asset is an unsigned file sitting where users download."

# --- download and rehash EVERY signed member, plus the required sidecars -------------------
_say "admitting ${#REQUIRED_CHECKSUMMED[@]} derived members, ${SIGNED_MEMBERS} signed rows and ${#REQUIRED_UNCHECKSUMMED[@]} generated sidecars"
while IFS= read -r name; do
	case "$name" in
	release-commit.txt | release-build-context.json | checksums.txt) continue ;;
	esac
	got="$(fetch_asset "$name")"
	bind_to_checksums "$name" "$got"
done <"$WORK/sum-names.txt"
for name in "${REQUIRED_UNCHECKSUMMED[@]}"; do
	case "$name" in
	checksums.txt.sig | checksums.txt.pem) continue ;;
	esac
	fetch_asset "$name" >/dev/null
done

# The provenance document itself, selected and admitted above.
fetch_asset "$PROVENANCE" >/dev/null

# --- 6. the OTA pair: same bytes, real signature, complete platform matrix -----------------
# THE PAIR MUST BE THE ONE THIS CEREMONY JUST ATTACHED, byte for byte. Finding the two
# filenames in the inventory proves only that two names exist; an out-of-band writer who
# replaced them between the attachment and this step keeps both names.
ATTACHED_PAIR_COMPARED=0
have_attached_pair=1
for local_pair in ota-dist/stable-manifest.json ota-dist/stable-manifest.json.sig; do
	[ -f "$local_pair" ] || have_attached_pair=0
done
if [ "$have_attached_pair" -eq 1 ]; then
	for pair in stable-manifest.json stable-manifest.json.sig; do
		local_digest="$(sha_of "ota-dist/${pair}")" || blind "could not digest ota-dist/${pair}"
		remote_digest="$(sha_of "$DL/${pair}")" || blind "could not digest the downloaded ${pair}"
		[ "$local_digest" = "$remote_digest" ] ||
			refuse "the remote ${pair} is not the bytes this ceremony attached" \
				"attached ${local_digest}" "remote   ${remote_digest}" \
				"Something rewrote the asset after the attachment step. Nothing is published."
	done
	ATTACHED_PAIR_COMPARED=1
	_say "the remote OTA pair is byte-identical to the pair this ceremony attached"
elif [ "$RECONCILE" -eq 1 ]; then
	# A RE-INSPECTION HAS NO ATTACHMENT OF ITS OWN, and inventing one would be worse than
	# saying so: the comparison exists to catch a writer between THIS job's attachment and
	# THIS job's publication, and a later operator inspecting a published release did not
	# attach anything. The check is declared absent, not quietly passed.
	_say "reconciliation: no attachment from this job to compare against (declared limit)"
else
	blind "ota-dist/stable-manifest.json{,.sig} are not in the workspace" \
		"This finalizer compares the remote pair against the exact bytes the signing step" \
		"produced in this job; without them there is nothing to compare."
fi

cosign_verify_blob "$DL/stable-manifest.json.pipeline.pem" "$DL/stable-manifest.json.pipeline.sig" \
	"$DL/stable-manifest.json" ||
	refuse "the remote stable-manifest.json is not the bytes phase 1 produced and signed" \
		"$(tail -3 "$WORK/cosign.out" 2>/dev/null | tr '\n' ' ')" \
		"The pipeline signature is what binds the manifest's POLICY — expires, min_version," \
		"rollout, security/advisories, notes, channel — which checksums.txt does not cover."

# DIRECTION ONE: the CONFIGURED public anchor, verified by the CHECKOUT's own verifier.
# `go run` at this tag, with the anchor supplied explicitly, so the verdict does not come
# from the artifact under test.
verify_args=(
	release verify-manifest
	--manifest "$DL/stable-manifest.json"
	--sig "$DL/stable-manifest.json.sig"
	--checksums "$DL/checksums.txt"
	--dir "$DL"
	--expect-channel stable
	--expect-version "$VERSION"
)
set +e
(cd "$ROOT" && "$GO_BIN" run ./cmd/olivares "${verify_args[@]}" --pubkey "$OTA_PUBKEY") \
	>"$WORK/verify-anchor.out" 2>&1
va_rc=$?
set -e
[ "$va_rc" -eq 0 ] ||
	refuse "the OTA signature does not verify under the configured public anchor" \
		"$(tail -5 "$WORK/verify-anchor.out" 2>/dev/null | tr '\n' ' ')"
_say "OTA signature verifies under the configured public anchor, over the published bytes"

# DIRECTION TWO: the SHIPPED binary's own embedded anchor, with NO --pubkey. This is the
# check a user's own binary performs, and no other run in this job can substitute for it.
# The archive it comes out of has already been bound to the signed checksums above.
SHIPPED_ARCHIVE="olivares_${VERSION}_linux_amd64.tar.gz"
have_asset "$SHIPPED_ARCHIVE" || refuse "the release carries no ${SHIPPED_ARCHIVE} to run"
mkdir -p "$WORK/community"
# ⛔ SYMLINK CONTRACT (INT-22, M79-M81), the same one the signing step carries: this extracts
# a member and RUNS it, and a link entry turns that name into something else entirely.
members="$("$TAR_BIN" -tzf "$DL/$SHIPPED_ARCHIVE")" ||
	blind "could not list the members of ${SHIPPED_ARCHIVE}; nothing was extracted"
links="$("$TAR_BIN" -tvzf "$DL/$SHIPPED_ARCHIVE" | command grep -E '^[hl]' || true)"
[ -z "$links" ] ||
	refuse "${SHIPPED_ARCHIVE} contains link entries; a release archive carries regular files only:" "$links"
# ⛔ EVERY MEMBER, NOT THE FIRST ONE (root return 02, F7). `$members` is the newline-joined
# listing, and `case` matches the WHOLE string: `*..*` is anchored on both sides and does fire
# anywhere, but `/*` is anchored at the start, so it only ever saw the first member. An
# absolute path on any later line passed a guard whose whole purpose is the INT-22 contract.
# Each member is now tested on its own, and an empty listing is a refusal rather than a
# vacuous pass.
[ -n "$members" ] || refuse "${SHIPPED_ARCHIVE} lists no members; refusing to extract"
while IFS= read -r member; do
	[ -n "$member" ] || refuse "${SHIPPED_ARCHIVE} lists an empty member name; refusing to extract"
	case "$member" in
	*..*) refuse "an archive member of ${SHIPPED_ARCHIVE} contains '..'; refusing to extract" "member: ${member}" ;;
	/*) refuse "an archive member of ${SHIPPED_ARCHIVE} is an absolute path; refusing to extract" "member: ${member}" ;;
	esac
done <<<"$members"
"$TAR_BIN" -xzf "$DL/$SHIPPED_ARCHIVE" -C "$WORK/community" olivares ||
	blind "could not extract olivares from ${SHIPPED_ARCHIVE}"
if [ -L "$WORK/community/olivares" ] || [ ! -f "$WORK/community/olivares" ]; then
	refuse "the extracted olivares is not a regular file; nothing has been chmod-ed or executed"
fi
chmod 0755 "$WORK/community/olivares"
set +e
"$WORK/community/olivares" "${verify_args[@]}" >"$WORK/verify-shipped.out" 2>&1
vs_rc=$?
set -e
[ "$vs_rc" -eq 0 ] ||
	refuse "the SHIPPED binary's embedded OTA anchor does not accept these published bytes" \
		"$(tail -5 "$WORK/verify-shipped.out" 2>/dev/null | tr '\n' ' ')" \
		"This is the check every user's own binary performs. If the configured anchor above" \
		"accepted what this rejects, the artifact is the suspect."
_say "the shipped binary's embedded anchor accepts the published bytes"

# THE PLATFORM MATRIX. verify-manifest binds every artifact the manifest NAMES; it cannot
# know which artifacts the manifest OMITS. The base archive set of the admitted recipe is
# the completeness claim, and it is checked here against the manifest itself.
manifest_names="$("$JQ_BIN" -r '.artifacts[]?.filename // empty' "$DL/stable-manifest.json")" ||
	refuse "the stable manifest does not decode as JSON with an artifacts array"
: >"$WORK/manifest-names.txt"
printf '%s\n' "$manifest_names" | command grep -v '^$' >"$WORK/manifest-names.txt" || true
matrix_problems=()
for a in "${BASE_ARCHIVES[@]}"; do
	command grep -qxF -- "$a" "$WORK/manifest-names.txt" ||
		matrix_problems+=("the manifest does not carry ${a}")
done
while IFS= read -r mn; do
	[ -n "$mn" ] || continue
	found=0
	for a in "${BASE_ARCHIVES[@]}"; do [ "$mn" = "$a" ] && found=1; done
	[ "$found" -eq 1 ] || matrix_problems+=("the manifest carries ${mn}, which is not a base archive of ${VERSION}")
done <"$WORK/manifest-names.txt"
[ "${#matrix_problems[@]}" -eq 0 ] ||
	refuse "the signed manifest does not describe the complete platform matrix" "${matrix_problems[@]}" \
		"The recipe for ${VERSION} publishes ${#BASE_ARCHIVES[@]} base archives. A manifest listing fewer" \
		"platforms passes every digest check and still leaves most of the fleet unserved."
_say "the signed manifest covers exactly the ${#BASE_ARCHIVES[@]}-platform base matrix"

# --- 7. provenance subjects, and the strict supply-chain verification ----------------------
# ⛔ THIS EXTRACTION IS A CONSISTENCY CHECK, NOT AN AUTHENTICATION. The subjects tie the
# document to these bytes; only slsa-verifier checks the signature over it, and it is handed
# the ORIGINAL complete file below, never anything reconstructed here.
#
# TWO CONTRACTUALLY VALID CARRIERS, AND NEITHER IS GUESSED AT. The pinned
# generator_generic_slsa3.yml@v2.1.0 emits a Sigstore bundle
# (mediaType application/vnd.dev.sigstore.bundle.v0.3+json) whose in-toto statement is nested
# under .dsseEnvelope; the Go builder emits a bare DSSE envelope with the statement at the top
# level. A document that presents BOTH is two competing envelopes in one file and is refused
# rather than resolved by precedence: choosing one would decide which claim is checked.
prov_payload="$WORK/provenance-payload.json"
# ONE ENVELOPE, NOT THE FIRST OF SEVERAL. The file is JSON Lines, so `head -1` would both
# pick a document by position — the first-match defect scripts/verify-release.sh already
# fixed for provenance selection — and risk a SIGPIPE-driven 141 under pipefail. The whole
# file is slurped and its length asserted instead.
prov_docs="$("$JQ_BIN" -s 'length' "$DL/$PROVENANCE" 2>/dev/null || printf 'x')"
[ "$prov_docs" = "1" ] ||
	refuse "${PROVENANCE} holds ${prov_docs} JSON documents; exactly one envelope is expected"
prov_shape="$("$JQ_BIN" -s -r '
  .[0] as $d
  | if ($d | type) != "object" then "not-an-object"
    else
      (($d | has("payload")) and (($d.payload | type) == "string")) as $bare
      | ((($d.dsseEnvelope | type) == "object")
         and ($d.dsseEnvelope | has("payload"))
         and (($d.dsseEnvelope.payload | type) == "string")) as $bundle
      | if $bare and $bundle then "ambiguous"
        elif $bundle then "bundle"
        elif $bare then "dsse"
        else "unrecognised" end
    end' "$DL/$PROVENANCE" 2>/dev/null || printf 'unreadable')"
case "$prov_shape" in
bundle) prov_env='.[0].dsseEnvelope' ;;
dsse) prov_env='.[0]' ;;
ambiguous)
	refuse "${PROVENANCE} carries both a top-level DSSE payload and a nested dsseEnvelope" \
		"Two competing envelopes in one document; this ceremony does not choose between them."
	;;
*)
	refuse "${PROVENANCE} is not a provenance document this ceremony recognises (${prov_shape})" \
		"Expected one Sigstore bundle with .dsseEnvelope.payload, or one bare DSSE envelope" \
		"with a top-level .payload."
	;;
esac
prov_ptype="$("$JQ_BIN" -s -r "${prov_env}.payloadType // empty" "$DL/$PROVENANCE" 2>/dev/null || true)"
[ "$prov_ptype" = "application/vnd.in-toto+json" ] ||
	refuse "the ${prov_shape} envelope declares payloadType '${prov_ptype:-<absent>}', not application/vnd.in-toto+json"
prov_b64="$("$JQ_BIN" -s -r "${prov_env}.payload // empty" "$DL/$PROVENANCE" 2>/dev/null || true)"
[ -n "$prov_b64" ] || refuse "the ${prov_shape} envelope carries no payload"
printf '%s' "$prov_b64" | "$B64_BIN" -d >"$prov_payload" 2>/dev/null ||
	refuse "the provenance payload is not decodable base64"
[ -s "$prov_payload" ] || refuse "the provenance payload is empty"
"$JQ_BIN" -e '.subject | type == "array" and length > 0' "$prov_payload" >/dev/null 2>&1 ||
	refuse "the provenance statement carries no subjects"
prov_missing=()
for a in "${BASE_ARCHIVES[@]}" "${FIPS_ARCHIVES[@]}"; do
	want="$(sum_digest_of "$a")"
	"$JQ_BIN" -e --arg d "$want" 'any(.subject[]; .digest.sha256 == $d)' "$prov_payload" >/dev/null 2>&1 ||
		prov_missing+=("${a} is not a subject of ${PROVENANCE}")
done
[ "${#prov_missing[@]}" -eq 0 ] ||
	refuse "the SLSA provenance does not cover every released archive" "${prov_missing[@]}"
_say "provenance ${PROVENANCE} (${prov_shape}) names every released archive as a subject"

# STRICT PUBLICATION MODE. The consumer verifier skips what is absent and says so; that is
# right for a user with a partial download and wrong for a publisher. Here a missing required
# member, a missing required tool or a skipped required verification is a failure — there is no
# path that publishes over a check that did not run. It runs in the download directory, over
# exactly the admitted bytes, and over the ORIGINAL provenance document.
#
# THE IDENTITY IS PASSED, NOT LOOKED UP. It is derived above from the repository this script
# resolved and the exact tag it is publishing. The delegated verification must not build its
# own anchor from a literal (no other profile could satisfy it) nor read OLIVARES_CERT_*,
# which are repository variables an admin can set without review.
set +e
(cd "$DL" && bash "$ROOT/scripts/verify-release.sh" --strict-publication \
	--source-tag "$RELEASE_TAG" \
	--cert-identity "$CERT_IDENTITY_REGEXP" \
	--cert-oidc-issuer "$CERT_OIDC_ISSUER" \
	--source-uri "github.com/${REPOSITORY}") >"$WORK/verify-release.out" 2>&1
vr_rc=$?
set -e
[ "$vr_rc" -eq 0 ] ||
	refuse "strict publication verification of the complete artifact set failed (exit ${vr_rc})" \
		"$(tail -6 "$WORK/verify-release.out" 2>/dev/null | tr '\n' ' ')"
_say "strict publication verification passed over the complete admitted set"

# --- 8. the evidence record ---------------------------------------------------------------
# Evidence, not authority: it records what was verified so a later reader can reconstruct the
# decision. It never grants one.
build_evidence() { # build_evidence <outcome>
	"$JQ_BIN" -n \
		--arg schema 'olivares.ai/release-finalization/v1' \
		--arg outcome "$1" \
		--arg profile "$PROFILE" \
		--arg repository "$REPOSITORY" \
		--arg tag "$RELEASE_TAG" \
		--arg commit "$RELEASE_COMMIT" \
		--arg release_id "$RELEASE_ID" \
		--arg run_id "$CTX_RUN_ID" \
		--arg run_attempt "$CTX_RUN_ATTEMPT" \
		--arg make_latest "$MAKE_LATEST" \
		--arg checksums_sha256 "$(sha_of "$DL/checksums.txt")" \
		--arg manifest_sha256 "$(sha_of "$DL/stable-manifest.json")" \
		--arg manifest_sig_sha256 "$(sha_of "$DL/stable-manifest.json.sig")" \
		--arg provenance "$PROVENANCE" \
		--argjson signed_members "$SIGNED_MEMBERS" \
		--argjson assets "$asset_count" \
		--argjson attached_pair_compared "$ATTACHED_PAIR_COMPARED" \
		'{schema:$schema,outcome:$outcome,profile:$profile,repository:$repository,
		  tag:$tag,commit:$commit,release_id:$release_id,
		  origin:{run_id:$run_id,run_attempt:$run_attempt},
		  candidate:{checksums_sha256:$checksums_sha256,manifest_sha256:$manifest_sha256,
		             manifest_signature_sha256:$manifest_sig_sha256,provenance:$provenance,
		             signed_members:$signed_members,release_assets:$assets,
	             attached_pair_compared:($attached_pair_compared == 1)},
		  publication:{make_latest:$make_latest}}'
}

# --- the postconditions of a PUBLISHED release --------------------------------------------
# The point of publishing is that a consumer can fetch these bytes. The manifest and its
# signature are the two a client needs, and the 404 QA2334 measured is exactly the shape this
# exercises. READS ONLY: a GET of a public download URL and a GET of the latest pointer. It
# mutates nothing, uploads nothing and clobbers nothing, which is what makes it safe to run
# over a release this job did not publish.
check_published_state() {
	local pubfile url curl_rc delivered verified latest_rc latest_id
	for pubfile in stable-manifest.json stable-manifest.json.sig; do
		url="$(asset_url_of "$pubfile")"
		[ -n "$url" ] || postcondition_failed "the published release exposes no download URL for ${pubfile}"
		set +e
		"$CURL_BIN" --fail --location --silent --show-error --max-time 120 \
			--output "$WORK/delivered-${pubfile}" "$url" 2>"$WORK/curl.err"
		curl_rc=$?
		set -e
		[ "$curl_rc" -eq 0 ] ||
			postcondition_failed "public delivery of ${pubfile} failed (curl exit ${curl_rc})" \
				"$(head -2 "$WORK/curl.err" 2>/dev/null | tr '\n' ' ')" "url ${url}"
		delivered="$(sha_of "$WORK/delivered-${pubfile}")" || postcondition_failed "could not digest the delivered ${pubfile}"
		verified="$(sha_of "$DL/${pubfile}")"
		[ "$delivered" = "$verified" ] ||
			postcondition_failed "the publicly delivered ${pubfile} is not the verified bytes" \
				"verified  ${verified}" "delivered ${delivered}"
	done
	_say "public tag-scoped delivery serves the verified manifest and its signature"

	if [ "$MAKE_LATEST" = "true" ]; then
		set +e
		gh_api "repos/${REPOSITORY}/releases/latest" >"$WORK/latest.json" 2>/dev/null
		latest_rc=$?
		set -e
		[ "$latest_rc" -eq 0 ] || postcondition_failed "the latest-release pointer could not be read"
		latest_id="$("$JQ_BIN" -r '.id|tostring' "$WORK/latest.json" 2>/dev/null || printf '<unreadable>')"
		[ "$latest_id" = "$RELEASE_ID" ] ||
			postcondition_failed "latest points at release id ${latest_id}, not ${RELEASE_ID}"
		_say "the latest pointer names this release"
	else
		_say "make_latest=${MAKE_LATEST}: this profile declared it does not move the latest pointer"
	fi
}

if [ "$RECONCILE" -eq 1 ]; then
	# ⛔ A RE-INSPECTION RUNS THE POSTCONDITIONS TOO (root return 02, F6). This exited here,
	# reporting that the already-published candidate "verifies completely" without ever
	# fetching a public URL — and the state QA2334 measured is precisely a published release
	# whose required .sig answered 404 while every API-side check passed, because the assets
	# are fetched by id through the API and delivery is a different layer. The runbook and the
	# workflow both point an operator at this mode to inspect such a release, so the mode that
	# could not see it was the mode they were told to use.
	#
	# The verdict follows the observation, not the intent: a confirmed publication whose
	# postconditions do not hold is a failed published state (exit 4), never
	# PUBLICATION_ALREADY_COMPLETE. Nothing below writes: no undraft, no upload, no clobber.
	check_published_state
	EVIDENCE_JSON="$(build_evidence "PUBLICATION_ALREADY_COMPLETE")"
	printf '%s\n' "$EVIDENCE_JSON"
	_say "the already-published candidate verifies completely, and its manifest, signature"
	_say "and pointer are the ones it serves. No effect was repeated: nothing was signed,"
	_say "clobbered, re-published or deleted."
	exit 0
fi

# --- 9. re-read, then publish by the RETAINED id ------------------------------------------
# THE LAST LOOK IS A DETECTOR, NOT A LOCK. Everything above took time; the inventory is read
# again here and any observed change refuses. GitHub's update endpoint offers no
# compare-and-swap over an asset set, and this script does not pretend to one.
# THE SAME SHAPE ON BOTH SIDES. The two inventories are reduced to the same canonical
# projection before they are compared: a difference that is only key order or page
# boundaries would otherwise read as a writer, and a control that cries wolf is one that
# gets bypassed.
inventory_snapshot() { # inventory_snapshot <destination>
	local dest="$1" rc
	set +e
	"$GH_BIN" api --paginate -H "Accept: application/vnd.github+json" \
		"repos/${REPOSITORY}/releases/${RELEASE_ID}/assets?per_page=100" \
		--jq '.[] | {name,id,size,state}' >"$WORK/inv.ndjson" 2>"$WORK/inv.err"
	rc=$?
	set -e
	[ "$rc" -eq 0 ] || return "$rc"
	"$JQ_BIN" -s -S 'sort_by(.name)' "$WORK/inv.ndjson" >"$dest"
}

"$JQ_BIN" -S '[.[] | {name,id,size,state}] | sort_by(.name)' "$assets_json" >"$WORK/assets-before.json"
set +e
inventory_snapshot "$WORK/assets-after.json"
recheck_rc=$?
set -e
[ "$recheck_rc" -eq 0 ] ||
	blind "could not re-read the asset inventory before publishing (gh exit ${recheck_rc})" \
		"$(head -3 "$WORK/inv.err" 2>/dev/null | tr '\n' ' ')"
if ! "$CMP_BIN" -s "$WORK/assets-before.json" "$WORK/assets-after.json"; then
	refuse "the release's asset inventory changed while it was being verified" \
		"$(diff "$WORK/assets-before.json" "$WORK/assets-after.json" 2>/dev/null | head -8 | tr '\n' ' ')" \
		"An observed change is a writer this ceremony does not control. Nothing is published."
fi

set +e
gh_api "repos/${REPOSITORY}/releases/${RELEASE_ID}" >"$WORK/release-recheck.json" 2>"$WORK/release-recheck.err"
rr_rc=$?
set -e
[ "$rr_rc" -eq 0 ] ||
	blind "could not re-read the release before publishing (gh exit ${rr_rc})"
for probe in '.id|tostring:'"$RELEASE_ID" '.tag_name:'"$RELEASE_TAG" '.draft|tostring:true' '.prerelease|tostring:false'; do
	expr="${probe%%:*}"
	want="${probe#*:}"
	got="$("$JQ_BIN" -r "$expr" "$WORK/release-recheck.json" 2>/dev/null || printf '<unreadable>')"
	[ "$got" = "$want" ] ||
		refuse "the release changed while it was being verified: ${expr} is '${got}', expected '${want}'"
done
_say "identity and inventory unchanged since admission; publishing release id ${RELEASE_ID}"

# ⛔ ONLY draft AND make_latest, ONLY BY THE RETAINED ID. Re-resolving the tag here would
# hand the mutation to whatever the tag names NOW; the id is the thing this script verified.
# `draft` is a JSON boolean (-F), `make_latest` is the documented string enum (-f).
PATCH_ISSUED=1
set +e
gh_api --method PATCH "repos/${REPOSITORY}/releases/${RELEASE_ID}" \
	-F draft=false -f "make_latest=${MAKE_LATEST}" >"$WORK/patch.json" 2>"$WORK/patch.err"
patch_rc=$?
set -e
if [ "$patch_rc" -ne 0 ]; then
	unknown "the publish PATCH for release id ${RELEASE_ID} exited ${patch_rc}" \
		"$(head -4 "$WORK/patch.err" 2>/dev/null | tr '\n' ' ')" \
		"A transport failure does not say whether the server applied the change."
fi

# --- 10. read back, by the retained id ----------------------------------------------------
set +e
gh_api "repos/${REPOSITORY}/releases/${RELEASE_ID}" >"$WORK/published.json" 2>"$WORK/published.err"
pb_rc=$?
set -e
[ "$pb_rc" -eq 0 ] ||
	unknown "the PATCH returned success and the read-back failed (gh exit ${pb_rc})" \
		"$(head -3 "$WORK/published.err" 2>/dev/null | tr '\n' ' ')"
pubq() { "$JQ_BIN" -r "$1" "$WORK/published.json" 2>/dev/null || printf '<unreadable>'; }
[ "$(pubq '.id|tostring')" = "$RELEASE_ID" ] ||
	unknown "the read-back names release id $(pubq '.id|tostring'), not ${RELEASE_ID}"
[ "$(pubq '.tag_name')" = "$RELEASE_TAG" ] ||
	unknown "the read-back names tag $(pubq '.tag_name'), not ${RELEASE_TAG}"
[ "$(pubq '.draft|tostring')" = "false" ] ||
	unknown "the read-back still reports draft=$(pubq '.draft|tostring')"
_say "PUBLISHED: ${RELEASE_TAG} (release id ${RELEASE_ID}) is no longer a draft"

# The admitted inventory must still be the published inventory.
set +e
inventory_snapshot "$WORK/assets-published.json"
ap_rc=$?
set -e
[ "$ap_rc" -eq 0 ] || postcondition_failed "the published asset inventory could not be re-read"
"$CMP_BIN" -s "$WORK/assets-before.json" "$WORK/assets-published.json" ||
	postcondition_failed "the published inventory differs from the admitted one" \
		"$(diff "$WORK/assets-before.json" "$WORK/assets-published.json" 2>/dev/null | head -8 | tr '\n' ' ')"

# --- 11. public delivery, and the pointer -------------------------------------------------
check_published_state

EVIDENCE_JSON="$(build_evidence "PUBLISHED")"
printf '%s\n' "$EVIDENCE_JSON"
_say "OK — ${RELEASE_TAG} published by retained id ${RELEASE_ID}, read back and delivered."
_say "    This is a record of what was verified, not a new authority."
exit 0
