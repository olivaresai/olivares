#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# release-commit-evidence.sh — record WHAT THIS RELEASE RUN WAS, in the two files
# GoReleaser then checksums and publishes.
#
#   usage:  scripts/release-commit-evidence.sh <is-snapshot>
#           <is-snapshot> is GoReleaser's own `{{ .IsSnapshot }}`, rendered by the hook that
#           calls this script — never a value this script guesses.
#   writes: release-commit.txt AND release-build-context.json in the working directory,
#           which for a `before` hook is GoReleaser's own cwd, i.e. the repository root
#           (v2.17.0 internal/pipe/before/before.go:34 calls shell.Run with dir "", and
#           internal/shell/shell.go:37 only sets cmd.Dir when dir is non-empty).
#   exit:   0 = written, or deliberately nothing written for a snapshot
#           1 = REFUSE: a real release whose identity cannot be recorded honestly
#
# ⛔ WHY THERE IS A SECOND FILE, AND WHY IT IS NOT MORE FIELDS IN THE FIRST.
#
# release-commit.txt answers "which SOURCE did this build come from". It cannot answer
# "was this artifact set produced by a completed, successful phase-1 run of THIS workflow
# in THIS repository", and phase 2 needs that second answer before it may publish: an
# authenticated source and an authenticated archive set still say nothing about WHICH run
# produced them, so a downloaded set from an unrelated successful run would satisfy every
# existing check. Widening release-commit.txt would break its own contract — phase 2 pins
# it BYTE-EXACTLY as "one OID and a newline", and three separate guards recompute that
# digest — so the run/attempt evidence goes in its own document, with its own digest inside
# the same signed checksums.txt.
#
# THE BYTES ARE CANONICAL ON PURPOSE. The phase-1 post-build guard recomputes this exact
# byte sequence from the Actions context and compares digests, exactly as it does for
# release-commit.txt: allow-listing the NAME would let the build rewrite the evidence while
# `git status --porcelain` never changes. So: one line, fixed field order, no whitespace,
# one trailing LF, values that are already constrained above. Nothing here is formatted by
# a tool that could reorder keys.
#
# WHAT IT IS NOT. It is DATA, and it grants no publication authority whatsoever: the
# protected environment and its reviewers remain the only authorization for phase 2. A
# release that cannot produce this evidence refuses; a release that produces it has said
# which run it was, and nothing more.
#
# ⛔ IT RECORDS THE EVENT; IT DOES NOT DECIDE WHICH EVENTS MAY PUBLISH (root return 02, F2).
# An earlier revision refused outright unless GITHUB_EVENT_NAME was `push`. This repository
# invokes GoReleaser from the tag-push job AND from the release rehearsal, which is
# workflow_dispatch by design and is the only end-to-end exercise of the release mechanics
# the project has — so that refusal killed the rehearsal in the first `before` hook, before
# the first build, and bought nothing: the consumer already refuses a non-push context twice,
# once on this recorded field (scripts/release-finalize-stable.sh) and once against the
# Actions API. Recording the true event and refusing it at the consumer keeps the whole
# defence and costs the rehearsal nothing. A rehearsal therefore produces evidence that says
# `workflow_dispatch`, which is exactly what it was, and no rehearsal artifact set can claim
# a push context it did not have.
#
# ⛔ NOTHING IS WRITTEN UNTIL EVERY FIELD HAS BEEN CHECKED, and a failed write removes both
# generated names. A validation refusal writes nothing at all; a filesystem failure is cleaned
# up on a best-effort basis and reports whether the cleanup itself succeeded. Neither is an
# atomic write, and this file does not claim one.
#
# ⛔ WHY THIS IS A GORELEASER `before` HOOK AND NOT A WORKFLOW STEP.
#
# It WAS a workflow step ("record the phase-1 commit as checksum-covered evidence"), sitting
# immediately before the goreleaser action, and that made EVERY legitimate release impossible.
# release-commit.txt is neither tracked nor ignored, so writing it left `?? release-commit.txt`
# in the tree, and GoReleaser refuses to release from a dirty tree — its own error page uses
# exactly this shape (`?? created.txt`) as the example:
# https://goreleaser.com/resources/errors/dirty/. Phase 1 therefore died at the build, before
# producing anything at all. It is not a security hole; it is a publication stop
# (the model, 2026-08-15, P0-A, verdict NO-LAND).
#
# A `before` hook is the earliest point that is AFTER that validation, and that is a MEASURED
# property of the pinned engine rather than a hope. In v2.17.0 the pipeline is ordered
#     dist.CleanPipe → env → git → semver → defaults → partial → snapshot → before → dist → …
# (internal/pipeline/pipeline.go:63-104), the dirty check lives in the git pipe
# (internal/pipe/git/git.go:194-224: `validate` → `CheckDirty`), and `CheckDirty` has NO other
# caller on this path — the only two others are the `--auto-snapshot` decisions in
# cmd/build.go:164 and cmd/release.go:148, which run before the pipeline and only with that
# flag, which this repository never passes. So nothing re-reads the tree state after the hook.
#
# ⛔ AND THE FILE MUST STAY VISIBLE TO `git status`. The workflow guards allow-list it BY NAME
# (`?? release-commit.txt`) and then pin its BYTES against GITHUB_SHA. Adding it to .gitignore
# would "fix" the dirty tree by making it invisible to `git status --porcelain` — the control
# would stop seeing the very path it allows, and every other file dropped beside it under the
# same rule would go unseen too. The allow-list is declared next to the check, not in a
# repo-wide ignore rule.
#
# WHERE IT ENDS UP: .goreleaser.yaml declares it in BOTH `checksum.extra_files` (its digest
# goes inside the checksums.txt that the workflow cosign-signs) AND `release.extra_files` (the
# file itself is uploaded to the draft). Neither implies the other in v2.17.0 — the checksum
# pipe appends extra files to a LOCAL artifact list and never registers them for upload
# (internal/pipe/checksums/checksums.go:174-198), while the release pipe registers them as
# artifact.UploadableFile without checksumming them (internal/pipe/release/release.go:160-174).
# Phase 2 needs both halves: it DOWNLOADS the asset and verifies its digest against the signed
# checksums.
#
# NO `set -e` gaps: this refuses loudly, or writes both evidence files and nothing else.
set -euo pipefail
export LC_ALL=C

snapshot="${1-}"
case "${snapshot}" in
true) ;;
false) ;;
"")
	echo "ERROR: usage: release-commit-evidence.sh <true|false>   (GoReleaser's {{ .IsSnapshot }})" >&2
	echo "Called with no verdict at all, this script cannot tell a rehearsal from a release." >&2
	exit 1
	;;
*)
	echo "ERROR: unknown snapshot verdict '${snapshot}'; expected exactly true or false." >&2
	exit 1
	;;
esac

# A SNAPSHOT HAS NO RUN COMMIT TO RECORD, and `task release:snapshot` is a local dry run:
# writing here would drop an untracked file into a developer's tree for a build that never
# publishes anything. GoReleaser skips its own dirty validation for snapshots too
# (internal/pipe/git/git.go:195-197), so nothing downstream depends on this file existing.
if [ "${snapshot}" = "true" ]; then
	echo "release-commit-evidence: snapshot build — no run commit to record, writing nothing"
	exit 0
fi

# FOR A REAL RELEASE THE COMMIT IS NOT OPTIONAL. Phase 2 binds itself to these bytes, so a
# release that cannot say which commit it built must not build one. Refusing here costs a red
# release; writing a placeholder would cost a signed artefact nobody can trace back.
sha="${GITHUB_SHA:-}"
if [ -z "${sha}" ]; then
	echo "ERROR: GITHUB_SHA is empty, and this is not a snapshot." >&2
	echo "The release evidence phase 2 binds to would be a guess. Refusing to build one." >&2
	exit 1
fi
case "${sha}" in
*[!0-9a-f]*)
	echo "ERROR: GITHUB_SHA is not lowercase hex: ${sha}" >&2
	exit 1
	;;
esac
if [ "${#sha}" -ne 40 ]; then
	echo "ERROR: GITHUB_SHA is not a full 40-hex OID (length ${#sha})." >&2
	exit 1
fi

# --- the build context phase 2 binds the artifact set to -----------------------------------
# EVERY FIELD IS CHECKED BEFORE ANYTHING IS RECORDED. An evidence file that faithfully records
# a malformed context is not evidence: the consumer would have to decide what an empty run id
# means, and "the field was blank" is exactly the ambiguity phase 2 must never resolve by
# guessing. Refusing here costs a red release; recording a blank costs a published artifact
# set nobody can tie to a run.
ctx_fail() {
	echo "ERROR: $1" >&2
	echo "Phase 2 binds the published artifact set to this context, so a context that cannot" >&2
	echo "be recorded exactly is a refusal, not a field left empty." >&2
	echo "Nothing was written." >&2
	exit 1
}

repo_id="${GITHUB_REPOSITORY_ID:-}"
repo="${GITHUB_REPOSITORY:-}"
event="${GITHUB_EVENT_NAME:-}"
ref="${GITHUB_REF:-}"
run_id="${GITHUB_RUN_ID:-}"
run_attempt="${GITHUB_RUN_ATTEMPT:-}"

# DECIMAL, CANONICAL, NO LEADING ZERO. GitHub's REST API answers these identifiers as JSON
# numbers, and phase 2 compares the two as STRINGS: `0123` and `123` are the same number and
# two different strings, so a comparison that passed here would fail there — or, worse, a
# consumer would normalise one side and not the other. The canonical form is fixed at the
# only place that writes it.
for pair in "repository_id=${repo_id}" "run_id=${run_id}" "run_attempt=${run_attempt}"; do
	name="${pair%%=*}"
	value="${pair#*=}"
	[ -n "${value}" ] || ctx_fail "${name} is empty; this run cannot say which run it is."
	case "${value}" in
	*[!0-9]*) ctx_fail "${name} is not a decimal identifier: ${value}" ;;
	0*) ctx_fail "${name} has a leading zero and is not canonical decimal: ${value}" ;;
	esac
	[ "${#value}" -le 20 ] || ctx_fail "${name} is longer than any GitHub identifier: ${value}"
done

# THE TRUE EVENT, VALIDATED BY SHAPE AND NOT BY POLICY. Which events may be PUBLISHED is the
# finalizer's decision and it makes it twice, on this field and against the Actions API; this
# file's job is to say what actually happened. GitHub event names are lowercase ASCII words
# with underscores, so anything else is refused rather than escaped — the value travels inside
# a JSON string written by printf, and a quoting bug in a control is worse than a rejected
# event name.
[ -n "${event}" ] || ctx_fail "GITHUB_EVENT_NAME is empty; this run cannot say what triggered it."
case "${event}" in
*[!a-z_]*) ctx_fail "GITHUB_EVENT_NAME is not a lowercase event name: ${event}" ;;
esac
[ "${#event}" -le 64 ] || ctx_fail "GITHUB_EVENT_NAME is longer than any GitHub event name: ${event}"

# THE REF IS RECORDED WHOLE AND VALIDATED BY SHAPE, NOT BY RELEASE POLICY. The strict
# vMAJOR.MINOR.PATCH rule belongs to the preflight and to the finalizer, which know which
# profile they are in; enforcing it here would refuse the rehearsal's own tags for a
# property this file does not own.
case "${ref}" in
refs/tags/*) ;;
*) ctx_fail "GITHUB_REF is '${ref:-<empty>}', not a tag ref." ;;
esac
tag_name="${ref#refs/tags/}"
[ -n "${tag_name}" ] || ctx_fail "GITHUB_REF names an empty tag."
# The value travels inside a JSON string written by printf, so any byte that would need
# escaping is refused rather than escaped: a quoting bug in a control is worse than a
# rejected tag, and no legitimate release tag contains these.
case "${tag_name}" in
*[\"\\]* | *' '* | *'	'*) ctx_fail "the tag name contains a character this evidence will not encode: ${tag_name}" ;;
esac

case "${repo}" in
"") ctx_fail "GITHUB_REPOSITORY is empty." ;;
*/*/*) ctx_fail "GITHUB_REPOSITORY is not OWNER/NAME: ${repo}" ;;
*/*) ;;
*) ctx_fail "GITHUB_REPOSITORY is not OWNER/NAME: ${repo}" ;;
esac
case "${repo}" in
*[!A-Za-z0-9._/-]*) ctx_fail "GITHUB_REPOSITORY contains an unexpected character: ${repo}" ;;
esac

# ONE LINE, FIXED ORDER, ONE LF. The guard downstream rebuilds this string and compares
# sha256; anything that reorders or reformats it — jq -S included — breaks that comparison
# for no gain.
ctx_json="$(printf '{"schema":"olivares.ai/release-build-context/v1","schema_version":1,"repository_id":"%s","repository":"%s","event":"%s","ref":"%s","commit":"%s","run_id":"%s","run_attempt":"%s"}' \
	"${repo_id}" "${repo}" "${event}" "${ref}" "${sha}" "${run_id}" "${run_attempt}")"

# A SMALL FIXED CAP, ASSERTED BEFORE IT IS WRITTEN. Phase 2 refuses to parse more than 4096
# bytes, and a producer that can exceed the consumer's bound is a release that dies in the
# ceremony instead of in the build. Measured on the string rather than on the file: LC_ALL=C
# is exported above, so ${#var} counts bytes, and the +1 is the single trailing LF below.
ctx_bytes=$((${#ctx_json} + 1))
[ "${ctx_bytes}" -le 4096 ] ||
	ctx_fail "the build context would be ${ctx_bytes} bytes, over the 4096-byte admission cap."

# --- and only now, the two files ------------------------------------------------------------
# Every field above has been checked, so the only way to fail here is the filesystem. A tree
# carrying one half of the evidence is the state phase 2 can neither admit nor diagnose, so a
# failed write removes BOTH generated names — a redirection TRUNCATES before it writes, so the
# file that failed is a real, partial file and removing only the other one leaves exactly that
# half state.
#
# THIS IS CLEANUP, NOT ATOMICITY, and the difference is stated because claiming the stronger
# property would be a claim the implementation cannot keep: the removal can itself fail, and
# then it says so. Only these two generated names are touched.
write_fail() { # write_fail <message>
	if ! rm -f release-commit.txt release-build-context.json; then
		echo "ERROR: $1" >&2
		echo "AND the partial evidence could not be removed. Inspect the tree by hand: phase 2" >&2
		echo "binds to these bytes and a half-written pair is neither admissible nor diagnosable." >&2
		exit 1
	fi
	echo "ERROR: $1" >&2
	echo "Both generated evidence files were removed; the tree carries neither." >&2
	exit 1
}
printf '%s\n' "${sha}" >release-commit.txt ||
	write_fail "could not write release-commit.txt"
printf '%s\n' "${ctx_json}" >release-build-context.json ||
	write_fail "could not write release-build-context.json"
echo "release-commit-evidence: recorded ${sha} in release-commit.txt"
echo "release-commit-evidence: recorded run ${run_id} attempt ${run_attempt} on event ${event} in release-build-context.json"
