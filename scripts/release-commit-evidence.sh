#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# release-commit-evidence.sh — record the commit THIS release run built, in the file
# GoReleaser then checksums and publishes.
#
#   usage:  scripts/release-commit-evidence.sh <is-snapshot>
#           <is-snapshot> is GoReleaser's own `{{ .IsSnapshot }}`, rendered by the hook that
#           calls this script — never a value this script guesses.
#   writes: release-commit.txt in the working directory, which for a `before` hook is
#           GoReleaser's own cwd, i.e. the repository root (v2.17.0
#           internal/pipe/before/before.go:34 calls shell.Run with dir "", and
#           internal/shell/shell.go:37 only sets cmd.Dir when dir is non-empty).
#   exit:   0 = written, or deliberately nothing written for a snapshot
#           1 = REFUSE: a real release whose commit cannot be recorded honestly
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
# NO `set -e` gaps: this refuses loudly or writes exactly one line.
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

printf '%s\n' "${sha}" >release-commit.txt
echo "release-commit-evidence: recorded ${sha} in release-commit.txt"
