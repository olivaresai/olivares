#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-gofmt.sh — every tracked Go source is gofmt-clean, and somebody says so out loud.
#
# WHY THIS EXISTS, measured 2026-08-05. `gofmt -l` over the tracked Go tree returned SEVENTY-NINE
# files across core/, modules/, connectors/, sdk/, cloud/ and commercial/. Not one gate in this
# repository was reporting it, and the reason is worth writing down rather than guessing at later:
#
#   Taskfile.yml `fmt:` runs `gofmt -w clients/go` — ONE directory out of the whole workspace.
#   .golangci.yml declares gofmt under `formatters:`, and in golangci-lint v2 a formatter is
#   something the tool APPLIES, not something it FAILS on. `task lint:go` is red for an unrelated
#   reason, so even its output was not being read. ⛔ Y ESA RAZÓN NO ES «golangci-lint vs go1.26»,
#   que es lo que decía esta línea hasta hoy: medido el 2026-08-07 con golangci-lint 2.12.2
#   (compilado con go1.26.5) sobre los once módulos de `go.work`, `lint:go` está rojo por **473
#   hallazgos en nueve módulos** — misspell 217, staticcheck 100, unused 74, revive 54, errcheck 11,
#   goimports 9, ineffassign 8. **ES el código.** Culpar a la toolchain convertía una campaña
#   pendiente en ruido inevitable.
#
# So the drift had no owner, no alarm and no upper bound. It was found by accident: an unrelated
# legal-header change touched a file that gofmt already disliked, and the question "did I break
# this?" turned out to be "no, and neither did the seventy-eight others".
#
# WHAT IT CHECKS. Every path `git ls-files` reports as *.go, with NO exclusion list. That is
# deliberate. A hand-kept list of what to SKIP is a denylist wearing a whitelist's clothes: the
# same shape that let ten top-level directories sit outside the export-closure gate for
# months. If a deliberately-malformed .go fixture is ever added under testdata/, excluding it
# becomes a decision somebody makes in the open, in this file, with a reason — not a silent
# exemption. Measured today: zero .go under testdata/, zero under vendor/.
#
# THREE ANSWERS, never two:
#   0  CLEAN       every tracked Go file is gofmt-clean, and it says how many it read.
#   1  DIRTY       at least one is not; each is named, with the command that fixes it.
#   2  UNVERIFIED  the check could not look — no gofmt, no git, or an empty file set. A gate that
#                  could not measure must never report what it did not see. An empty set is
#                  UNVERIFIED and not CLEAN on purpose: "I found no Go files in a Go monorepo"
#                  is a broken invocation, not a clean tree.
#
# `--selftest` proves each of those three branches against a throwaway tree, because a gate whose
# red case has never been observed is a gate nobody has tested.

set -uo pipefail

# The ambient git environment OUTRANKS `-C`: with GIT_DIR exported — which git does
# from every LINKED worktree, i.e. from every parallel session — this script's throwaway
# repositories would be driven into the LIVE repository instead. Measured 2026-08-06;
# it left the branch of PR #526 pointing at a fixture commit. Fail closed: a missing
# sanitiser is "I could not isolate", never "isolation was not needed".
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

unverified() {
	echo "==> UNVERIFIED: $*" >&2
	echo "    Part of the Go tree was NOT examined. Absence of findings in an unexamined" >&2
	echo "    tree is not absence of drift." >&2
	exit 2
}

# check_tree <dir> -> 0 clean / 1 dirty / 2 unverified. Kept as a function so --selftest can
# point it at a fixture tree and observe all three verdicts for real.
check_tree() {
	local root="$1" files n dirty
	command -v gofmt >/dev/null 2>&1 || unverified "gofmt is not on PATH."
	command -v git >/dev/null 2>&1 || unverified "git is not on PATH."
	git -C "$root" rev-parse --is-inside-work-tree >/dev/null 2>&1 ||
		unverified "$root is not a git work tree, so the tracked file set cannot be enumerated."

	files="$(git -C "$root" ls-files '*.go')"
	[[ -n "$files" ]] || unverified "git ls-files reported no *.go under $root."
	n="$(printf '%s\n' "$files" | wc -l | tr -d ' ')"

	# gofmt -l prints the files it would change. It also prints PARSE ERRORS to stderr and exits
	# non-zero for them, which is a different answer from "unformatted" and must not be folded in.
	local err_file rc=0
	err_file="$(mktemp "${TMPDIR:-/tmp}/gofmt-check.XXXXXX")"
	dirty="$(cd "$root" && printf '%s\n' "$files" | xargs gofmt -l 2>"$err_file")" || rc=$?
	if [[ -s "$err_file" ]]; then
		echo "gofmt could not parse part of the tree — NOT a clean result:" >&2
		cat "$err_file" >&2
		rm -f "$err_file"
		unverified "gofmt reported parse errors over $n file(s)."
	fi
	rm -f "$err_file"
	((rc == 0)) || unverified "gofmt exited $rc with nothing on stderr, which it should never do."

	if [[ -n "$dirty" ]]; then
		local d
		d="$(printf '%s\n' "$dirty" | wc -l | tr -d ' ')"
		echo "==> DIRTY: $d of $n tracked Go file(s) are not gofmt-clean:"
		printf '  %s\n' $dirty
		echo ""
		echo "    Fix with:  gofmt -w \$(git ls-files '*.go')"
		echo "    This is formatting only — verify with 'git diff -w' that nothing else moved."
		return 1
	fi
	echo "gofmt: OK — $n tracked Go file(s) are gofmt-clean."
	return 0
}

selftest() {
	local work rc out
	work="$(mktemp -d "${TMPDIR:-/tmp}/gofmt-selftest.XXXXXX")"
	trap 'rm -rf "$work"' RETURN
	local pass=0 fail=0
	check() { # check <what> <expected-rc> <actual-rc>
		if [[ "$2" == "$3" ]]; then
			printf '  ok    %-58s rc=%s\n' "$1" "$3"
			pass=$((pass + 1))
		else
			printf '  FAIL  %-58s rc=%s (esperaba %s)\n' "$1" "$3" "$2"
			fail=$((fail + 1))
		fi
	}

	mkdir -p "$work/clean" && (
		cd "$work/clean" && git init -q . &&
			git config user.email t@example.invalid && git config user.name T
	)
	printf 'package p\n\nfunc F() int { return 1 }\n' >"$work/clean/a.go"
	git -C "$work/clean" add a.go
	rc=0
	out="$(check_tree "$work/clean" 2>&1)" || rc=$?
	check "a gofmt-clean tree passes and says how many it read" 0 "$rc"
	case "$out" in *"1 tracked Go file(s) are gofmt-clean"*) ;; *)
		printf '  FAIL  %-58s %s\n' "the CLEAN line names the count" "$out"; fail=$((fail + 1)) ;;
	esac

	# The red case, and it must name the file — a gate that only prints a number sends the reader
	# to look for the culprit by hand.
	cp -a "$work/clean" "$work/dirty"
	printf 'package p\n\nfunc F() int {\nreturn 1\n}\n' >"$work/dirty/a.go"
	rc=0
	out="$(check_tree "$work/dirty" 2>&1)" || rc=$?
	check "an unformatted file is DIRTY" 1 "$rc"
	case "$out" in *"a.go"*) ;; *)
		printf '  FAIL  %-58s %s\n' "the DIRTY output names the file" "$out"; fail=$((fail + 1)) ;;
	esac

	# An empty set is UNVERIFIED, not CLEAN. This is the branch that turns "the invocation was
	# wrong" into an answer instead of a green.
	mkdir -p "$work/empty" && (
		cd "$work/empty" && git init -q . &&
			git config user.email t@example.invalid && git config user.name T
	)
	rc=0
	out="$(check_tree "$work/empty" 2>&1)" || rc=$?
	check "a tree with no Go files is UNVERIFIED, never CLEAN" 2 "$rc"

	# Not a work tree at all.
	mkdir -p "$work/notgit"
	rc=0
	out="$(check_tree "$work/notgit" 2>&1)" || rc=$?
	check "a directory that is not a work tree is UNVERIFIED" 2 "$rc"

	# And the tool being absent must not read as clean. PATH is emptied for the subshell only.
	rc=0
	out="$(PATH=/nonexistent-for-this-probe check_tree "$work/clean" 2>&1)" || rc=$?
	check "gofmt missing from PATH is UNVERIFIED, never CLEAN" 2 "$rc"

	printf '\ncheck-gofmt selftest: %d passed, %d failed\n' "$pass" "$fail"
	[[ "$fail" -eq 0 ]]
}

case "${1:-}" in
--selftest) selftest ;;
"") check_tree "$ROOT" ;;
*)
	echo "usage: $0 [--selftest]" >&2
	exit 64
	;;
esac
