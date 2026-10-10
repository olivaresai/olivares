#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# changed-go-packages.sh BASE [HEAD] — ./dir of every workspace Go package that BASE..HEAD
# changes (HEAD defaults to HEAD), one per line, so the race detector runs on what changed.
# A changed file belongs to the nearest directory holding .go files (testdata/ counts for the
# package above it), so embedded files map to their package. A directory outside go.work, or
# whose Go files build tags all exclude, is named on stderr and skipped; a range git cannot read
# or a package Go cannot load exits non-zero. go.mod and go.sum changes map to no package: the
# build compiles every module.
set -euo pipefail

[ "$#" -ge 1 ] && [ "$#" -le 2 ] || { echo "usage: $0 BASE [HEAD]" >&2; exit 2; }
BASE="$1"
HEAD="${2:-HEAD}"
# The checkout it runs in, not its own: the pre-push hook runs it on a temporary checkout.
ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

files="$(git -c core.quotePath=false diff --no-renames --name-only --end-of-options "$BASE" "$HEAD" --)" ||
	{ echo "changed-go-packages: cannot diff $BASE..$HEAD" >&2; exit 2; }
dirs=()
while IFS= read -r f; do
	[ -n "$f" ] || continue
	case "$f" in
	*/testdata/*) d="${f%%/testdata/*}" ;;
	*) d="$(dirname "$f")" ;;
	esac
	while [ "$d" != . ] && ! compgen -G "$d/*.go" >/dev/null; do
		d="$(dirname "$d")"
	done
	[ "$d" = . ] || dirs+=("./$d")
done <<<"$files"
[ "${#dirs[@]}" -gt 0 ] || exit 0
mapfile -t pkgs < <(printf '%s\n' "${dirs[@]}" | sort -u)
# Check module membership separately: go list -e also reports real package errors,
# which must fail selection rather than masquerade as out-of-workspace directories.
modules="$(go list -m -f '{{.Dir}}')"
workspace_pkgs=()
for d in "${pkgs[@]}"; do
	module="$ROOT/${d#./}"
	while [ "$module" != / ] && [ ! -f "$module/go.mod" ]; do
		module="$(dirname "$module")"
	done
	if grep -Fxq -- "$module" <<<"$modules"; then
		workspace_pkgs+=("$d")
	else
		echo "changed-go-packages: not a workspace package: $d" >&2
	fi
done
[ "${#workspace_pkgs[@]}" -gt 0 ] || exit 0
# A package whose Go files all carry build tags (an e2e or contract suite) has nothing to test
# without them: it is named and skipped. Any other loading error fails selection, through a
# strict `go list` that prints Go's own diagnostic.
state='{{if not .Error}}ok{{else if and .IgnoredGoFiles (not .GoFiles) (not .CgoFiles) (not .TestGoFiles) (not .XTestGoFiles)}}tagged{{else}}broken{{end}}'
listed="$(go list -e -f "$state {{.Dir}}" "${workspace_pkgs[@]}")"
selected=()
broken=()
while read -r kind dir; do
	d="./${dir#"$ROOT"/}"
	case "$kind" in
	ok) selected+=("$d") ;;
	tagged) echo "changed-go-packages: build tags exclude every file: $d" >&2 ;;
	*) broken+=("$d") ;;
	esac
done <<<"$listed"
if [ "${#broken[@]}" -gt 0 ]; then
	go list "${broken[@]}" >/dev/null || exit 2
	echo "changed-go-packages: go list -e reported errors for: ${broken[*]}" >&2
	exit 2
fi
[ "${#selected[@]}" -eq 0 ] || printf '%s\n' "${selected[@]}"
