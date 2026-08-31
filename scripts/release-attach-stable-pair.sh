#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# release-attach-stable-pair.sh — the custody half of the phase-2 signing ceremony: read the
# draft ONCE, refuse anything that is not a mutable draft, stage what is already there, upload
# the verified stable pair with --clobber, and restore the exact previous snapshot if that
# upload fails. Ends deny-closed on any draft carrying a security manifest.
#
#   usage: bash scripts/release-attach-stable-pair.sh <release-tag>
#   reads: ota-dist/stable-manifest.json{,.sig} — already verified by the caller
#   exit:  0 = the pair is attached and the draft carries no security manifest
#          1 = refused, or uploaded-and-rolled-back, or rollback INCOMPLETE (said so)
#
# ⛔ WHY THIS IS A SCRIPT AND NOT MORE LINES OF THE WORKFLOW — AND WHAT THAT DOES NOT COST.
#
# GitHub limits every `run:` to 21,000 characters ("Runs command-line programs that do not
# exceed 21,000 characters using the operating system's shell", Workflow syntax reference,
# docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax, read 2026-08-15).
# The ceremony's block measured 21,600 characters — 600 over — so a legitimate release could
# not run this step at all, and `task lint:actions` never saw it because actionlint does not
# model the platform limit (the model, 2026-08-15, P0-B, verdict NO-LAND).
#
# THE CUT IS DELIBERATELY NOT WHERE THE GUARANTEE LIVES. What the caller's block proves is
# that no step boundary — and therefore no `uses:` — can fall between the `git status` that
# checks this workspace and the code that CONSUMES the release rule, because a `run:` scalar
# cannot contain a step. Splitting the block there would have destroyed exactly that. So the
# check, the classifier call and the evidence binding all stay in the ONE `run:` scalar, and
# only this trailing custody ceremony moved — into a versioned file that the SAME scalar
# invokes, after the same `git status`, exactly like scripts/release-ota-channel.sh already
# was. A tracked script called from inside the verified window is covered by that window; a
# second `run:` step would not have been.
#
# Everything below is the block's own code and its own reasoning, moved verbatim.
set -euo pipefail

RELEASE_TAG="${1-}"
if [ -z "${RELEASE_TAG}" ]; then
	echo "ERROR: usage: release-attach-stable-pair.sh <release-tag>" >&2
	echo "Called with no tag, this ceremony would read and clobber an unnamed release." >&2
	exit 1
fi

# ⛔ ONE READ, TAKEN BEFORE ANY MUTATION, and it answers three questions at once:
# is this a draft, is it mutable, and what is on it. Reading once is what keeps the
# three answers consistent with each other; asking again later would only add a
# second snapshot that can disagree with the first.
set +e
release_state="$(gh release view "${RELEASE_TAG}" --json assets,isDraft,isImmutable \
  --jq '["DRAFT \(.isDraft)","IMMUTABLE \(.isImmutable)"] + [.assets[].name | "ASSET \(.)"] | .[]')"
gh_rc=$?
set -e
if [ "${gh_rc}" -ne 0 ]; then
  echo "ERROR: could not read the draft asset inventory for ${RELEASE_TAG} (gh exit ${gh_rc})." >&2
  echo "A ceremony that cannot see what it attached must not certify the security channel." >&2
  echo "Re-run this step once the release API is reachable; do not publish the draft meanwhile." >&2
  exit 1
fi

# THE TARGET MUST BE A DRAFT, AND IT MUST BE MUTABLE — asserted BEFORE the upload,
# because that upload is a `--clobber` and gh's own manual says of it: "existing
# assets are deleted before new assets are uploaded. If the upload fails, the
# original assets will be lost." Against an already published release that is a
# public artefact deleted on our way to failing.
is_draft=""
is_immutable=""
have_manifest=0
have_signature=0
have_stable_manifest=0
have_stable_sig=0
cr_anomaly=0
while IFS= read -r line; do
  case "${line}" in
  "DRAFT "*) is_draft="${line#DRAFT }" ;;
  "IMMUTABLE "*) is_immutable="${line#IMMUTABLE }" ;;
  "ASSET security-manifest.json") have_manifest=1 ;;
  "ASSET security-manifest.json.sig") have_signature=1 ;;
  "ASSET stable-manifest.json") have_stable_manifest=1 ;;
  "ASSET stable-manifest.json.sig") have_stable_sig=1 ;;
  "ASSET "*)
    # A name that only matches once a trailing CR is removed is a name this
    # ceremony cannot classify. Rather than assume gh always emits LF — which is
    # not a claim this repository has a source for — say so and stop.
    _n="${line#ASSET }"
    if [ "${_n%$'\r'}" = "security-manifest.json" ] ||
      [ "${_n%$'\r'}" = "security-manifest.json.sig" ]; then
      cr_anomaly=1
    fi
    ;;
  esac
done <<<"${release_state}"

if [ "${is_draft}" != "true" ]; then
  echo "ERROR: ${RELEASE_TAG} is not a draft (isDraft=${is_draft:-<absent>})." >&2
  echo "This ceremony clobbers the stable pair, and --clobber deletes the existing assets" >&2
  echo "before uploading. Refusing to do that to a published release. Nothing was uploaded." >&2
  exit 1
fi
# ONLY `false`. `null`, absent, or anything else is not evidence that this release
# can be mutated — it is the absence of evidence, and the adjacent diagnostic already
# said so while the switch accepted `null` anyway (the model repair audit, P2-04).
case "${is_immutable}" in
false) ;;
*)
  echo "ERROR: ${RELEASE_TAG} reports isImmutable=${is_immutable:-<absent>}." >&2
  echo "An immutable release cannot accept the clobbering upload this step performs," >&2
  echo "and an unknown posture is not a yes. Nothing was uploaded." >&2
  exit 1
  ;;
esac
if [ "${cr_anomaly}" -eq 1 ]; then
  echo "ERROR: a draft asset matches a security-channel name only after a trailing CR is removed." >&2
  echo "Refusing to classify an inventory this ceremony cannot read unambiguously." >&2
  exit 1
fi

# STAGING, so a failed clobber is recoverable. gh deletes before it uploads, so the
# previous pair only exists in this job's copy of it from here on.
# EXACTLY THE SUBSET THAT EXISTS. Staging only when BOTH members were present left a
# partial pair — one surviving asset — going into a `--clobber` with no custody at
# all, and gh deletes same-name originals before uploading (the model repair audit,
# P1-03). One original is not "no original".
staged=0
stage_patterns=()
[ "${have_stable_manifest}" -eq 1 ] && stage_patterns+=(--pattern stable-manifest.json)
[ "${have_stable_sig}" -eq 1 ] && stage_patterns+=(--pattern stable-manifest.json.sig)
if [ "${#stage_patterns[@]}" -gt 0 ]; then
  mkdir -p ota-staging
  if gh release download "${RELEASE_TAG}" --dir ota-staging --clobber \
    "${stage_patterns[@]}"; then
    staged=1
  else
    echo "ERROR: could not stage the existing stable asset(s) before clobbering." >&2
    echo "Refusing to delete assets this ceremony would not be able to put back." >&2
    exit 1
  fi
fi

set +e
gh release upload "${RELEASE_TAG}" \
  ota-dist/stable-manifest.json ota-dist/stable-manifest.json.sig --clobber
upload_rc=$?
set -e
if [ "${upload_rc}" -ne 0 ]; then
  echo "ERROR: the stable pair upload failed (gh exit ${upload_rc})." >&2
  # ⛔ RESTORE THE SNAPSHOT, not just the names that were there before. gh uploads
  # the assets CONCURRENTLY — one goroutine per asset — so a failed call can leave
  # one member uploaded and the other not. Re-uploading the staged originals does
  # not undo that: an asset that did NOT exist before is now on the draft, and with
  # no previous assets at all the old code staged nothing and rolled back nothing,
  # leaving a brand-new asset from a failed attempt (the model, exact audit of
  # 533563e48, second P1).
  #
  # The custody contract is therefore: whatever this attempt CREATED is deleted, and
  # whatever existed BEFORE is put back. Anything it cannot verify is declared
  # UNKNOWN and left for a human — never described as a rollback that happened.
  post_state="$(gh release view "${RELEASE_TAG}" --json assets --jq '.assets[].name')" ||
    post_state="__UNREADABLE__"
  if [ "${post_state}" = "__UNREADABLE__" ]; then
    echo "⛔ UNKNOWN: the draft could not be read after the failure." >&2
    echo "No rollback was attempted and the draft's contents are not known. Inspect" >&2
    echo "and repair it by hand; do not publish it." >&2
    exit 1
  fi
  rollback_ok=1
  for asset in stable-manifest.json stable-manifest.json.sig; do
    existed=0
    case "${asset}" in
    stable-manifest.json) [ "${have_stable_manifest}" -eq 1 ] && existed=1 ;;
    stable-manifest.json.sig) [ "${have_stable_sig}" -eq 1 ] && existed=1 ;;
    esac
    present_now=0
    while IFS= read -r a || [ -n "${a}" ]; do
      [ "${a%$'\r'}" = "${asset}" ] && present_now=1
    done <<<"${post_state}"
    if [ "${existed}" -eq 0 ] && [ "${present_now}" -eq 1 ]; then
      echo "Removing ${asset}, which this attempt created." >&2
      gh release delete-asset "${RELEASE_TAG}" "${asset}" --yes || {
        echo "⛔ could not remove ${asset}; the draft carries an asset from a failed attempt." >&2
        rollback_ok=0
      }
    fi
  done
  if [ "${staged}" -eq 1 ]; then
    echo "Restoring the asset(s) that were there before." >&2
    restore_files=()
    [ "${have_stable_manifest}" -eq 1 ] && restore_files+=(ota-staging/stable-manifest.json)
    [ "${have_stable_sig}" -eq 1 ] && restore_files+=(ota-staging/stable-manifest.json.sig)
    gh release upload "${RELEASE_TAG}" "${restore_files[@]}" --clobber || {
      echo "⛔ could not restore the previous asset(s)." >&2
      rollback_ok=0
    }
  fi
  if [ "${rollback_ok}" -eq 1 ]; then
    echo "Rollback done: the draft carries exactly what it carried before this step." >&2
  else
    echo "⛔ ROLLBACK INCOMPLETE. The draft does NOT match its previous state." >&2
    echo "Do not publish it; repair it by hand." >&2
  fi
  exit 1
fi
echo "Verified OTA pair attached to the draft. Publishing the draft remains a human action."
# THE DECISION BELOW IS CORROBORATION, NOT AUTHORIZATION, and it runs on the flags
# parsed from the SINGLE read taken above — before any mutation, alongside isDraft
# and isImmutable. The tag's own advisories declaration is what authorized this
# ceremony; what follows can only ADD red, catching a security manifest on the draft
# that the declaration did not predict (a hand upload, a stale asset from an earlier
# attempt). A snapshot may miss a concurrent write, so it never says yes.
if [ "${have_manifest}" -eq 0 ]; then
  echo "no security manifest on the draft for ${RELEASE_TAG}: nothing further for this ceremony to certify."
else
  # ⛔ DENY-CLOSED: ANY draft carrying security-manifest.json fails this ceremony,
  # whether or not an asset called security-manifest.json.sig sits beside it.
  #
  # An earlier revision let the pair finish GREEN and emitted a ::warning:: saying
  # the signature was accepted by name. That warning was honest as a human
  # declaration and WORTHLESS as enforcement — Actions still records the job as
  # success — so the release could complete over bytes nothing had verified. The
  # counterexample is not hypothetical: phase 1b re-runs `gh release upload
  # --clobber` on the JSON ONLY, so a signature over a PREVIOUS manifest keeps the
  # right filename beside the new one and satisfies any name check. A zero-byte
  # .sig does too (the model contrast, 2026-08-09, P1-01, verdict NO-LAND).
  #
  # A NOMINAL SIGNATURE DOES NOT AUTHORIZE. What would authorize is verifying the
  # signature over these manifest bytes under the OTA key — which is #644, and is
  # deliberately NOT reimplemented here. Until #644 exists, the honest answer for a
  # security release is "this ceremony cannot finish it", not a green tick.
  #
  # The presence of the .sig still changes the DIAGNOSIS, because the operator's
  # next action differs, and those two messages are what the battery pins now that
  # both paths share one exit code.
  echo "ERROR: the draft carries security-manifest.json, and this ceremony cannot certify it." >&2
  if [ "${have_signature}" -eq 1 ]; then
    echo "An asset named security-manifest.json.sig is present, but only BY NAME: this ceremony" >&2
    echo "signs stable only, so nothing here checked those bytes against the OTA key. A signature" >&2
    echo "over a PREVIOUS manifest keeps the right filename, and so does a zero-byte file." >&2
  else
    echo "There is no security-manifest.json.sig on the draft at all." >&2
  fi
  echo "Cryptographic verification of the security channel is #644. Until it lands, a release that" >&2
  echo "declared advisories cannot be completed here: do not publish this draft." >&2
  exit 1
fi
