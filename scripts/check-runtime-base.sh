#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-runtime-base.sh — the agent runtime base is one pinned Node.js LTS image, and
# Dockerfile.release is where it is read.
#
# WHY. A Dependabot update on 2026-10-10 moved Dockerfile.release and Dockerfile's runtime stage
# from node:24-trixie-slim to node:25-trixie-slim. Node 25 is an odd release line: it never
# becomes LTS and got no security releases after 2026-06-01, so the 0.1 image shipped an
# end-of-life Node. patch-velocity parsed the pin with a hard-coded node:24 pattern, found
# nothing, and its weekly rescan stopped before the scanners ran.
#
# THE RULE. Dockerfile.release has exactly one FROM, of the form
# `FROM node:<even major>-<variant>@sha256:<64 hex>`, and the last FROM of Dockerfile (its
# runtime stage) is the same line. On success it prints `<image> <digest>` on stdout for
# patch-velocity's drift step, so the workflow has no second copy of the base.
#
# Exit 0 = CLEAN. Exit 1 = VIOLATION. Exit 2 = COULD NOT LOOK (a file is missing or unreadable).
#
# USAGE: scripts/check-runtime-base.sh [ROOT]   (default: this script's repository)
set -euo pipefail
export LC_ALL=C

PROG=check-runtime-base
root="${1:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"

for f in Dockerfile.release Dockerfile; do
	if [ ! -r "$root/$f" ]; then
		printf '%s: COULD NOT LOOK — %s is missing or unreadable; this is not a clean verdict\n' "$PROG" "$root/$f" >&2
		exit 2
	fi
done

violation() {
	printf '%s: VIOLATION — %s\n' "$PROG" "$1" >&2
	exit 1
}

# Instructions are case-insensitive and may be indented; such a FROM is found here and then
# refused by the canonical pattern below, never skipped.
release_from="$(grep -iE '^[[:space:]]*FROM[[:space:]]' "$root/Dockerfile.release" || true)"
[ "$(printf '%s' "$release_from" | grep -ciE '^[[:space:]]*FROM')" = 1 ] ||
	violation "Dockerfile.release must have exactly one FROM line, found: ${release_from:-none}"
runtime_from="$({ grep -iE '^[[:space:]]*FROM[[:space:]]' "$root/Dockerfile" || true; } | tail -n 1)"
[ "$runtime_from" = "$release_from" ] ||
	violation "Dockerfile's runtime stage ($runtime_from) is not Dockerfile.release's base ($release_from); keep one pinned runtime"

re='^FROM (node:([1-9][0-9]*)-[a-z0-9.-]+)@(sha256:[0-9a-f]{64})$'
[[ "$release_from" =~ $re ]] ||
	violation "Dockerfile.release's base ($release_from) is not node:<major>-<variant>@sha256:<digest>; pin a numbered Node tag by digest"
image="${BASH_REMATCH[1]}" major="${BASH_REMATCH[2]}" digest="${BASH_REMATCH[3]}"
[ $((major % 2)) = 0 ] ||
	violation "Dockerfile.release's base is Node $major, an odd release line that never becomes LTS and reaches end of life within months; use the current even (LTS) line"

printf '%s %s\n' "$image" "$digest"
