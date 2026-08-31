#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-unreachable-build-guards.sh — prove the gate by MUTATION, in BOTH directions.
#
# The catching direction alone is not enough and this repository has the receipt: a detector only
# tested on "does it find the bad thing" is how the first session-number spec shipped reporting
# ZERO with live collisions. The half that matters here is the NOT-catching direction, because the
# fixture that would most easily produce a false positive is the REPAIRED Dockerfile.fips: it
# explains the old defect in prose, quoting `go tool nm` inside a comment, next to a `-s -w` build.
# An external contrast read exactly that prose and reported the file as still broken. If this gate
# did the same it would be worse than nothing — it would send someone to "fix" correct code.
set -u -o pipefail
export LC_ALL=C

# The subject builds throwaway repositories nowhere, but it DOES call `git ls-files`, so the
# ambient git environment must be cleared or every scenario grades the host repository instead.
# GIT_DIR is exported by git into any hook run from a LINKED worktree, and it OUTRANKS `cd`.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

# ONE case below puts a `git` shim on PATH, and a shim needs a mount that permits execve.
# `mktemp -d` lands under ${TMPDIR:-/tmp}, which this container mounts noexec, so the shim
# would never run and the case would grade the REAL git. lib/exec-workdir.sh exists for
# exactly this and is already shared by two other batteries; see the case for the measurement.
_olivares_exec_workdir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/exec-workdir.sh"
# shellcheck source=/dev/null
. "$_olivares_exec_workdir" || {
	echo "FATAL: cannot source $_olivares_exec_workdir (exec-capable scratch)" >&2
	exit 2
}
unset _olivares_exec_workdir

HERE=$(cd -- "$(dirname -- "$0")" && pwd)
SUT="$HERE/check-unreachable-build-guards.sh"
[ -r "$SUT" ] || { echo "test-unreachable-build-guards: cannot read $SUT" >&2; exit 2; }

# The repository fixtures used to be twelve unrelated tmp.* directories with no cleanup.
# One owned root makes a killed battery leave at most one attributable entry; normal exits remove
# it, and the Taskfile wrapper both verifies and backstops that cleanup.
RUN_TMP="$(mktemp -d "${TMPDIR:-/tmp}/unreachable-build-guards.XXXXXX")" || exit 2
cleanup() { chmod -R u+rwX "$RUN_TMP" 2>/dev/null; rm -rf -- "$RUN_TMP"; }
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

PASS=0; FAIL=0; START=$SECONDS

# Prove the host repository is untouched. The subject only reads, but "it only reads" is a comment
# until something asserts it, and a comment gates nothing.
AMB_ROOT=$(git -C "$HERE" rev-parse --show-toplevel 2>/dev/null || true)
if [ -n "$AMB_ROOT" ]; then
	AMB_HEAD=$(git -C "$AMB_ROOT" rev-parse HEAD 2>/dev/null || echo NONE)
	# The ref NAMES, kept as a list rather than a digest: a digest can only say "something
	# changed", and what this postcondition needs to say is WHICH — see the comparison below.
	AMB_REFS=$(git -C "$AMB_ROOT" for-each-ref --format='%(refname)' 2>/dev/null | LC_ALL=C sort)
fi

mkrepo() {
	local d; d=$(mktemp -d "$RUN_TMP/repo.XXXXXX") || return 1
	git -c init.defaultBranch=main init -q "$d" >/dev/null 2>&1 || return 1
	git -C "$d" config user.email "t@example.invalid"
	git -C "$d" config user.name "t"
	git -C "$d" config commit.gpgsign false
	printf '%s' "$d"
}
add() { # add <repo> <path> <content>
	mkdir -p "$(dirname -- "$1/$2")"; printf '%s\n' "$3" > "$1/$2"
	git -C "$1" add -A >/dev/null 2>&1
}
run() { # run <repo> <want-rc> <label>
	local d="$1" want="$2" label="$3" out rc
	out=$(cd "$d" && bash "$SUT" 2>&1); rc=$?
	if [ "$rc" -eq "$want" ]; then
		PASS=$((PASS + 1)); printf 'ok   %-62s rc=%d\n' "$label" "$rc"
	else
		FAIL=$((FAIL + 1))
		printf 'FAIL %-62s got rc=%d want rc=%d\n' "$label" "$rc" "$want"
		printf '     %s\n' "$out"
	fi
}

# ------------------------------------------------------------ catching direction
d=$(mkrepo)
add "$d" Dockerfile.bad 'FROM golang:1.26 AS build
RUN go build -trimpath -ldflags "-s -w -buildid=" -o /out/app ./cmd/app && \
    go tool nm /out/app | grep -q '"'"'crypto/internal/fips140/v1.0.0'"'"' \
      || (echo FATAL >&2; exit 1)'
run "$d" 1 "strip then nm in ONE RUN block is caught"

# The real Dockerfile.stig shape, byte for byte in the part that matters, so the fixture cannot
# drift away from the defect it stands for.
d=$(mkrepo)
add "$d" Dockerfile.stig 'FROM golang:1.26.5-bookworm AS build
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly GOFIPS140=v1.0.0
ARG VERSION=dev
RUN BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" && \
    go build -trimpath \
      -ldflags "-s -w -buildid= -X main.version=${VERSION}" \
      -o /out/olivares ./cmd/olivares && \
    go tool nm /out/olivares | grep -q '"'"'crypto/internal/fips140/v1.0.0'"'"' \
      || (echo '"'"'FATAL: FIPS 140-3 v1.0.0 module not linked'"'"' >&2; exit 1)'
run "$d" 1 "the historical Dockerfile.stig block is caught"

# A COMMENT IN THE MIDDLE OF THE BLOCK MUST NOT HIDE THE DEFECT. This case exists because the
# first cut of the gate answered CLEAN here: a comment line does not end in a backslash, so a
# naive reader closes the RUN block at the comment and never reaches the `go tool nm` below it.
# Docker strips comments BEFORE evaluating the continuation, so the block really does continue.
# Found by mutation, not by review -- see the note in the subject.
d=$(mkrepo)
add "$d" Dockerfile.trap 'FROM golang:1.26 AS build
RUN go build -trimpath -ldflags "-s -w -buildid=" -o /out/app ./cmd/app && \
    # a perfectly ordinary comment in the middle of a RUN block
    go tool nm /out/app | grep -q '"'"'crypto/internal/fips140/v1.0.0'"'"' \
      || (echo FATAL >&2; exit 1)'
run "$d" 1 "a COMMENT mid-block does not hide the unreachable guard"

# ------------------------------------------------------- NOT-catching direction
# THE ONE THAT MATTERS. The repaired file quotes the old broken command inside a COMMENT while
# still building with -s -w. A gate that reads prose reports this as broken and sends someone to
# un-fix correct code -- which is precisely what an external contrast did to Dockerfile.fips.
d=$(mkrepo)
add "$d" Dockerfile.fips 'FROM golang:1.26.5-bookworm AS build
ENV GOFIPS140=v1.0.0
RUN go build -trimpath -ldflags "-s -w -buildid=" -o /out/olivares ./cmd/olivares && \
    # THIS GUARD USED TO MAKE THE IMAGE UNBUILDABLE. It read
    # `go tool nm /out/olivares | grep -q crypto/internal/fips140/v1.0.0`
    # over the binary the line above had just stripped with -s -w.
    FIPS_JSON="$(GODEBUG=fips140=on /out/olivares version -o json)" && \
    printf '"'"'%s'"'"' "$FIPS_JSON" | grep -q '"'"'"fips"[[:space:]]*:[[:space:]]*"on"'"'"' \
      || (echo FATAL >&2; exit 1)'
run "$d" 0 "a COMMENT quoting the old command is not a live guard"

# Stripping with no symbol assertion at all is ordinary and must stay clean.
d=$(mkrepo)
add "$d" Dockerfile 'FROM golang:1.26 AS build
RUN go build -ldflags "-s -w" -o /out/app ./cmd/app'
run "$d" 0 "stripping WITHOUT a symbol assertion is fine"

# Reading symbols from a binary that was NOT stripped is legitimate -- scripts/fips-verify.sh does
# exactly this, and if the gate flagged it the repair would be to delete a working verifier.
d=$(mkrepo)
add "$d" Dockerfile 'FROM golang:1.26 AS build
RUN go build -o /out/app ./cmd/app && \
    go tool nm /out/app | grep -q crypto/internal/fips140/v1.0.0'
run "$d" 0 "nm WITHOUT stripping is a legitimate assertion"

# Two SEPARATE RUN blocks are two layers. The strip in the first does not make the nm in the
# second unreachable in the same way -- it is a different (and real) question about layer state,
# and answering it here would be inventing a finding.
d=$(mkrepo)
add "$d" Dockerfile 'FROM golang:1.26 AS build
RUN go build -ldflags "-s -w" -o /out/app ./cmd/app
RUN go tool nm /out/app | grep -q something'
run "$d" 0 "the pattern SPLIT across two RUN blocks is not this finding"

# ------------------------------------------- the four spellings the first cut did not see
# All four were live defects that went CLEAN, found by an adversarial contrast of this gate. They
# are here because the first version matched the literal string `-s -w` inside double quotes and
# called itself a class gate: it recognised ONE SPELLING. Stripping is stripping however written.
d=$(mkrepo)
add "$d" Dockerfile.q1 'FROM golang:1.26 AS build
RUN go build -trimpath -ldflags '"'"'-s -X main.v=1 -w'"'"' -o /out/app ./cmd/app && \
    go tool nm /out/app | grep -q crypto/internal/fips140'
run "$d" 1 "single quotes with -s and -w NOT adjacent"

d=$(mkrepo)
add "$d" Dockerfile.q2 'FROM golang:1.26 AS build
RUN go build -ldflags=-s -ldflags=-w -o /out/app ./cmd/app && \
    go tool nm /out/app | grep -q crypto'
run "$d" 1 "-ldflags=-s and -ldflags=-w as separate flags"

# A `#` inside CODE is data, not a comment. `${line%%#*}` truncated at the first hash anywhere, so
# an ordinary sed expression hid the defect below it from both detectors.
d=$(mkrepo)
add "$d" Dockerfile.q3 'FROM golang:1.26 AS build
RUN sed -i '"'"'s#/a#/b#'"'"' x.txt && \
    go build -ldflags "-s -w" -o /out/app ./cmd/app && \
    go tool nm /out/app | grep -q crypto'
run "$d" 1 "a # inside CODE does not truncate the line"

# ...and the mirror for the loosened matcher: a lone -s or a lone -w is NOT stripping, so a build
# carrying only one of them next to a symbol read must stay clean. Without this the widening above
# would be indistinguishable from "flag anything with a dash-s in it".
d=$(mkrepo)
add "$d" Dockerfile.q4 'FROM golang:1.26 AS build
RUN go build -ldflags "-s" -o /out/app ./cmd/app && \
    go tool nm /out/app | grep -q crypto'
run "$d" 0 "-s alone is not stripping: the symbol read is legitimate"

# ---------------------------------------------------------------- could-not-look
# ZERO DOCKERFILES IS NOT APPLICABLE, NOT "could not look". This case asserted exit 2 until wiring
# the gate into the pre-push proved the assertion wrong: the refclass battery runs the real hook in
# sandboxes with no Dockerfiles, and exiting 2 there took the hook down over six unrelated cases.
# The defence that mattered -- a BROKEN enumeration reading as clean -- lives upstream now, on the
# exit status of git ls-files, and has its own case below.
d=$(mkrepo)
add "$d" README.md 'no dockerfiles here'
run "$d" 0 "a tree with NO Dockerfile is NOT APPLICABLE, and says so"

# ...and the defence that replaced it: git failing must still be exit 2. A stub that emits a path
# and then dies is the shape that used to print CLEAN over a truncated corpus.
#
# ⚠ THE STUB NEEDS AN EXEC-CAPABLE MOUNT, and until 2026-08-14 it did not ask for one. It was
# written into `mktemp -d` output, i.e. under ${TMPDIR:-/tmp}, and this container mounts /tmp
# noexec. Measured the first time the battery ran from the gate it now hangs off: the shim never
# executed, PATH fell through to the REAL git, the enumeration SUCCEEDED, and the subject
# correctly answered DIRTY (rc=1) over the fixture's live Dockerfile — so the case reported FAIL
# about a property it had not exercised at all. The battery was green only because nobody ran it
# without TMPDIR pointing somewhere executable. Same class as test-check-secrets.sh case 5, which
# is why lib/exec-workdir.sh exists; two failure modes, and this one is the loud one.
STUB_LABEL="a TRUNCATED enumeration is COULD NOT LOOK, not clean"
d=$(mkrepo)
add "$d" Dockerfile 'FROM x
RUN go build -ldflags "-s -w" -o /o ./c && go tool nm /o | grep -q z'
stubdir="$(olivares_pick_exec_workdir unreachable-guards-stub)" || stubdir=""
if [ -z "$stubdir" ]; then
	# NEVER a silent skip: a montage that could not be built has measured nothing, and a case
	# that quietly disappears is how a battery certifies coverage it does not have.
	FAIL=$((FAIL + 1))
	printf 'FAIL %-62s no exec-capable scratch for the stub\n' "$STUB_LABEL"
else
	stub_ran="$d/.stub-ran"
	printf '#!/bin/sh\nif [ "$1" = "ls-files" ]; then : > "%s"; printf "Dockerfile\\0"; exit 1; fi\nexec /usr/bin/git "$@"\n' \
		"$stub_ran" > "$stubdir/git"
	chmod +x "$stubdir/git"
	out=$(cd "$d" && PATH="$stubdir:$PATH" bash "$SUT" 2>&1); rc=$?
	if [ ! -f "$stub_ran" ]; then
		# CONTROL POSITIVE. Without it the case scores a shim that never ran, which is the
		# defect above wearing its own result: an answer with no measurement behind it.
		FAIL=$((FAIL + 1))
		printf 'FAIL %-62s the stub never ran: nothing was measured\n' "$STUB_LABEL"
	elif [ "$rc" -eq 2 ]; then
		PASS=$((PASS + 1)); printf 'ok   %-62s rc=2\n' "$STUB_LABEL"
	else
		FAIL=$((FAIL + 1)); printf 'FAIL %-62s got rc=%d want rc=2\n' "$STUB_LABEL" "$rc"
		printf '     %s\n' "$out"
	fi
	rm -rf "$stubdir"
fi

out=$(cd /tmp && bash "$SUT" 2>&1); rc=$?
if [ "$rc" -eq 2 ]; then
	PASS=$((PASS + 1)); printf 'ok   %-62s rc=2\n' "outside a git repository is NOT clean"
else
	FAIL=$((FAIL + 1)); printf 'FAIL %-62s got rc=%d want rc=2\n' "outside a git repository is NOT clean" "$rc"
fi

# ---------------------------------------------------------------- post-condition
if [ -n "${AMB_ROOT:-}" ]; then
	now=$(git -C "$AMB_ROOT" rev-parse HEAD 2>/dev/null || echo NONE)
	[ "$now" = "$AMB_HEAD" ] || { FAIL=$((FAIL + 1)); printf 'FAIL %-62s HEAD moved\n' "host repo untouched"; }
	# ⛔ THIS USED TO FAIL ON ANY REF-NAME CHANGE, AND IT WAS WRONG IN BOTH DIRECTIONS.
	#
	# It fingerprinted every ref name in the host repo and demanded nobody move them while this
	# battery ran. On a clone five lanes share that is not an invariant, it is a race: any branch
	# creation, `worktree add -b`, or a fetch bringing a new remote branch breaks it — and the push
	# that pays is the one that did nothing. Measured 2026-08-20: it killed a push whose only crime
	# was that another worktree was being created at the time, and the cost is not the retry, it is
	# that with a push in flight nobody can create a ref for 12-30 minutes.
	#
	# ⭐ AND THE SHARPER HALF: it could never catch the leak it exists for. THIS BATTERY CREATES NO
	# REFS — no branch, no tag, no update-ref anywhere in this file; its fixtures only `git init`
	# and commit. So a GIT_DIR leak from here MOVES the current branch's OID, which is a VALUE
	# change and invisible to a set of NAMES. The property it actually needs is the one right
	# above: `rev-parse HEAD`, which is exactly what a leaked commit would move, and that check
	# stays and is the one that guards.
	#
	# So the name comparison stops being a verdict and becomes a REPORT. A ref that DISAPPEARS is
	# still a failure — deletion is the one shape a stray git operation here could produce that the
	# HEAD check would miss.
	now=$(git -C "$AMB_ROOT" for-each-ref --format='%(refname)' 2>/dev/null | LC_ALL=C sort)
	gone=$(comm -23 <(printf '%s\n' "$AMB_REFS") <(printf '%s\n' "$now") | grep -c . || true)
	added=$(comm -13 <(printf '%s\n' "$AMB_REFS") <(printf '%s\n' "$now") | grep -c . || true)
	if [ "${gone:-0}" -gt 0 ]; then
		FAIL=$((FAIL + 1))
		printf 'FAIL %-62s %s ref(s) DISAPPEARED\n' "host repo untouched" "$gone"
		comm -23 <(printf '%s\n' "$AMB_REFS") <(printf '%s\n' "$now") | sed 's/^/       /'
	fi
	if [ "${added:-0}" -gt 0 ]; then
		# Named, never silent: a tolerated difference nobody prints is the same as no check.
		printf 'note %-62s %s ref(s) appeared meanwhile (another lane; not this battery)\n' \
			"host repo untouched" "$added"
		comm -13 <(printf '%s\n' "$AMB_REFS") <(printf '%s\n' "$now") | head -5 | sed 's/^/       /'
	fi
	[ "$FAIL" -eq 0 ] && { PASS=$((PASS + 1)); printf 'ok   %-62s 2 properties\n' "the host repository is untouched"; }
fi

printf '\ncheck-unreachable-build-guards: %d passed, %d failed, %ds wall\n' "$PASS" "$FAIL" "$((SECONDS - START))"
[ "$FAIL" -eq 0 ] || exit 1
