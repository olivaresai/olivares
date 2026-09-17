#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Bind a release archive to an ALREADY-AUTHENTICATED checksums.txt, using nothing but
# coreutils.
#
# WHY THIS EXISTS AND WHY IT USES NO OLIVARES CODE
# ------------------------------------------------
# The OTA phase-2 job proves the published release is genuine by EXTRACTING the
# shipped linux/amd64 archive and RUNNING the binary inside it (`olivares version`,
# then `olivares release verify-manifest`). That proof was circular: nothing checked
# the archive's digest before the binary came out of it. The anchors the job then
# grepped for are PUBLIC repo variables, published in docs/RELEASE-VERIFICATION.md,
# so a substituted archive could print exactly the expected fingerprints, print `OK:`
# and exit 0 — the "independent verification" would have been performed by the very
# artifact under suspicion.
#
# So the archive must be bound BEFORE it is opened, by a tool that shares no code with
# it: sha256sum + awk against the checksums.txt that `cosign verify-blob` has ALREADY
# authenticated. The caller is responsible for that cosign step; this script assumes
# checksums.txt is trusted and assumes nothing about the archive.
#
# Usage:
#   verify-archive-digest.sh <checksums.txt> <archive> [<archive>...]
#
# Exit 0 only when EVERY named archive is listed exactly once in checksums.txt and its
# bytes hash to that entry. Any other outcome is a hard failure: do not extract, do not
# execute, treat it as a substituted artifact.
set -euo pipefail

die() { echo "ERROR: $*" >&2; exit 1; }

[ "$#" -ge 2 ] || die "usage: $(basename "$0") <checksums.txt> <archive> [<archive>...]"

CHECKSUMS="$1"; shift
[ -f "$CHECKSUMS" ] || die "checksums file not found: $CHECKSUMS"

for archive in "$@"; do
  [ -f "$archive" ] || die "archive not found: $archive"
  dir="$(cd "$(dirname "$archive")" && pwd)"
  base="$(basename "$archive")"

  # Exact filename match on the second field — never a substring match. A prefix/
  # substring grep would happily accept the `_fips_` variant's line for the base
  # archive (and vice versa).
  matches="$(awk -v want="$base" '$2 == want { print }' "$CHECKSUMS")"
  [ -n "$matches" ] || die "$base is NOT listed in $CHECKSUMS — nothing authenticates these bytes (fail-closed)"

  count="$(printf '%s\n' "$matches" | wc -l | tr -d ' ')"
  [ "$count" = "1" ] || die "$base is listed $count times in $CHECKSUMS — one file, several answers; refusing to pick one"

  # sha256sum -c does the comparison itself, so no digest string is ever compared by
  # this script's own (fallible) shell logic.
  ( cd "$dir" && printf '%s\n' "$matches" | sha256sum -c --strict - >/dev/null ) \
    || die "$base does NOT match its entry in the authenticated $CHECKSUMS — treat this as a SUBSTITUTED artifact: do not extract it, do not run it, investigate who wrote to the release"

  echo "verified: $base is the archive the release pipeline signed"
done
