#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-unreachable-build-guards.sh — a guard that CANNOT PASS is not defence in depth; it is an
# outage with a reassuring comment. This refuses the one shape we have already paid for twice.
#
# THE SHAPE, and both times it shipped. A Dockerfile stage links the binary with `-s -w` (strip
# the symbol table and DWARF) and then, in the same RUN, asserts on symbol names via
# `go tool nm | grep -q …`. The grep can never match, the `||` branch fires unconditionally, and
# EVERY build of that file dies at that step — while the image labels and the public docs rest a
# FIPS/STIG claim on the artifact that was never produced.
#
#   Dockerfile.fips  — had it, was repaired (it now RUNS the binary and asserts on its output)
#   Dockerfile.stig  — had the IDENTICAL line and was left behind
#
# That gap is the whole reason this file exists. Repairing the case did not close the class: the
# second copy sat there for as long as the first, and the external contrast on gate S-5 found it
# months later. Measured on this tree 2026-08-08, with both controls, no container needed:
#
#   build WITHOUT -s -w  ->  go tool nm prints 72220 symbol lines
#   build WITH    -s -w  ->  go tool nm prints 0 and says "no symbol section"
#
# WHY THE CHECK IS DERIVED AND NOT A LIST OF FILENAMES. A hand-written list of Dockerfiles is
# exactly how the STIG copy survived: somebody fixed the file they were looking at. This walks
# every tracked Dockerfile, finds each RUN block, and asks one question per block — does this
# block strip AND then read symbols? A new Dockerfile with the same mistake is caught the day it
# lands, with nobody having to remember to add it anywhere.
#
# WHAT IT DELIBERATELY DOES NOT DO. It does not judge whether a guard is a GOOD guard, and it does
# not run docker. It answers exactly one question — can this assertion ever be true against the
# binary this same block just produced — and says so. Pretending to more would be the "gate that
# reads as coverage" this repository keeps finding.
#
# Exit 0 = CLEAN. Exit 1 = at least one unreachable guard, each named with file and line.
# Exit 2 = COULD NOT LOOK (no git, not a repository, unreadable file). NEVER a clean verdict.
set -u
set -o pipefail
export LC_ALL=C

say() { printf '%s\n' "$*"; }
cannot_look() {
	say "check-unreachable-build-guards: COULD NOT LOOK — $1" >&2
	say "check-unreachable-build-guards: this is not a clean verdict." >&2
	exit 2
}

command -v git >/dev/null 2>&1 || cannot_look "no git on PATH"
ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || cannot_look "not inside a git working tree"
cd "$ROOT" || cannot_look "cannot enter $ROOT"

# The corpus is DERIVED from what git tracks, so a new Dockerfile is in scope automatically.
# `-z` and a NUL-delimited read, because a path is allowed to contain anything but NUL.
FILES=""
# THE `|| cannot_look` USED TO HANG OFF THIS `while`, AND IT WAS DEAD CODE: the exit status of a
# `while … done < <(cmd)` is the LOOP's, never the process substitution's. If `git ls-files` died
# AFTER emitting some paths, the gate judged a TRUNCATED corpus and printed CLEAN — and `CHECKED
# -eq 0` does not save it, because that only catches an EMPTY enumeration, not a partial one.
# Reproduced with a git stub that emits one path and exits 1: rc=0, "CLEAN — 1 Dockerfile(s)",
# over a tree whose Dockerfile.stig carried the defect. The green appears exactly when something
# is already wrong, which is when the gate matters most.
#
# Capturing to a file makes the status readable, and the paths stay NUL-delimited end to end: a
# path may contain anything but NUL, and `for f in $FILES` (unquoted) re-split them on IFS, so a
# single Dockerfile under a directory with a space aborted the WHOLE run with a diagnostic naming
# a file that does not exist. Both measured 2026-08-08 by an adversarial contrast of this file.
LIST="$(mktemp)" || cannot_look "mktemp failed"
trap 'rm -f "$LIST"' EXIT
git ls-files -z > "$LIST" 2>/dev/null || cannot_look "git ls-files failed (exit $?); the corpus is unknown, not empty"

FILES_N=0
while IFS= read -r -d '' f; do
	case "$(basename -- "$f")" in
	Dockerfile | Dockerfile.*) FILES_N=$((FILES_N + 1)) ;;
	esac
done < "$LIST"

COUNT=0
FINDINGS=0
CHECKED=0

while IFS= read -r -d '' f; do
	case "$(basename -- "$f")" in
	Dockerfile | Dockerfile.*) ;;
	*) continue ;;
	esac
	[ -r "$f" ] || cannot_look "$f is tracked but unreadable"
	CHECKED=$((CHECKED + 1))

	# Walk RUN blocks. A block starts at a line whose first word is RUN and continues while the
	# previous line ended in a backslash — the shell continuation Docker uses to keep one layer.
	in_block=0
	start=0
	strips=0
	has_s=0
	has_w=0
	reads_syms=0
	lineno=0
	# A trailing line without its newline must still be processed, hence the `|| [ -n "$line" ]`.
	while IFS= read -r line || [ -n "$line" ]; do
		lineno=$((lineno + 1))
		if [ "$in_block" -eq 0 ]; then
			case "$line" in
			RUN\ * | RUN$'\t'*)
				in_block=1
				start=$lineno
				strips=0
				has_s=0
				has_w=0
				reads_syms=0
				;;
			*) continue ;;
			esac
		fi

		# Comments inside a block are real (Docker allows them between continuations) and they are
		# EXACTLY what fooled a reader once: the repaired Dockerfile.fips explains the old defect in
		# prose, and an external contrast reported it as still broken. Strip the comment before
		# judging, so this gate measures the code and not the description of the code.
		#
		# ONLY A `#` THAT OPENS A COMMENT, though. `${line%%#*}` truncated at the FIRST hash
		# anywhere, so an ordinary `sed 's#/a#/b#'` hid everything after it on that line — from
		# BOTH detectors. Docker treats `#` as a comment only at the start of a line (leading
		# whitespace allowed); anywhere else it is data. Measured 2026-08-08 by an adversarial
		# contrast: a live defect went CLEAN because a sed expression sat above it.
		case "$line" in
		[[:space:]]*\#* | \#*)
			stripped="${line#"${line%%[![:space:]]*}"}"
			case "$stripped" in \#*) code="" ;; *) code="$line" ;; esac
			;;
		*) code="$line" ;;
		esac

		# THE FLAGS ARE MATCHED SEPARATELY, not as the literal string `-s -w`. The first cut looked
		# for them adjacent and inside double quotes, so it recognised ONE SPELLING and called
		# itself a class gate. Four live defects went CLEAN, all measured: `-ldflags '-s -X main.v=1
		# -w'` (single quotes, not adjacent), `-ldflags=-s -ldflags=-w` (separate flags), and any
		# ordering variant. Stripping is stripping however it is spelled: what matters is that both
		# -s and -w reach the linker in the same block that later reads symbols.
		case " $code " in
		*" -s "* | *" -s'"* | *' -s"'* | *"-ldflags=-s"* | *"'-s "* | *'"-s '*) has_s=1 ;;
		esac
		case " $code " in
		*" -w "* | *" -w'"* | *' -w"'* | *"-ldflags=-w"* | *"'-w "* | *'"-w '*) has_w=1 ;;
		esac
		case "$code" in
		*-s\ -w* | *-w\ -s*) has_s=1; has_w=1 ;;
		esac
		[ "$has_s" -eq 1 ] && [ "$has_w" -eq 1 ] && strips=1

		case "$code" in
		*"go tool nm"*) reads_syms=1 ;;
		esac

		# A COMMENT-ONLY LINE DOES NOT END THE BLOCK, and getting this wrong made the first cut
		# of this gate report CLEAN on a Dockerfile that carried the exact defect. Docker strips
		# comments BEFORE evaluating the backslash continuation, so a comment sitting between two
		# continued lines is invisible to its parser — but the comment line itself does not end in
		# a backslash, so a naive reader closes the block there and never sees the rest.
		#
		# It was the MUTATION that found it, not review: forcing the gate to judge prose instead
		# of code left the battery fully green, which meant the case meant to prove that behaviour
		# was measuring nothing. The fixture had a comment in the middle, so the block had already
		# closed before the `go tool nm` line was ever read. A surviving mutant is not a passing
		# gate; it is a gate that is not looking.
		case "$(printf '%s' "$code" | tr -d '[:space:]')" in
		"") continue ;;
		esac

		# Block ends when the line does not end in a backslash.
		case "$line" in
		*\\) ;;
		*)
			if [ "$strips" -eq 1 ] && [ "$reads_syms" -eq 1 ]; then
				FINDINGS=$((FINDINGS + 1))
				say "check-unreachable-build-guards: UNREACHABLE GUARD — ${f}:${start}" >&2
				say "  this RUN block links with -s -w and then asserts on 'go tool nm' output." >&2
				say "  Stripping removes the symbol table, so nm prints nothing and the assertion" >&2
				say "  can never hold: the build dies here every time, for everyone." >&2
				say "  repair: assert on the binary's BEHAVIOUR instead — run it and check what it" >&2
				say "          reports. Dockerfile.fips does exactly that and is the reference." >&2
			fi
			in_block=0
			COUNT=$((COUNT + 1))
			;;
		esac
	done < "$f"
done < "$LIST"

if [ "$CHECKED" -eq 0 ]; then
	# ZERO DOCKERFILES IS "NOT APPLICABLE", AND THIS USED TO BE A `cannot_look`. That was the right
	# instinct in the wrong place, and wiring the gate into the pre-push is what proved it: the
	# refclass battery runs the REAL hook inside synthetic sandboxes that have no Dockerfiles, so
	# exiting 2 there took the whole hook down — six of its cases went red, and none of them was
	# about Dockerfiles. Measured 2026-08-08, immediately after wiring.
	#
	# The defence it was standing in for is real but belongs upstream: what must never happen is
	# the ENUMERATION breaking and this gate calling that "clean". That is now caught where it
	# happens, by reading the exit status of `git ls-files` into a file rather than off a `while`
	# whose status is its own. With git having succeeded, zero Dockerfiles means zero Dockerfiles.
	#
	# So it says so and exits 0. It is not a silent green: a verdict naming what it did not find is
	# not the same as one that pretends it looked.
	say "check-unreachable-build-guards: NOT APPLICABLE — the enumeration succeeded and this tree"
	say "  tracks no Dockerfile. Nothing to grade, and nothing hidden: a broken enumeration exits 2"
	say "  above, at the point where it breaks."
	exit 0
fi

if [ "$FINDINGS" -ne 0 ]; then
	say "check-unreachable-build-guards: DIRTY — $FINDINGS unreachable guard(s) in $CHECKED Dockerfile(s)." >&2
	exit 1
fi

say "check-unreachable-build-guards: CLEAN — $CHECKED Dockerfile(s), $COUNT RUN block(s), no guard that cannot pass."
exit 0
