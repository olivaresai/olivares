#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Which published console captures does the site actually use? One definition, sourced by
# docs-captures.sh (what it publishes) and check-screenshot-coverage.sh (what it allows to stay).
#
# referenced_captures <repo root>: file names (sorted, unique) that a docs page under docs-site/src,
# a README at the repository root or launch material under docs/launch names as
# console/<name>.png. Launch posts count because check-launch-image-refs.sh fails on an embedded
# image that is gone. Pinned snapshots under console/<YYYY-MM>/ do not match: the name must follow
# console/ directly. Returns 2, with a message, when it cannot read the tree: an empty set means "no
# page names any capture", never "could not look".
referenced_captures() {
	local root="$1" paths f out rc
	paths=("$root/docs-site/src")
	[ -d "${paths[0]}" ] || {
		echo "capture-refs: ⛔ COULD NOT CHECK: missing ${paths[0]}" >&2
		return 2
	}
	for f in "$root"/README*.md "$root/docs/launch"; do
		[ -e "$f" ] && paths+=("$f")
	done
	# grep exits 1 for "no match" (an empty set) and 2 for a fault: only the first is an answer.
	out="$(grep -rIhoE 'console/[A-Za-z0-9._-]+\.png' "${paths[@]}")"
	rc=$?
	[ "$rc" -le 1 ] || {
		echo "capture-refs: ⛔ COULD NOT CHECK: grep failed (rc $rc) under $root" >&2
		return 2
	}
	[ -z "$out" ] || sed 's#^console/##' <<<"$out" | sort -u
	return 0
}
