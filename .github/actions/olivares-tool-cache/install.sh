#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Install a pinned prebuilt CI tool (go-task or gitleaks) from its GitHub release, verified against
# the SHA-256 the workflow pins, and expose it on PATH for the following steps.
#
# Why this exists (measured, run 34854565361, 2026-09-14): `go install …@v3.51.1` compiled go-task
# from source in every one of 15 jobs and took 4m00s–6m20s each (ci-runner-1..9); gitleaks took
# another 45–56 s. A pinned release tarball is ~5 MB and takes seconds, and these self-hosted
# runners keep RUNNER_TOOL_CACHE between jobs, so the second job on a runner does not download.
#
# TRUST MODEL (native review of c9006234, 2026-09-14 16:05Z, three defects closed here):
#   * What the cache keeps is the VERIFIED ARCHIVE, keyed by tool/version/<pinned sha256>. Nothing
#     executable is ever trusted from the cache: on every run the archive is re-hashed against the
#     sha256 the workflow pins, and the binary is extracted fresh from it into a JOB-PRIVATE
#     directory under RUNNER_TEMP. A planted executable, a self-asserted digest sidecar or a
#     changed pin cannot cause a stale or foreign binary to run: a changed pin is a different key.
#   * Publication is serialized with flock and done by rename only. A complete valid archive is
#     never deleted; a replacement (after a failed verification) is renamed over it atomically, so a
#     concurrent consumer that already opened the old inode keeps reading it. No
#     delete-before-publish window.
#   * Fail-closed: a checksum mismatch, a failed download or a binary that does not execute is a
#     red with a reason, never a fallback to `go install`. The diagnostic is also written to
#     $RUNNER_TEMP/ci-fail-<tool>-install.log so the failure reporter can name it.
#
# Contract:
#   install.sh <tool> <version> <archive-sha256>
#     tool     task | gitleaks
#     version  release version without the leading v (e.g. 3.51.1)
#     sha256   SHA-256 of the linux amd64 release archive, as published in the release checksums
# Environment: RUNNER_TOOL_CACHE (preferred cache root; falls back to RUNNER_TEMP/tool-cache),
#              RUNNER_TEMP (job scratch; the binary lives there), GITHUB_PATH (PATH export).
set -euo pipefail

tool="${1:?tool (task|gitleaks)}"
version="${2:?version}"
expected_sha="${3:?archive sha256}"

case "$expected_sha" in
  *[!0-9a-f]*|"") echo "::error::olivares-tool-cache: the pinned sha256 must be 64 lowercase hex characters"; exit 1 ;;
esac
if [[ ${#expected_sha} -ne 64 ]]; then echo "::error::olivares-tool-cache: the pinned sha256 must be 64 hex characters (got ${#expected_sha})"; exit 1; fi

case "$(uname -m)" in
  x86_64) ;;
  *) echo "::error::olivares-tool-cache supports linux x86_64 runners only; this runner is $(uname -m)"; exit 1 ;;
esac

case "$tool" in
  task)
    archive="task_linux_amd64.tar.gz"
    url="https://github.com/go-task/task/releases/download/v${version}/${archive}"
    binary="task"; version_flag="--version"
    ;;
  gitleaks)
    archive="gitleaks_${version}_linux_x64.tar.gz"
    url="https://github.com/gitleaks/gitleaks/releases/download/v${version}/${archive}"
    binary="gitleaks"; version_flag="version"
    ;;
  *) echo "::error::olivares-tool-cache: unknown tool '${tool}' (task|gitleaks)"; exit 1 ;;
esac

scratch="${RUNNER_TEMP:-/tmp}"
cache_root="${RUNNER_TOOL_CACHE:-${scratch}/tool-cache}/olivares"
archive_dir="${cache_root}/${tool}/${version}/${expected_sha}"
cached="${archive_dir}/${archive}"
job_dir="${scratch}/olivares-tool-cache/${tool}/${version}"
fail_log="${scratch}/ci-fail-${tool}-install.log"
mkdir -p "$scratch" "$cache_root"
: > "$fail_log"
log() { printf '%s\n' "$*" | tee -a "$fail_log"; }

# The pinned sha256 is the only trusted identity. It is checked on EVERY run, for a cached archive
# and for a fresh download alike; nothing else (mtime, sidecar, file name) vouches for the bytes.
verified() { [[ -f "$1" ]] && [[ "$(sha256sum "$1" | cut -c1-64)" == "$expected_sha" ]]; }

if verified "$cached"; then
  log "olivares-tool-cache: ${tool} ${version} archive reused from ${cached} (sha256 verified against the pin)"
else
  if [[ -f "$cached" ]]; then
    log "olivares-tool-cache: cached ${archive} does not match the pinned sha256; replacing it"
  fi
  # Staging lives under the cache root so the final publish is a same-filesystem rename.
  staging="$(mktemp -d "${cache_root}/.staging.XXXXXX")"
  trap 'rm -rf "$staging"' EXIT
  log "olivares-tool-cache: downloading ${url}"
  if ! curl --fail --location --silent --show-error --retry 3 --retry-delay 2 \
        --max-time 120 --output "${staging}/${archive}" "$url" 2>>"$fail_log"; then
    echo "::error::olivares-tool-cache: download of ${tool} ${version} failed (see ${fail_log})"
    exit 1
  fi
  actual_sha="$(sha256sum "${staging}/${archive}" | cut -c1-64)"
  if [[ "$actual_sha" != "$expected_sha" ]]; then
    log "olivares-tool-cache: ${archive} sha256 ${actual_sha} does not match the pinned ${expected_sha}"
    echo "::error::olivares-tool-cache: checksum mismatch for ${tool} ${version}; refusing to install"
    exit 1
  fi
  # Serialized, rename-only publication. Two installers may verify the same bytes concurrently;
  # whichever publishes second finds a valid archive and leaves it alone. A mismatching archive
  # is replaced by rename, never deleted first.
  mkdir -p "$archive_dir"
  exec 9>"${cache_root}/.publish.lock"
  flock 9
  if verified "$cached"; then
    log "olivares-tool-cache: ${archive} published concurrently by another installer; using it"
  else
    mv -f "${staging}/${archive}" "$cached"
    log "olivares-tool-cache: ${archive} published to ${cached} (sha256 ${actual_sha})"
  fi
  flock -u 9
  exec 9>&-
  rm -rf "$staging"; trap - EXIT
fi

# Extract the binary for THIS job from the archive verified above. The job directory is private
# to this job (RUNNER_TEMP), so no other installer can remove it, and nothing cached is executed.
rm -rf "$job_dir"
mkdir -p "$job_dir"
tar -xzf "$cached" -C "$job_dir" "$binary"
chmod 0755 "${job_dir}/${binary}"

# Reaching is not installing (measured 2026-08-18 on ci-runner-2: a green install step and no
# `task` three steps later). Export the job directory for the following steps AND prove the
# binary runs right now, from the exported location.
echo "$job_dir" >> "${GITHUB_PATH:?GITHUB_PATH is not set}"
if ! reported="$("${job_dir}/${binary}" "$version_flag" 2>&1)"; then
  echo "::error::olivares-tool-cache: ${job_dir}/${binary} is present but does not execute (noexec mount or wrong binary?)"
  exit 1
fi
# The binary must be the requested tool at the requested version, not merely something that exits 0.
case "$reported" in
  *"$version"*) ;;
  *) echo "::error::olivares-tool-cache: ${binary} reported '${reported}' but version ${version} was requested"; exit 1 ;;
esac
rm -f "$fail_log"
echo "olivares-tool-cache: ${tool} ${version} reachable at ${job_dir}/${binary} (archive ${cached})"
