#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for the branch-aware pre-push classification (GATES v3, 2026-08-02).
#
# WHAT IT PROVES, AND WHY IT HAS TO EXIST. The classification decides whether a push pays
# ~1 hour of build+test or ~4 minutes of lints. A regression in it is INVISIBLE in the
# direction that matters: "the full gate stopped running for main" looks exactly like a
# fast, green push. Nobody would stage a release tag, a five-ref push and a missing
# toolchain by hand to find out, so the rule lives in a script that can be driven from
# stdin, and every scenario is replayed here in seconds.
#
# Eleven rounds of adversarial review shaped this file as much as the code it guards
# (an internal design note (not shipped)). The findings that were about THIS
# FILE, and what each one bought:
#
# G-01 the classifier accepted malformed protocol lines and downgraded them to `fast`
# or `skip`; the single assertion named "a malformed line is classified strictest"
# passed for the wrong reason and covered none of the twelve real forms.
# G-02 33 assertions exercised the CLASSIFIER, and not one proved the HOOK obeys it: a
# one-line `gate_class=fast` injected into the hook made a main push run the fast
# lane, and the whole battery still reported 33/33 green. The hook is now RUN, end
# to end, against stub `task`/`git`, and MUTATED on purpose to prove those runs can
# fail. An assertion that cannot go red is a decoration.
# H-01 the parser was neither byte-exact nor equivalent to git: a NUL and an
# unterminated last record scored `fast`, and a refname git ACCEPTS
# (`refs/heads/a<U+0085>b`) scored `full` because bash character classes follow the
# locale. Both directions are fixtures, and the ref rules are swept byte by byte
# against `git check-ref-format` (255 single-byte injections).
# H-02 `honours_full` asserted six of the seven heavy tasks by hand, so a hook with
# `task tokens:check` deleted passed 97/0. The seven are DECLARED once here and the
# observed call sequence is compared against that list — plus one mutant per task.
# H-05 `$(...)` strips trailing newlines, so `fast<TAB>reason<LF><LF>` satisfied a
# contract that says "exactly one line".
# H-06 the unconditional `task` check broke `git push --delete` on a machine without
# go-task — measured with real git. Deletion-only pushes are now a NAMED exception
# with their own verdict, and every other class stays unconditional.
# I-03 `$(...)` also DELETES NUL bytes: `fa<NUL>st` reached the hook as `fast`.
# I-04 `honours_fast` forbade four heavy tasks by hand, so three could leak into the
# fast lane with the battery green. Both lanes now compare the WHOLE observed
# sequence against a declared list, and there is one mutant per task in both
# directions.
# J-01 the "complete sequence" was the calls the hook made SYNCHRONOUSLY: a deferred
# `(sleep 1; task tokens:check) &` ran a heavy task on a feature push with the
# battery at 217/0. The run now drains every descendant that keeps an inherited
# descriptor (see M-2).
#
# ================== LIMITATIONS, DECLARED — of THIS battery (2026-08-03) =============
# Narrowed to the piece that actually landed. Nothing below is inherited wording: each line
# describes an observation this file really makes, and stops where it really stops.
#
# M-1 THE HEAVY TASKS ARE STUBS. Nothing here runs the real gate; what is proved is WHICH
# tasks are invoked, in what order, and by which lane — never that they pass.
# M-2 THE DRAIN SEES PROCESSES THAT STILL HOLD fd 9. The run passes an extra descriptor on
# the same pipe as stdout and reads it to EOF, so a fork, a subshell or a detached
# grandchild is waited for AS LONG AS IT KEEPS THAT DESCRIPTOR. A descendant that
# closes it — `(exec 9>&-; sleep 1; task tokens:check) &` — reaches EOF early and its
# later work is NOT observed. This is an inherited-descriptor probe, not process
# supervision. Work handed to a process the hook did NOT start (`at`, a systemd unit,
# a message to a daemon already running) leaves no descendant and is not observed
# either.
# M-3 THE OBSERVATION IS THE CALLS THE HOOK MAKES, NOT THE GRAPH go-task EXPANDS. The stub
# `task` records what the hook TYPED. A `deps:` edge added to a fast lint in
# Taskfile.yml can still buy a heavy task on a feature push and nothing here would see
# it — measured in round 7. A resolver for that closure was written and then TAKEN OUT
# for being blind to syntax go-task accepts (L-01, round 11); the hook's header lists
# it under DEFERRED D-2. This file therefore makes NO claim about the Taskfile graph,
# rather than a claim it cannot keep.
# M-4 THE MUTEX IS ONLY OBSERVED AS "TAKEN BY main/tags, NOT TAKEN BY a feature ref". The
# mutex itself is unchanged by this split (the hook's N-4), so its lifecycle — stale
# takeover, owner-safe release, contention, origin down — is NOT exercised here and
# none of it is asserted. `git` is a stub; no remote, real or bare, is touched.
# M-5 THE DOC CHECKS ARE STRING COMPARISONS over CONTRIBUTING.md and, when present, three
# hub-only operating documents. An export names each deliberate omission and checks the
# public contributor document. NARROWED 2026-08-16 (META-06): the exact heavy sequence is
# now compared against CONTRIBUTING.md **and CLAUDE.md** (H-02b, through a normaliser that
# strips `**` and collapses whitespace, so prose keeps its typography). CLAUDE.md went
# first because it is the file every session is ORDERED to obey — and because it HAD
# drifted: it was missing `lint:format-ratchet` while every other copy carried it, which is
# precisely the hole this note described and nobody closed.
# STILL OPEN: WORKFLOW.md and an internal design note (not shipped) are checked only for the marker and
# four phrases that are now false, so a list edited THERE can stay green (L-06, round 11).
# They prove the written policy was updated at all — they cannot prove anybody read it.
# =====================================================================================
#
# NO `set -e` HERE, DELIBERATELY — same reason as scripts/test-pg-test-env.sh: this file
# REPORTS failures through check(); `set -e` would turn a failed assertion into a silent
# STOP and the run would end on a clean-looking tail. Critical SETUP carries `|| exit`.
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
CLASSIFY="$ROOT/scripts/prepush-refclass.sh"
HOOK="$ROOT/.githooks/pre-push"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-refclass.XXXXXX")" || exit 1
# (K-05, round 9) THE RUN REPORTS ITS OWN DURATION. `Taskfile.yml` used to describe this
# battery as "~5s"; round 9 measured 112s on a clean run. A figure written into prose is a
# claim that goes stale silently and that nothing re-measures, so the number now comes from
# the run that is happening, printed with the tally at the end, and the description says
# none.
BAT_T0="$(date +%s)"

# The end-to-end fixtures put stub `task`/`git` on PATH, and PATH lookup needs the exec
# bit: /tmp is tmpfs+noexec in this dev container (same reason lint:export and lint:commerce
# build under the worktree). Probe rather than assume, and fall back to a gitignored
# repo-local directory — a battery that silently stopped running the hook would be the very
# defect G-02 is about.
EXECDIR=""
EXEC_PARENT=""
cleanup() {
	rm -rf "$WORK"
	if [ -n "$EXEC_PARENT" ]; then
 rm -rf "$EXECDIR"
 rmdir "$EXEC_PARENT" 2>/dev/null
	fi
	return 0
}
trap cleanup EXIT HUP INT TERM

exec_capable() {
	local probe="$1/.execprobe.$$"
	printf '#!/bin/sh\nexit 0\n' >"$probe" 2>/dev/null || return 1
	if ! chmod 755 "$probe" 2>/dev/null; then
 rm -f "$probe"
 return 1
	fi
	if "$probe" 2>/dev/null; then
 rm -f "$probe"
 return 0
	fi
	rm -f "$probe"
	return 1
}

if exec_capable "$WORK"; then
	EXECDIR="$WORK"
else
	EXEC_PARENT="$ROOT/.prepush-refclass-tmp"
	mkdir -p "$EXEC_PARENT" || exit 1
	EXECDIR="$(mktemp -d "$EXEC_PARENT/run.XXXXXX")" || exit 1
	if ! exec_capable "$EXECDIR"; then
 echo "test-prepush-refclass: no exec-capable scratch directory (tried \$TMPDIR and" >&2
 echo "test-prepush-refclass: $EXEC_PARENT); the hook cannot be run end to end." >&2
 exit 1
	fi
fi

ZERO="0000000000000000000000000000000000000000"
SOME="1f0e4d6c8b2a90715c3e8d4f2a1b9c8d7e6f5a4b"
ZERO256="$(printf '0%.0s' $(seq 64))"
SOME256="6d9ebe70e71036eb84cbd7920aba35afa455034fad32a34fbfe7cc2fa2fbb1f5"
LOCK_REF="refs/gate-locks/heavy"

pass=0
fail=0
FAILED_NAMES=""
check() {
	if [ "$3" -eq 0 ]; then
 pass=$((pass + 1))
 printf ' ok %-62s %s\n' "$1" "$2"
	else
 fail=$((fail + 1))
 printf ' FAIL %-62s %s\n' "$1" "$2"
 FAILED_NAMES="${FAILED_NAMES}
 - $1 ($2)"
	fi
}

# classify <stdin-text> -> sets $verdict and $reason from the real script.
# The classifier's OUTPUT is NEVER piped into another command: `| tail` (or any pipeline
# whose last member succeeds) discards the exit status this battery is here to read.
# Piping INTO it is fine — the status read back is the classifier's own.
verdict=""
reason=""
rc=0
classify() {
	local out
	out="$(bash "$CLASSIFY" <<<"$1" 2>"$WORK/classify.err")"
	rc=$?
	IFS=$'\t' read -r verdict reason <<<"$out"
}

# classify_raw <printf-format> — byte-exact stdin: a here-string appends a newline of its
# own, which is precisely the difference between "a blank line" and "end of input".
classify_raw() {
	local out
	out="$(printf '%b' "$1" | bash "$CLASSIFY" 2>"$WORK/classify.err")"
	rc=$?
	IFS=$'\t' read -r verdict reason <<<"$out"
}

# classify_fmt <printf-format> [args…] — like classify_raw but the format reaches printf
# untouched, so `\x00` really is a NUL on the classifier's stdin. `%b` cannot carry one and
# no bash variable can hold one, which is exactly why a NUL went unnoticed until round 3.
classify_fmt() {
	local out fmt="$1"
	shift
	out="$(printf "$fmt" "$@" | bash "$CLASSIFY" 2>"$WORK/classify.err")"
	rc=$?
	IFS=$'\t' read -r verdict reason <<<"$out"
}

echo "prepush-refclass — the three families, strictest-wins, and the escapes"

# --- (a) main alone -> FULL ------------------------------------------------------------
classify "refs/heads/main $SOME refs/heads/main $ZERO"
[ "$rc" -eq 0 ] && [ "$verdict" = "full" ]
check "(a) push to main takes the FULL gate" "verdict=${verdict:-<none>}" $?

# --- (b) feature alone -> FAST ---------------------------------------------------------
classify "refs/heads/feature/HUB-gate-split $SOME refs/heads/feature/HUB-gate-split $ZERO"
[ "$rc" -eq 0 ] && [ "$verdict" = "fast" ]
check "(b) push to a feature branch takes FAST lints only" "verdict=${verdict:-<none>}" $?

# --- (c) release tag -> FULL -----------------------------------------------------------
classify "refs/tags/v26.8.0 $SOME refs/tags/v26.8.0 $ZERO"
[ "$rc" -eq 0 ] && [ "$verdict" = "full" ]
check "(c) a release tag takes the FULL gate" "verdict=${verdict:-<none>}" $?

case "$reason" in *v26.8.0*) true ;; *) false ;; esac
check "(c) the reason names the tag it is gating for" "reason mentions the ref" $?

# A tag that is not a release version is still a tag: the family is the namespace, not
# the name. Nothing downstream should have to guess which prefixes count.
classify "refs/tags/rc-scratch $SOME refs/tags/rc-scratch $ZERO"
[ "$verdict" = "full" ]
check "(c') a non-v tag is still a tag" "verdict=${verdict:-<none>}" $?

# --- (d) multi-ref: the STRICTEST wins --------------------------------------------------
classify "refs/heads/feature/x $SOME refs/heads/feature/x $ZERO
refs/heads/main $SOME refs/heads/main $ZERO"
[ "$verdict" = "full" ]
check "(d) feature+main in one push -> the strictest wins" "verdict=${verdict:-<none>}" $?

classify "refs/heads/main $SOME refs/heads/main $ZERO
refs/heads/feature/x $SOME refs/heads/feature/x $ZERO"
[ "$verdict" = "full" ]
check "(d') order does not change the verdict" "verdict=${verdict:-<none>}" $?

classify "refs/heads/feature/x $SOME refs/heads/feature/x $ZERO
refs/tags/v26.8.0 $SOME refs/tags/v26.8.0 $ZERO"
[ "$verdict" = "full" ]
check "(d'') --follow-tags drags the tag's gate along" "verdict=${verdict:-<none>}" $?

# --- (e) deletion -> DELETE (its own verdict since H-06) ---------------------------------
classify "(delete) $ZERO refs/heads/feature/dead $SOME"
[ "$verdict" = "delete" ]
check "(e) a branch deletion gates nothing" "verdict=${verdict:-<none>}" $?

# A deletion of main is still a deletion: there are no commits to test.
classify "(delete) $ZERO refs/heads/main $SOME"
[ "$verdict" = "delete" ]
check "(e') deleting main is a deletion, not a main push" "verdict=${verdict:-<none>}" $?

# (H-06) THE EXCEPTION IS NARROW BY CONSTRUCTION. `delete` requires EVERY line to be a
# deletion, because the hook waives its toolchain check on that verdict alone. One
# gate-lock ref, one commit-carrying ref or one unreadable line and the ordinary verdict
# stands — so no push can smuggle work through the exception.
classify "(delete) $ZERO refs/heads/a $SOME
(delete) $ZERO refs/heads/b $SOME"
[ "$verdict" = "delete" ]
check "(H-06) two deletions are still a deletion push" "verdict=${verdict:-<none>}" $?

classify "(delete) $ZERO refs/heads/a $SOME
refs/heads/l $SOME $LOCK_REF $ZERO"
[ "$verdict" = "skip" ]
check "(H-06) deletion + gate-lock ref is NOT the exception" "verdict=${verdict:-<none>}" $?

classify "(delete) $ZERO refs/heads/a $SOME
refs/heads/feature/x $SOME refs/heads/feature/x $ZERO"
[ "$verdict" = "fast" ]
check "(H-06) deletion + a feature push is a feature push" "verdict=${verdict:-<none>}" $?

classify "(delete) $ZERO refs/heads/a $SOME
refs/heads/f $SOME refs/heads/f"
[ "$verdict" = "full" ]
check "(H-06) deletion + an unreadable line is FULL" "verdict=${verdict:-<none>}" $?

# --- (I-05) A ZERO OID IS AN ORDER GIT GIVES, NOT A SHAPE ANYONE MAY ASSERT ---------------
# Round 5: the classifier kept the local OID and the remote ref but threw the FIRST field
# away, so ANY line with an all-zero local OID counted as a deletion — including
# `refs/heads/main <zeros> refs/heads/main <nonzero>`, a tuple git does not emit for content
# and cannot emit for a deletion either. It scored `delete`, and the hook's named exception
# then exited 0 without a toolchain. No real push was shown to produce it, so this is not a
# demonstrated content bypass; it is the standalone classifier accepting a shape outside its
# own contract, and "only forms git emits" is the contract. Git emits exactly two for a
# zero local OID, both measured against git 2.39.5 in round 5 — the `(delete)` sentinel and
# the literal all-zero token of the same width — so those two are accepted and nothing else.
classify "refs/heads/main $ZERO refs/heads/main $SOME"
[ "$verdict" = "full" ]
check "(I-05) a zero OID with a REF as its local token is not a deletion" \
	"verdict=${verdict:-<none>}" $?

case "$reason" in *"zero"*) true ;; *) false ;; esac
check "(I-05) and the reason names the incoherent tuple" "reason identifies it" $?

classify "refs/heads/main $ZERO refs/heads/main $SOME
refs/heads/main $ZERO refs/heads/main $SOME"
[ "$verdict" = "full" ]
check "(I-05) two identical incoherent tuples stay FULL" "verdict=${verdict:-<none>}" $?

classify "HEAD $ZERO refs/heads/dead $SOME"
[ "$verdict" = "full" ]
check "(I-05) nor is a rev expression a deletion sentinel" "verdict=${verdict:-<none>}" $?

# ...and the two forms git DOES emit must keep working, or this hardening would break
# `git push --delete` — the one operation round 3 already had to rescue once.
classify "$ZERO $ZERO refs/heads/probe $SOME"
[ "$verdict" = "delete" ]
check "(I-05) the all-zero local token git emits IS a deletion" "verdict=${verdict:-<none>}" $?

classify "$ZERO256 $ZERO256 refs/heads/probe $SOME256"
[ "$verdict" = "delete" ]
check "(I-05) same in a sha256 repository (64 zeros)" "verdict=${verdict:-<none>}" $?

# --- (f) the hook's own mutex ref -> SKIP -----------------------------------------------
classify "refs/heads/lock $SOME refs/gate-locks/heavy $ZERO"
[ "$verdict" = "skip" ]
check "(f) refs/gate-locks/* is signalling, not content" "verdict=${verdict:-<none>}" $?

# --- (f2) the canon's anti-collision claim ref -> SKIP -----------------------------------
# Same class as the mutex above. It scored FULL as an "unrecognised ref" until 2026-08-09,
# which meant the protocol the canon MANDATES before a batch push cost a 2h30 gate to publish
# a pointer — so the only way to obey it was --no-verify, i.e. disabling the whole hook on a
# step that gates nothing. That is precisely the failure the deletion exception exists to stop.
classify "refs/heads/claim $SOME refs/integration-claims/integrador-20260809T000000Z $ZERO"
[ "$verdict" = "skip" ]
check "(f2) refs/integration-claims/* is signalling, not content" "verdict=${verdict:-<none>}" $?

# --- (f3) ...but it does NOT launder a ref that IS content -------------------------------
# The non-firing direction, and the one that matters: if the skip were a property of the PUSH
# rather than of the ref, a claim travelling alongside main would drag main into the fast lane.
# Strictest-wins must still hold.
classify "refs/heads/main $SOME refs/heads/main $ZERO
refs/heads/claim $SOME refs/integration-claims/integrador-20260809T000000Z $ZERO"
[ "$verdict" = "full" ]
check "(f3) a claim ref travelling WITH main is still FULL" "verdict=${verdict:-<none>}" $?

# --- (f2b) the session-claim namespace, same class and same two directions ---------------
# Added 2026-08-20 with the reservation protocol. The fixture ref carries NO S-number on
# purpose: the arm matches the NAMESPACE, not the name, and an S-shaped token in a shipped
# STRING is a leak the export gate refuses — measured, it rejected this file's first version. A number picked but not yet pushable is
# published as refs/session-claims/S<N>-<lane>, which turns a sentence in a mailbox into
# something for-each-ref can count. Without this arm it falls to `*` and scores FULL, so a
# reservation costs the full gate and the only way to reserve is --no-verify.
classify "refs/heads/claim $SOME refs/session-claims/reserved-by-a-lane $ZERO"
[ "$verdict" = "skip" ]
check "(f2b) refs/session-claims/* is a reserved number, not content" "verdict=${verdict:-<none>}" $?

# The non-firing direction, which is the one that would hurt: skip is a property of the REF.
classify "refs/heads/main $SOME refs/heads/main $ZERO
refs/heads/claim $SOME refs/session-claims/reserved-by-a-lane $ZERO"
[ "$verdict" = "full" ]
check "(f2c) a session-claim travelling WITH main is still FULL" "verdict=${verdict:-<none>}" $?

# ⛔ AND THE ARM MUST NOT REACH refs/heads/claim/*, which is where the integrator first put one.
# Under refs/heads a skip would let arbitrary content through the branch lane, and this
# classifier is pure bash by design so it cannot check that a claim carries none. A branch is a
# branch: it gets the branch lane.
classify "refs/heads/claim $SOME refs/heads/claim/reserved-by-a-lane $ZERO"
[ "$verdict" = "fast" ]
check "(f2d) refs/heads/claim/* is a BRANCH and gets the branch lane" "verdict=${verdict:-<none>}" $?

# --- (f4) and an unknown namespace is still deny-closed ----------------------------------
classify "refs/heads/x $SOME refs/not-a-known-namespace/x $ZERO"
[ "$verdict" = "full" ]
check "(f4) an unrecognised namespace is still classified strictest" "verdict=${verdict:-<none>}" $?

# --- (f5) refs/archive/* — preserving work must not cost more than losing it --------------
# Fifty-six branches were archived on 2026-08-10 and every one scored FULL as an unrecognised
# ref: 2h30 per rescued branch, on work that is abandoned and usually stale by weeks. The only
# ways past that are to delete the work or to bypass the hook. Third instance of one pattern,
# after the mutex and the claim.
classify "refs/heads/dead $SOME refs/archive/feature/abandoned-visual-asset-work $ZERO"
[ "$verdict" = "skip" ]
check "(f5) refs/archive/* is write-only preservation, not delivery" "verdict=${verdict:-<none>}" $?

# --- (f6) the non-firing direction: it is a property of the REF, not of the push ----------
classify "refs/heads/main $SOME refs/heads/main $ZERO
refs/heads/dead $SOME refs/archive/old $ZERO"
[ "$verdict" = "full" ]
check "(f6) an archive ref travelling WITH main is still FULL" "verdict=${verdict:-<none>}" $?

# --- (f7) and the exemption does NOT generalise to a namespace someone invents ------------
# Another lane preserved work under refs/salvage/* the same afternoon. Naming ONE sanctioned
# namespace is the whole safety of f5: if any invented prefix were exempt, ungated content
# would reach the server under a name of the pusher's choosing.
classify "refs/heads/x $SOME refs/salvage/x $ZERO"
[ "$verdict" = "full" ]
check "(f7) an invented preservation namespace is NOT exempt" "verdict=${verdict:-<none>}" $?
# --- (S) the canonical live-status ref -> FAST (D-4-status, 2026-08-03) ------------------
# One EXACT ref, not a namespace. refs/status/live is the coordination stream the status
# protocol publishes to (an internal design note (not shipped)): the exact ref
# is the capability, every sibling/nested/case-variant status ref stays at the deny-closed
# default, and main/tag dominance is untouched because promote() is monotonic.
STATUS_LIVE="refs/heads/publish $SOME refs/status/live $ZERO"

classify "$STATUS_LIVE"
[ "$verdict" = "fast" ]
check "(S-01) refs/status/live is the canonical status stream — FAST" \
	"verdict=${verdict:-<none>}" $?

case "$reason" in *"status stream"*) true ;; *) false ;; esac
check "(S-01) and the reason names the status stream" "reason names it" $?

for sib in refs/status/LIVE refs/status/lane refs/status/live/nested refs/status/archive refs/status; do
	classify "refs/heads/x $SOME $sib $ZERO"
	[ "$verdict" = "full" ]
	check "(S-02) sibling '$sib' stays deny-closed FULL" "verdict=${verdict:-<none>}" $?
done

classify "$STATUS_LIVE
refs/heads/main $SOME refs/heads/main $ZERO"
[ "$verdict" = "full" ]
check "(S-03) status + main is FULL" "verdict=${verdict:-<none>}" $?

classify "refs/heads/main $SOME refs/heads/main $ZERO
$STATUS_LIVE"
[ "$verdict" = "full" ]
check "(S-03) main + status is FULL — order does not matter" "verdict=${verdict:-<none>}" $?

classify "$STATUS_LIVE
refs/tags/v26.8.0 $SOME refs/tags/v26.8.0 $ZERO"
[ "$verdict" = "full" ]
check "(S-03) status + tag is FULL" "verdict=${verdict:-<none>}" $?

classify "$STATUS_LIVE
refs/heads/x $SOME refs/notes/commits $ZERO"
[ "$verdict" = "full" ]
check "(S-03) status + unknown namespace is FULL" "verdict=${verdict:-<none>}" $?

classify "$STATUS_LIVE
refs/heads/f $SOME refs/heads/f"
[ "$verdict" = "full" ]
check "(S-03) status + a malformed line is FULL" "verdict=${verdict:-<none>}" $?

classify "$STATUS_LIVE
refs/heads/feature/x $SOME refs/heads/feature/x $ZERO"
[ "$verdict" = "fast" ]
check "(S-04) status + feature stays FAST" "verdict=${verdict:-<none>}" $?

classify "$STATUS_LIVE
refs/heads/lock $SOME refs/gate-locks/heavy $ZERO"
[ "$verdict" = "fast" ]
check "(S-04) status + gate-lock stays FAST" "verdict=${verdict:-<none>}" $?

classify "$STATUS_LIVE
$ZERO $ZERO refs/heads/dead $SOME"
[ "$verdict" = "fast" ]
check "(S-04) status + a branch deletion: the content class wins — FAST" \
	"verdict=${verdict:-<none>}" $?

classify "$ZERO $ZERO refs/status/live $SOME"
[ "$verdict" = "delete" ]
check "(S-04) pure deletion of the status ref keeps the delete verdict" \
	"verdict=${verdict:-<none>}" $?

classify "refs/heads/publish $SOME256 refs/status/live $ZERO256"
[ "$verdict" = "fast" ]
check "(S-05) a sha256 record gives the same FAST answer" "verdict=${verdict:-<none>}" $?

out_s="$(OLIVARES_FAST_PUSH=1 bash "$CLASSIFY" <<<"$STATUS_LIVE")"
IFS=$'\t' read -r verdict_s _ <<<"$out_s"
[ "$verdict_s" = "fast" ]
check "(S-06) OLIVARES_FAST_PUSH does not alter the status verdict" \
	"verdict=${verdict_s:-<none>}" $?

# --- (g) OLIVARES_FAST_PUSH=1 on main -> STILL FULL --------------------------------------
out_g="$(OLIVARES_FAST_PUSH=1 bash "$CLASSIFY" <<<"refs/heads/main $SOME refs/heads/main $ZERO")"
rc_g=$?
IFS=$'\t' read -r verdict_g reason_g <<<"$out_g"
[ "$rc_g" -eq 0 ] && [ "$verdict_g" = "full" ]
check "(g) OLIVARES_FAST_PUSH=1 does NOT downgrade main" "verdict=${verdict_g:-<none>}" $?

case "$reason_g" in *IGNORED*) true ;; *) false ;; esac
check "(g) and it SAYS the escape was ignored" "reason names the ignored escape" $?

tag_line="refs/tags/v26.8.0 $SOME refs/tags/v26.8.0 $ZERO"
out_gt="$(OLIVARES_FAST_PUSH=1 bash "$CLASSIFY" <<<"$tag_line")"
IFS=$'\t' read -r verdict_gt _ <<<"$out_gt"
[ "$verdict_gt" = "full" ]
check "(g') nor a tag" "verdict=${verdict_gt:-<none>}" $?

# --- (h) OLIVARES_FAST_PUSH=1 on a feature branch -> FAST --------------------------------
feat_line="refs/heads/feature/x $SOME refs/heads/feature/x $ZERO"
out_h="$(OLIVARES_FAST_PUSH=1 bash "$CLASSIFY" <<<"$feat_line")"
rc_h=$?
IFS=$'\t' read -r verdict_h _ <<<"$out_h"
[ "$rc_h" -eq 0 ] && [ "$verdict_h" = "fast" ]
check "(h) OLIVARES_FAST_PUSH=1 on a feature branch stays FAST" "verdict=${verdict_h:-<none>}" $?

# --- (j) empty stdin -> SKIP -------------------------------------------------------------
out_j="$(bash "$CLASSIFY" </dev/null)"
rc_j=$?
IFS=$'\t' read -r verdict_j _ <<<"$out_j"
[ "$rc_j" -eq 0 ] && [ "$verdict_j" = "skip" ]
check "(j) empty stdin gates nothing" "verdict=${verdict_j:-<none>}" $?

# --- DENY-CLOSED: an unknown namespace costs MORE, never less ----------------------------
classify "refs/heads/x $SOME refs/notes/commits $ZERO"
[ "$verdict" = "full" ]
check "an unrecognised remote ref is classified strictest" "verdict=${verdict:-<none>}" $?

# The field that decides is the REMOTE ref, not the local one: `git push origin
# feature/x:main` publishes to main and must pay for main.
classify "refs/heads/feature/x $SOME refs/heads/main $ZERO"
[ "$verdict" = "full" ]
check "feature:main is a MAIN push (remote ref decides)" "verdict=${verdict:-<none>}" $?

echo
echo "G-01 — a line this rule cannot read is DENY-CLOSED, not a licence to run less"

# Every one of these was measured returning `fast` or `skip` from the first revision
# (audit table, 2026-08-02). The old single assertion — two fields with a non-zero second
# field — went green because the EMPTY third field fell into the unknown-namespace arm, not
# because malformed syntax was detected. That is a vacuous assertion, and it hid all twelve.
malformed_case() { # <label> <stdin-text>
	classify "$2"
	[ "$verdict" = "full" ]
	check "(G-01) $1 -> FULL" "verdict=${verdict:-<none>}" $?
}
malformed_case "one field" "refs/heads/x"
malformed_case "two fields, zero oid" "refs/heads/x $ZERO"
malformed_case "three fields" "refs/heads/f $SOME refs/heads/f"
malformed_case "five fields" "refs/heads/f $SOME refs/heads/f $ZERO extra"
malformed_case "non-hex local oid" "refs/heads/f zzzz refs/heads/f $ZERO"
malformed_case "short local oid" "refs/heads/f 1f0e4d6c refs/heads/f $ZERO"
malformed_case "non-hex remote oid" "refs/heads/f $SOME refs/heads/f nope"
malformed_case "UPPER-case oid" "refs/heads/f ${SOME^^} refs/heads/f $ZERO"
malformed_case "empty ref refs/heads/" "refs/heads/x $SOME refs/heads/ $ZERO"
malformed_case "refs/heads/foo..bar" "refs/heads/x $SOME refs/heads/foo..bar $ZERO"
malformed_case "refs/heads/.hidden" "refs/heads/x $SOME refs/heads/.hidden $ZERO"
malformed_case "refs/heads/x.lock" "refs/heads/x $SOME refs/heads/x.lock $ZERO"
malformed_case "one-level ref (no refs/)" "refs/heads/x $SOME heads/main $ZERO"
malformed_case "blank line + a feature ref" "
refs/heads/feature/x $SOME refs/heads/feature/x $ZERO"

classify_raw "refs/heads/feature/x $SOME refs/heads/feature/x $ZERO\r\n"
[ "$verdict" = "full" ]
check "(G-01) a CRLF line -> FULL" "verdict=${verdict:-<none>}" $?

# EOF-with-no-lines is the ONLY silent skip, and it must stay distinguishable from a line
# that happens to be empty — otherwise "nothing to push" and "I could not parse this"
# collapse into the same green.
classify_raw ""
[ "$verdict" = "skip" ]
check "(G-01) true EOF (zero lines) is still SKIP" "verdict=${verdict:-<none>}" $?

classify_raw "\n"
[ "$verdict" = "full" ]
check "(G-01) but ONE blank line is not EOF -> FULL" "verdict=${verdict:-<none>}" $?

case "$reason" in *blank*) true ;; *) false ;; esac
check "(G-01) and the reason NAMES the offending line" "reason identifies it" $?

# Quoted back, but BOUNDED: the reason is printed to a terminal and TAB-split by the hook,
# so the offending line is sanitized and truncated rather than echoed raw.
classify "refs/heads/f $SOME refs/heads/f"
case "$reason" in *"refs/heads/f $SOME"*) true ;; *) false ;; esac
check "(G-01) a malformed line is quoted back in the reason" "line echoed" $?

classify_raw "refs/heads/f $SOME refs/heads/f \a\a\a\n"
case "$reason" in *"$(printf '\a')"*) false ;; *) true ;; esac
check "(G-01) and control characters never reach the reason" "sanitized" $?

# A malformed line must not be rescued by a well-formed one travelling with it: strictest
# wins applies to "I could not read this" exactly as it applies to main.
classify "refs/heads/feature/x $SOME refs/heads/feature/x $ZERO
refs/heads/f $SOME refs/heads/f"
[ "$verdict" = "full" ]
check "(G-01) malformed + feature in one push -> FULL" "verdict=${verdict:-<none>}" $?

# The legitimate shapes real git emits must NOT be dragged into `full` by the new strictness
# — a deny-closed rule that fires on ordinary work would simply be turned off. Measured
# against git 2.39.5 on 2026-08-02 (bare-remote fixture, one push per form).
legit_case() { # <label> <expected> <stdin>
	classify "$3"
	[ "$verdict" = "$2" ]
	check "(G-01) legitimate git form: $1" "verdict=${verdict:-<none>} want=$2" $?
}
legit_case "git push origin HEAD" fast "HEAD $SOME refs/heads/feature/x $ZERO"
legit_case "HEAD~0:refs/heads/x" fast "HEAD~0 $SOME refs/heads/feature/x $ZERO"
legit_case "main^{}:refs/heads/x" fast "main^{} $SOME refs/heads/feature/x $ZERO"
legit_case "(delete) sentinel" delete "(delete) $ZERO refs/heads/dead $SOME"
legit_case "sha256 repository (64 hex)" full "HEAD $SOME256 refs/heads/main $ZERO256"
legit_case "annotated tag object" full "refs/tags/v1.2.3 $SOME refs/tags/v1.2.3 $ZERO"

# --- H-01: BYTES IN, BYTES JUDGED --------------------------------------------------------
# Both of these were measured returning `fast` from the round-3 tree, i.e. a push git never
# sent, classified as an ordinary feature branch.

# A NUL cannot travel through `%b`, a variable or a command substitution — bash deletes it
# at every one of those doors, which is precisely how a ref containing one was read as
# `refs/heads/feature/x` and gated as a feature branch.
classify_fmt 'refs/heads/fea\x00ture/x %s refs/heads/fea\x00ture/x %s\n' "$SOME" "$ZERO"
[ "$verdict" = "full" ]
check "(H-01) a NUL byte in the protocol -> FULL" "verdict=${verdict:-<none>}" $?

case "$reason" in *NUL*) true ;; *) false ;; esac
check "(H-01) and the reason names the NUL" "reason identifies it" $?

# Every record git sends ends in LF — measured 2026-08-02 by hexdumping the hook's own
# stdin for a one-ref push, a three-ref push and a deletion. A stream that ends without it
# is not the protocol, and guessing what the truncated tail meant is not this file's job.
classify_fmt 'refs/heads/feature/x %s refs/heads/feature/x %s' "$SOME" "$ZERO"
[ "$verdict" = "full" ]
check "(H-01) a last record with no terminating LF -> FULL" "verdict=${verdict:-<none>}" $?

# ...but a WELL-FORMED stream must still be read to the end: the same code path decides
# both, and a fix that refused everything would pass the two checks above.
classify_fmt 'refs/heads/feature/x %s refs/heads/feature/x %s\n' "$SOME" "$ZERO"
[ "$verdict" = "fast" ]
check "(H-01) and an LF-terminated line is still read" "verdict=${verdict:-<none>}" $?

two_records='refs/heads/feature/x %s refs/heads/feature/x %s\n'
two_records+='refs/heads/main %s refs/heads/main %s\n'
classify_fmt "$two_records" "$SOME" "$ZERO" "$SOME" "$ZERO"
[ "$verdict" = "full" ]
check "(H-01) multi-line streams still split on every LF" "verdict=${verdict:-<none>}" $?

# A BIG push, because `git push --all`/`--mirror` is where a line reader stops being free.
# The first round-4 implementation split the buffer by hand and was quadratic — measured at
# 3s for 1000 refs and 51s for 5000, plus a subshell per ref for sanitising. Both are gone;
# what this asserts is the property that must survive any future rewrite: 500 ordinary refs
# plus main is still ONE verdict, and it is main's.
big_push=""
for i in $(seq 1 500); do
	big_push+="refs/heads/f$i $SOME refs/heads/feature/x$i $ZERO"$'\n'
done
big_verdict="$(printf '%s%s\n' "$big_push" "refs/heads/main $SOME refs/heads/main $ZERO" |
	bash "$CLASSIFY" | cut -f1)"
[ "$big_verdict" = "full" ]
check "(H-01) a 501-ref push is read whole, strictest wins" "verdict=${big_verdict:-<none>}" $?

# LOCALE INDEPENDENCE. `[[:cntrl:]]` under C.UTF-8 classifies U+0085 and U+2028 as control
# characters; git forbids ASCII controls only. Round 3 pushed both of these refs to a real
# bare remote successfully and the classifier called them malformed. The classifier must
# judge BYTES whatever the caller's locale is, so this runs it under a UTF-8 locale on
# purpose — the assertion is about the script forcing its own semantics, not about ours.
utf8_case() { # <label> <ref-bytes-as-printf-escapes> <expected>
	local out
	out="$(LC_ALL=C.UTF-8 bash -c "printf 'refs/heads/$2 %s refs/heads/$2 %s\n' '$SOME' '$ZERO' |
 LC_ALL=C.UTF-8 bash '$CLASSIFY'")"
	IFS=$'\t' read -r verdict reason <<<"$out"
	[ "$verdict" = "$3" ]
	check "(H-01) under C.UTF-8: $1" "verdict=${verdict:-<none>} want=$3" $?
}
# Three answers: if this container has no UTF-8 locale, LC_ALL falls back to C and the five
# cases below would pass for the wrong reason. Assert the locale really is in effect first,
# by measuring the very property that broke (U+0085 read as a control character).
LC_ALL=C.UTF-8 bash -c '[[ $'"'"'\xc2\x85'"'"' == *[[:cntrl:]]* ]]'
check "(H-01) precondition: a UTF-8 locale is really in effect" "C.UTF-8 usable" $?

utf8_case "a<U+0085>b is a valid refname (git agrees)" 'a\xc2\x85b' fast
utf8_case "a<U+2028>b is a valid refname (git agrees)" 'a\xe2\x80\xa8b' fast
utf8_case "a<0x80>b, a bare high-bit byte, is valid too" 'a\x80b' fast
utf8_case "a<DEL>b is NOT (git rejects it)" 'a\x7fb' full
utf8_case "a<0x1f>b is NOT (ASCII control)" 'a\x1fb' full

# PARITY WITH GIT ITSELF. The ref rules are reimplemented in bash inside the classifier
# (no subprocess per line), so they are checked against the authority: `git check-ref-format`
# must agree on every name under refs/. The ONE intentional divergence is documented in the
# classifier — a destination outside refs/ is refused here, and git fully qualifies every
# remote ref before the hook sees it (`main:foo` arrives as `refs/heads/foo`), so no real
# push can hit that arm.
if ! command -v git >/dev/null 2>&1; then
	check "(G-01) ref-format parity vs git" "git is NOT installed — cannot check" 1
else
	parity_bad=""
	# shellcheck disable=SC1090
	source /dev/stdin <<<"$(sed -n '/^valid_remote_ref()/,/^}/p' "$CLASSIFY")"
	if ! declare -F valid_remote_ref >/dev/null; then
 # Three answers, not two: "the classifier exposes no ref validator" is NOT "the refs
 # are fine", and must not be reported as a parity mismatch either.
 valid_remote_ref() { return 1; }
 parity_bad=" NO valid_remote_ref() in $CLASSIFY —"
	fi
	for r in refs/heads/main refs/heads/a/b/c refs/tags/v1.2.3 refs/notes/commits \
 "$LOCK_REF" refs/heads/ refs/heads/foo..bar refs/heads/.hid refs/heads/x.lock \
 refs/heads/a//b 'refs/heads/x@{1}' refs/heads/tail. 'refs/heads/st*r' \
 'refs/heads/ca:ret' 'refs/heads/til~de' 'refs/heads/back\slash' \
 refs/heads/mid./dle refs/heads/lock.lock/x refs/heads/a.b.c; do
 if git check-ref-format "$r" 2>/dev/null; then g=ok; else g=bad; fi
 if valid_remote_ref "$r"; then m=ok; else m=bad; fi
 [ "$g" = "$m" ] || parity_bad="$parity_bad $r(git=$g,mine=$m)"
	done
	[ -z "$parity_bad" ]
	check "(G-01) ref-format parity vs git check-ref-format" "${parity_bad:-19/19 agree}" $?

	# (H-01) THE BYTE SWEEP. The structural forms above are all ASCII, and round 3 measured
	# that the hole was elsewhere: bytes and locale. So inject EVERY byte 0x01..0xff into a
	# refname and require the WHOLE classifier — field pattern and ref validator together,
	# run as the hook runs it — to agree with git about that name. NUL is excluded because
	# it cannot travel in argv to `git check-ref-format`; it has its own fixture above.
	# ~2s, and it is the only assertion here that can see a locale or a high-bit byte.
	byte_bad=""
	byte_n=0
	for i in $(seq 1 255); do
 # `printf -v` and NOT a command substitution: $(...) strips the trailing LF, so byte
 # 10 would silently become "no byte at all" and test nothing.
 printf -v byte "\\x$(printf '%02x' "$i")"
 bref="refs/heads/a${byte}b"
 if git check-ref-format "$bref" 2>/dev/null; then g=ok; else g=bad; fi
 bverdict="$(printf '%s %s %s %s\n' "$bref" "$SOME" "$bref" "$ZERO" |
 bash "$CLASSIFY" | cut -f1)"
 if [ "$bverdict" = "fast" ]; then m=ok; else m=bad; fi
 byte_n=$((byte_n + 1))
 [ "$g" = "$m" ] ||
 byte_bad="$byte_bad 0x$(printf '%02x' "$i")(git=$g,mine=$bverdict)"
	done
	[ -z "$byte_bad" ]
	check "(H-01) byte sweep: 255 injections agree with git" "${byte_bad:-${byte_n}/255 agree}" $?
fi

echo
echo "G-02/G-05 — the HOOK, run end to end against stub task/git"

# ------------------------------------------------------------------------------------------
# The harness. 33 assertions used to exercise the classifier and call that "wiring", proved
# by two greps and an awk. It let a `gate_class=fast` injected into the hook survive: main
# ran 20 fast tasks, zero heavy ones, exit 0, battery green. So: build a throwaway tree with
# the hook and rule UNDER TEST, put stub `task` and `git` on PATH, run the hook with real
# stdin, and read what it actually called.
E2E_LOCK_OID="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
e2e_rc=0
e2e_log=""
e2e_out=""
E2E_TIMEOUT=""

e2e_setup() { # <hook> <classifier> -> prints the fixture dir
	local hook="$1" rule="$2" dir
	dir="$(mktemp -d "$EXECDIR/e2e.XXXXXX")" || return 1
	mkdir -p "$dir/scripts/lib" "$dir/bin" || return 1
	cp "$hook" "$dir/pre-push" || return 1
	cp "$rule" "$dir/scripts/prepush-refclass.sh" || return 1
	# The git-env sanitiser the hook sources before anything else. It is copied rather than
	# stubbed because the hook FAILS CLOSED without it (exit 2), which is the correct
	# production behaviour and would otherwise turn every case in this battery red for a
	# reason that has nothing to do with ref classification.
	cp "$ROOT/scripts/lib/git-env.sh" "$dir/scripts/lib/git-env.sh" || return 1
	# The gate's Postgres probe, stubbed: this battery is about which lane runs, not about
	# whether THIS machine has a server.
	printf '#!/usr/bin/env bash\necho "export OLIVARES_TEST_POSTGRES_DSN=stub"\n' \
 >"$dir/scripts/pg-test-env.sh" || return 1
	cat >"$dir/bin/task" <<-'STUB' || return 1
 #!/usr/bin/env bash
 printf 'task %s\n' "$*" >>"$STUB_LOG"
 exit 0
	STUB
	# The stub `git` exists to let the mutex COMPLETE, not to model it (M-4): it hands back a
	# lock oid for commit-tree and succeeds at every push, so acquisition is immediate and the
	# EXIT trap releases. What the assertions read is WHETHER those calls happened at all —
	# main and tags take the lock, a feature ref never reaches it.
	cat >"$dir/bin/git" <<-'STUB' || return 1
 #!/usr/bin/env bash
 printf 'git %s\n' "$*" >>"$STUB_LOG"
 case "$1" in
 commit-tree) echo "${STUB_LOCK_OID}" ;;
 esac
 exit 0
	STUB
	chmod 755 "$dir/bin/task" "$dir/bin/git" "$dir/scripts/pg-test-env.sh" || return 1
	printf '%s' "$dir"
}

e2e_run() { # <hook> <classifier> <stdin> [VAR=VAL ...]
	local hook="$1" rule="$2" input="$3" dir rcfile drain
	shift 3
	dir="$(e2e_setup "$hook" "$rule")" || return 1
	e2e_log="$dir/calls.log"
	: >"$e2e_log"
	rcfile="$dir/hook.rc"
	: >"$rcfile"
	# The lock wait is INJECTED at 0: the production default is 3h, one `sleep 60` at a time,
	# and a battery that hangs reports nothing at all — the worst of the three answers.
	# E2E_TIMEOUT is the same reasoning applied to the run as a whole: a case that must fail
	# fast says so, and a regression shows up as 124 instead of silence.
	#
	# (J-01) THE HOOK RETURNING IS NOT THE HOOK FINISHING. Until round 7 this read the call log
	# the instant the hook exited, so `(sleep 1; task tokens:check) &` injected into the fast
	# lane ran a HEAVY task on a feature push and the battery still reported 217 passed / 0
	# failed — measured with a persistent probe: hook_rc=0, tokens_immediate=0,
	# tokens_after_2s=1. "The gate declared this lane clean before the expensive thing ran" is
	# the exact shape of a green push that gated nothing.
	#
	# So the run holds an EXTRA inherited descriptor. fd 9 is a second handle on the same pipe
	# as stdout, and a descendant that redirects its own stdout and stderr to /dev/null still
	# inherits it: EOF on that pipe therefore means every process the hook started AND KEPT
	# THAT DESCRIPTOR has exited (M-2 states the escape). `timeout … cat` bounds it — a drain
	# with no deadline would trade a blind spot for a hang.
	drain="${E2E_TIMEOUT:+$((E2E_TIMEOUT + 10))}"
	e2e_out="$( {
 cd "$dir" && env -i PATH="$dir/bin:/usr/bin:/bin" HOME="$dir" \
 STUB_LOG="$e2e_log" STUB_LOCK_OID="$E2E_LOCK_OID" \
 OLIVARES_GATE_LOCK_WAIT_SECS=0 "$@" \
 ${E2E_TIMEOUT:+timeout "$E2E_TIMEOUT"} bash ./pre-push <<<"$input"
 printf '%s' "$?" >"$rcfile"
	} 2>&1 9>&1 | timeout "${drain:-30}" cat)"
	# 125 is "this battery never got a status", which is not the same as any status the hook
	# could return — the third answer applies to the harness too.
	e2e_rc=""
	IFS= read -r e2e_rc <"$rcfile" 2>/dev/null || true
	case "$e2e_rc" in '' | *[!0-9]*) e2e_rc=125 ;; esac
	return 0
}

# `-e` is not decoration: every fragment below starts with `--force-with-lease=`, and grep
# would read that as its own option.
called() { grep -qxF -e "$1" "$e2e_log"; } # an exact call line
called_like() { grep -qF -e "$1" "$e2e_log"; } # a fragment of one

FAST_MAIN="refs/heads/main $SOME refs/heads/main $ZERO"
FAST_FEAT="refs/heads/feature/x $SOME refs/heads/feature/x $ZERO"
FAST_TAG="refs/tags/v26.8.0 $SOME refs/tags/v26.8.0 $ZERO"

# THE HEAVY TASKS, DECLARED ONCE (H-02). Round 3 removed `task tokens:check` from the
# hook and this battery still reported 97 passed / 0 failed, because `honours_full` listed
# six calls by hand and nobody noticed which one was missing. A hand-written list of calls
# is not a list of the gate: it is six independent assertions that happen to be near each
# other. So the list lives here, once, and three separate things are compared against it —
# the sequence the hook actually calls, the banner the hook prints, and the list
# CONTRIBUTING.md gives contributors.
#
# TEN since 2026-08-10: `build:cloud` and `test:cloud:norace` joined, because cloud/ was
# gated by NOTHING — not in go.work, not named by this hook, and absent from every CI
# workflow — which is how a canon decision of 2026-07-24 never reached the cloud control
# plane and a live exporter to the retired provider survived two weeks unseen.
# test:cloud:norace already called itself the "push gate correctness leg" in its own desc.
#
# EIGHT since 2026-08-06: `test:license-worker` joined the lane, first because it costs
# 4.05s. It was in mainline-ci and in NO local gate, so a `main` push could — and twice
# did — land a red licence Worker and block every other lane behind it.
HEAVY_TASKS=(test:license-worker build:cloud test:cloud:norace check:web tokens:check lint:format-ratchet lint:guide-docs lint:raw-palette test:web web:check build:go test sdk:check)
heavy_declared="$(printf '%s\n' "${HEAVY_TASKS[@]}")"
heavy_joined="$(printf '%s + ' "${HEAVY_TASKS[@]}")"
heavy_joined="${heavy_joined% + }"

# THE FAST LINTS, DECLARED ONCE TOO (I-04, round 5). The heavy list above is what a feature
# push must NOT run — and round 5 measured that "must not run" was being checked against four
# names typed by hand, so three of the seven heavy tasks could leak into the fast lane with
# the battery still green. A prohibition list is only as good as its completeness, and nobody
# maintains one. So the fast lane declares what it DOES run, the observed sequence is required
# to equal it EXACTLY (nothing missing, nothing extra, in this order), and the heavy list it
# is allowed to run is the empty one. Both directions then come from the same equality:
# an eighth task appears, a declared one disappears, or the order drifts — all red.
FAST_LINTS=(
	lint:mid-operation lint:disk-headroom lint:overlay-seal lint:release-anchor-identity:selftest lint:int-12-no-land lint:hook-leg-wiring lint:hook-leg-wiring:selftest lint:worktree-identity lint:conflict-markers lint:run-block-syntax lint:run-block-syntax:selftest lint:release-disk-floor lint:unbound-env lint:channel-parity
	lint:session-record lint:holds lint:holds-selftest lint:inbox lint:inbox:selftest lint:publish-inbox-shape lint:git-env lint:git-env:selftest
	lint:session-numbers lint:commit-identity lint:commit-identity-gate lint:portal-brand lint:emitted-urls lint:readme-task-claims
	vet lint:export lint:js-lexer lint:export-closure lint:hooks-path lint:hooks-path-gate lint:addon-sets
	lint:addon-sets-gate lint:nul lint:nul-gate lint:hub-battery lint:program-anchors lint:brief-effort
	lint:spdx lint:spdx-gate lint:gofmt lint:brand-parity lint:email-brand lint:go-toolchain
	lint:boundary lint:verifier-truth lint:migrations lint:d1-migrations lint:connectors lint:connector-inventory
	lint:cli-registries lint:agent-surfaces lint:connector-onboard lint:migrations:coverage lint:pin-distance lint:fiscal-id
	lint:unreachable-build-guards lint:i18n lint:i18n-anchors lint:engine-output lint:datatable-empty lint:console-perms
	lint:docs-parity lint:adr-not-published lint:public-counts lint:cli-coverage lint:console-route-docs lint:ci-env-reach
	lint:config-coverage lint:screenshot-freshness lint:motor-declarado lint:motor-declarado:selftest lint:mute-pipefail lint:cli-transport-exempt lint:cli-transport-exempt:selftest
	lint:watchdog-unpublished-work lint:gate-lock-order lint:client-callers lint:client-callers-selftest lint:kms-backends
	lint:release-version lint:launch-image-refs lint:screenshot-coverage lint:launch-placeholders lint:launch-counts lint:cloud-availability-claims lint:video-scene-anchors lint:docs-honesty
	lint:commerce lint:commerce-entity-fks lint:commerce-entity-fks:selftest lint:security-txt lint:cosign-pins lint:release-mechanics
	lint:actions lint:pg-env lint:json-decoders lint:error-mappers lint:build-bin-parity lint:web-e2e-boot
	lint:session-duplicates
	# Las tres baterias que cableo al carril rapido: sus gates llegaron con banco propio y
	# NO las invocaba nadie. La de spdx-awk va con OLIVARES_SPDX_AWK_SKIP_E2E=1 en el gancho —su
	# fila e2e corre el gate real de SPDX, que `task lint:spdx` ya paga— y el env va en su propia
	# linea porque un prefijo `VAR=1 task …` es invisible para el extractor de check-gate-parity.
	lint:sigpipe-booleans:selftest lint:spdx:awk-selftest ci:run-skipped-steps:selftest lint:claim-lector:selftest
	lint:npm-vuln-gate lint:status-ref lint:ci-ports lint:cra-pack lint:decision-index
	lint:decision-index:selftest lint:taskfile-shape lint:taskfile-shape:selftest lint:taskfile-graph lint:taskfile-executable lint:taskfile-executable:selftest
	lint:phone-home-claims lint:phone-home-claims:selftest lint:translation-drift lint:classify-allowlist lint:classify-allowlist:selftest lint:docsite-versions lint:docsite-versions:selftest
	lint:docs-redirects lint:docs-redirects:selftest lint:md-links lint:md-links:selftest lint:download-contract lint:download-contract:selftest lint:sigpipe-booleans lint:hub-web-fidelity
	lint:web-bundle-freshness lint:list-truncation-witness lint:list-truncation-witness:selftest lint:badge-vacio-filas lint:badge-vacio-filas:selftest lint:capture-view-keys lint:capture-view-keys:selftest lint:seed-payloads:selftest lint:exec-tmpdir:selftest lint:sembrado-volumen:selftest lint:adopcion-otlp:selftest lint:tenant-bound-writes:selftest lint:tenant-bound-reads lint:tenant-bound-reads:selftest
	lint:0022-published-schema lint:0022-published-schema:selftest lint:ver-10-og lint:ver-10-og:selftest lint:baseline-shrink lint:relay-pointer
	test:publish-enterprise-artifacts test:cli-walk lint:commit-msg-closing lint:timeout-headroom lint:branch-protection lint:rebase-web-branch
	lint:ci-timeout-arithmetic lint:ci-timeout-arithmetic:selftest lint:ci-step-guard lint:ci-step-guard:selftest lint:ci-inner-clocks lint:ci-inner-clocks:selftest lint:ci-sibling-legs lint:ci-sibling-legs:selftest
	lint:test-hook-parallelism lint:test-hook-parallelism:selftest lint:ci-secrets-cap lint:alc-02-f1-hold:selftest lint:alc-02-f2-hold:selftest lint:alc-02-f3-hold:selftest
	lint:alc-02-f4-hold:selftest lint:alc-04-xff-hold:selftest lint:c02-hold-key-until-producer:selftest lint:c02-r2-key-from-token:selftest lint:c02-r2-set-key-prep:selftest lint:c03-02-sign-features-prep:selftest
	lint:c03-02-sign-features:selftest lint:c03-11-fase-r:selftest lint:c05-14-effect-key-prep:selftest lint:c13-07-ar-holds-prep:selftest lint:c13-07-holds lint:cfg-01-production-hold:selftest lint:eco-01-activation-hold:selftest
	lint:eco-11-reserve-funded:selftest lint:meta-12-phone-home:selftest lint:ver-02-cut-hold:selftest lint:ci-history-depth lint:ci-history-depth:selftest
	lint:openapi-op-descriptions lint:runbook-coverage lint:reserve-numbers:selftest lint:release-diagrams lint:claim-safety:selftest lint:classify-paths-parity lint:classify-paths-parity:selftest lint:orphan-batteries lint:orphan-batteries:selftest lint:purge-scratch:selftest lint:race-report:selftest lint:race-groups:selftest lint:release-smoke-answers lint:release-smoke-stamp lint:prepush-treesnap lint:prepush-refclass
)
fast_declared="$(printf '%s\n' "${FAST_LINTS[@]}")"
full_declared="$(printf '%s\n' "${FAST_LINTS[@]}" "${HEAVY_TASKS[@]}")"

# observed_heavy — every `task` call the hook made AFTER the last fast lint, in order. The
# fast lints end at lint:prepush-refclass; everything past it is the heavy gate, including
# the low-memory branch (`GOFLAGS=-p=1 GOMAXPROCS=2 task test` logs as `task test`, which is
# why the branch cannot hide a missing call either).
observed_heavy() {
	awk '/^task lint:prepush-refclass$/ { seen = 1; next }
 seen && /^task / { sub(/^task /, ""); print }' "$e2e_log"
}

# observed_tasks — EVERY `task` call the hook made, in order, wherever it made it. This is the
# total shape, and it is what makes the two predicates below non-vacuous: `observed_heavy`
# only looks AFTER the last fast lint, so a heavy call injected anywhere earlier — which is
# exactly where the I-04 mutant lands — is invisible to it. An equality against a declared
# sequence needs no prohibition list to maintain: anything not declared is extra, and extra
# is red.
observed_tasks() {
	# `--list-all` NO es un lint: es la comprobacion de que `task` puede LEER el Taskfile, que el
	# hook hace antes de todo. Entro el 2026-08-19 al medir un fail-open del gate entero — con un
	# Taskfile que no parsea, el hook salia **0** y los 55 lints no corrian —, y como el stub de
	# `task` registra toda llamada, aparecia en esta secuencia y rompia las diez casillas que
	# comparan lo observado con la lista DECLARADA.
	#
	# Se excluye AQUI y no se declara en FAST_LINTS a proposito: declararlo diria que es un gate
	# mas, y no lo es. La lista declarada tiene que seguir siendo exactamente los lints.
	awk '/^task / { sub(/^task /, ""); if ($0 != "--list-all") print }' "$e2e_log"
}

# honours_full/honours_fast are the PROPERTIES, factored out so the same predicate can be
# pointed at a deliberately broken hook further down. If they cannot fail, they prove nothing.
honours_full() { # <hook> <stdin>
	e2e_run "$1" "$CLASSIFY" "$2" || return 1
	[ "$e2e_rc" -eq 0 ] || return 1
	called "task lint:export" || return 1
	called "task lint:prepush-refclass" || return 1
	# THE list, in order, nothing missing and nothing extra. `called` per task would still
	# pass a hook that ran them twice, in the wrong order, or with an eighth task nobody
	# reviewed; an equality against the declared sequence catches all three.
	[ "$(observed_heavy)" = "$heavy_declared" ] || return 1
	# ...and the WHOLE sequence, fast lints included: the check above starts counting at the
	# last fast lint, so it cannot see a call inserted before one.
	[ "$(observed_tasks)" = "$full_declared" ] || return 1
	called_like "git commit-tree" || return 1
	return 0
}

honours_fast() { # <hook> <stdin>
	e2e_run "$1" "$CLASSIFY" "$2" || return 1
	[ "$e2e_rc" -eq 0 ] || return 1
	called "task lint:export" || return 1
	called "task lint:prepush-refclass" || return 1
	# THE WHOLE POINT OF THE SPLIT, AND IT MUST DISCRIMINATE (I-04). Until round 5 this was
	# four hand-written prohibitions — `check:web`, `build:go`, `test`, `sdk:check` — so
	# `tokens:check`, `test:web` and `web:check` could run on a feature push with the battery
	# green. The observed sequence is now required to BE the declared fast list, and the heavy
	# list observed in this lane is required to be the declared EMPTY one.
	[ "$(observed_tasks)" = "$fast_declared" ] || return 1
	[ -z "$(observed_heavy)" ] || return 1
	! called_like "git commit-tree" || return 1
	! called_like "git push" || return 1
	return 0
}

honours_full "$HOOK" "$FAST_MAIN"
check "(G-02) a main push RUNS the full gate and takes the mutex" "rc=${e2e_rc}" $?

# The observed sequence, named in the failure message: "some assertion in honours_full went
# red" is not a finding, and this is the one that H-02 was about.
#
# `check … "$(…)" $?` WOULD BE VACUOUS, and this file caught itself doing it: arguments are
# expanded left to right, so a command substitution in the MESSAGE runs before `$?` is
# expanded and `$?` then reports that subshell (always 0). Measured against the H-02 red
# fixture — a hook with `task tokens:check` removed made this assertion pass. The status is
# captured into a variable BEFORE anything else runs.
[ "$(observed_heavy)" = "$heavy_declared" ]
heavy_seq_rc=$?
# The COUNT comes from the array, never from a word typed in the label. `test:license-worker`
# joining the lane on 2026-08-06 would otherwise have left two assertions saying "seven"
# about eight tasks — a message that lies is the seed of the next hand-maintained list.
check "(H-02) the heavy calls ARE the ${#HEAVY_TASKS[@]} declared, in order" \
	"observed=[$(observed_heavy | tr '\n' ' ')]" "$heavy_seq_rc"

# The banner the developer reads must be the same list. It used to be typed out twice in
# the hook (banner and calls), which is how a list can be true in one place and stale in
# the other for weeks.
case "$e2e_out" in *"$heavy_joined"*) true ;; *) false ;; esac
check "(H-02) and the hook's banner names exactly those seven" "banner == declared list" $?

honours_full "$HOOK" "$FAST_TAG"
check "(G-02) a tag push RUNS the full gate" "rc=${e2e_rc}" $?

# The low-memory branch runs a DIFFERENT command line for `task test`, so a check that only
# looks at the ordinary branch cannot see it drop the call. Force it with a threshold no
# machine can satisfy and require the same seven, in the same order.
e2e_run "$HOOK" "$CLASSIFY" "$FAST_MAIN" OLIVARES_GATE_MEM_MIN_GIB=999999
[ "$e2e_rc" -eq 0 ] && [ "$(observed_heavy)" = "$heavy_declared" ]
check "(H-02) the low-memory branch runs the same seven" "rc=${e2e_rc}" $?

case "$e2e_out" in *"degraded serial mode"*) true ;; *) false ;; esac
check "(H-02) and it says it is degraded, not silently different" "branch announced" $?

honours_fast "$HOOK" "$FAST_FEAT"
check "(G-02) a feature push runs fast lints ONLY, no mutex" "rc=${e2e_rc}" $?

# (S-07) The status stream rides the SAME fast lane, end to end: exactly the declared fast
# sequence with lint:export first, an empty heavy list, and no mutex traffic. The equality
# inside honours_fast is what makes mutant C below killable — a hook that skips one lint
# only for status changes the observed sequence and goes red here.
honours_fast "$HOOK" "$STATUS_LIVE"
check "(S-07) a status-ref push runs fast lints ONLY, no mutex" "rc=${e2e_rc}" $?

honours_full "$HOOK" "$STATUS_LIVE
$FAST_MAIN"
check "(S-07) status + main through the HOOK runs the full gate" "rc=${e2e_rc}" $?

honours_full "$HOOK" "$FAST_FEAT
$FAST_MAIN"
check "(G-02) multi-ref feature+main runs the full gate" "rc=${e2e_rc}" $?

honours_full "$HOOK" "$FAST_MAIN
$FAST_FEAT"
check "(G-02) multi-ref main+feature (other order) too" "rc=${e2e_rc}" $?

# OLIVARES_FAST_PUSH=1 must not reach the heavy lane through the hook either — the escape is
# reported ignored by the classifier, and the hook must not act on it independently.
e2e_run "$HOOK" "$CLASSIFY" "$FAST_MAIN" OLIVARES_FAST_PUSH=1
[ "$e2e_rc" -eq 0 ] && called "task test" && called "task build:go"
check "(G-02) OLIVARES_FAST_PUSH=1 on main still RUNS the heavy gate" "rc=${e2e_rc}" $?

case "$e2e_out" in *IGNORED*) true ;; *) false ;; esac
check "(G-02) and the hook prints that the escape was ignored" "hook says IGNORED" $?

# --- MUTANTS: the assertions above must be able to FAIL ------------------------------------
# Two one-line integration regressions, injected at the anchor the hook carries for exactly
# this purpose. If either survives, the end-to-end checks are decorative and this file says so.
mutate_hook() { # <name> <injected line> -> prints the mutant path
	local name="$1" inject="$2" out="$WORK/mutant-$1"
	awk -v inject="$inject" '
 { print }
 /MUTATION-ANCHOR/ { print inject; hit++ }
 END { if (hit != 1) exit 1 }
	' "$HOOK" >"$out" || return 1
	printf '%s' "$out"
}


mut_fast="$(mutate_hook "forcefast" 'gate_class=fast')"
check "(G-02) the mutation anchor is present exactly once" "injected 'gate_class=fast'" $?

if [ -n "$mut_fast" ]; then
	honours_full "$mut_fast" "$FAST_MAIN"
	[ "$?" -ne 0 ]
	check "(G-02) MUTANT 'main->fast' is CAUGHT by the full-lane check" "mutant dies" $?
else
	check "(G-02) MUTANT 'main->fast' is CAUGHT by the full-lane check" "no mutant built" 1
fi

mut_full="$(mutate_hook "forcefull" 'gate_class=full')"
if [ -n "$mut_full" ]; then
	honours_fast "$mut_full" "$FAST_FEAT"
	[ "$?" -ne 0 ]
	check "(G-02) MUTANT 'feature->full' is CAUGHT by the fast-lane check" "mutant dies" $?
else
	check "(G-02) MUTANT 'feature->full' is CAUGHT by the fast-lane check" "no mutant built" 1
fi

# A third mutant, aimed at the property the mutex exists for: dropping the fast-lane exit
# would put every feature push back on the host lock.
mut_nolock="$WORK/mutant-nofastexit"
sed 's/^if \[ "\$gate_class" != "full" \]; then$/if false; then/' "$HOOK" >"$mut_nolock"
honours_fast "$mut_nolock" "$FAST_FEAT"
[ "$?" -ne 0 ]
check "(G-02) MUTANT 'feature falls through to the mutex' is CAUGHT" "mutant dies" $?

# --- (S-08) MUTANTS for the status arm — assertions that cannot go red prove nothing -----
grep -q 'refs/status/live)' "$CLASSIFY"
check "(S-08) the exact status arm exists to be mutated" "arm present" $?

# A: the exact arm deleted -> the status line must fall back to deny-closed FULL, which is
# what S-01 discriminates.
mut_sa="$WORK/classify-no-status"
sed '\|refs/status/live)|,/;;/d' "$CLASSIFY" >"$mut_sa"
out_sa="$(bash "$mut_sa" <<<"$STATUS_LIVE" | cut -f1)"
[ "$out_sa" = "full" ]
check "(S-08) MUTANT 'status arm deleted' is CAUGHT" "mutant verdict=${out_sa:-<none>}" $?

# B: the arm broadened to the whole namespace -> a sibling rides the fast lane under the
# mutant, which is what S-02 discriminates.
mut_sb="$WORK/classify-broad-status"
sed 's|refs/status/live)|refs/status/*)|' "$CLASSIFY" >"$mut_sb"
out_sb="$(bash "$mut_sb" <<<"refs/heads/x $SOME refs/status/lane $ZERO" | cut -f1)"
[ "$out_sb" = "fast" ]
check "(S-08) MUTANT 'arm broadened to refs/status/*' is CAUGHT" \
	"sibling rides fast under the mutant" $?

# C: the hook skips lint:export ONLY for a status push -> the exact-sequence equality in
# honours_fast (S-07) loses its first lint and goes red.
mut_sc="$WORK/hook-no-export-for-status"
sed 's|^task lint:export$|case "$gate_reason" in *status*) : ;; *) task lint:export ;; esac|' \
	"$HOOK" >"$mut_sc"
if grep -q 'gate_reason.*status' "$mut_sc" && bash -n "$mut_sc" 2>/dev/null; then
	honours_fast "$mut_sc" "$STATUS_LIVE"
	[ $? -ne 0 ]
	check "(S-08) MUTANT 'skip lint:export for status' is CAUGHT" "mutant dies" $?
else
	check "(S-08) MUTANT 'skip lint:export for status' is CAUGHT" "no usable mutant" 1
fi

# --- (H-02) ONE MUTANT PER HEAVY TASK ------------------------------------------------------
# The exact experiment that survived round 3: remove one heavy call and see whether anything
# goes red. It is run for all seven, and the call is REPLACED by `true` rather than deleted,
# so the mutant stays syntactically valid — a mutant that dies of a syntax error proves the
# hook cannot run, not that the gate is checked. Both branches of the low-memory `if` are
# replaced together, which is the case a `grep -v` would have missed.
mutate_drop_task() { # <task> -> prints the mutant path
	local t="$1" out="$WORK/mutant-notask-${1//[^a-zA-Z0-9]/_}"
	awk -v t="$t" '
 {
 line = $0
 sub(/^[ \t]+/, "", line)
 if (line ~ /^#/) { print; next }
 # `task X` y también `task X || true`: a repository gate se cableó en modo INFORME y el ancla
 # al final de línea no lo veía, así que su mutante salía «no usable» y la guarda
 # quedaba declarada pero INALCANZABLE — una fila de la lista que ninguna casilla
 # podía matar. Medido el 2026-08-16.
 if (line ~ ("(^|[ \t])task " t "( \\|\\| true)?$")) {
 hit++; sub(/task [^ ]+( \|\| true)?$/, "true"); print; next
 }
 print
 }
 END { if (hit < 1) exit 1 }
	' "$HOOK" >"$out" || return 1
	printf '%s' "$out"
}

for t in "${HEAVY_TASKS[@]}"; do
	mut_task="$(mutate_drop_task "$t")"
	if [ -z "$mut_task" ]; then
 check "(H-02) MUTANT 'no task $t' is CAUGHT" "no call to mutate — hook drifted" 1
 continue
	fi
	if ! bash -n "$mut_task" 2>/dev/null; then
 check "(H-02) MUTANT 'no task $t' is CAUGHT" "mutant is not valid bash" 1
 continue
	fi
	honours_full "$mut_task" "$FAST_MAIN"
	[ "$?" -ne 0 ]
	check "(H-02) MUTANT 'no task $t' is CAUGHT" "mutant dies (rc=${e2e_rc})" $?
done

# --- (I-04) THE OTHER HALF OF THE SAME PROPERTY: A HEAVY TASK LEAKING INTO THE FAST LANE ----
# Round 5, the finding that mattered most even though it was filed MEDIUM: `honours_fast`
# forbade four of the seven heavy tasks BY HAND, so a hook carrying
# `if [ "$gate_class" = "fast" ]; then task tokens:check; fi` ran a heavy task on every
# feature push and this battery still reported 160 passed / 0 failed. The assertion that
# justifies the whole split — "a feature ref runs NO heavy task" — did not discriminate; it
# was a vacuous test in the load-bearing place.
#
# A hand-written prohibition list is the same defect H-02 fixed in the other direction, so
# the cure is the same: the fast lane DECLARES its list (FAST_LINTS, and an EMPTY heavy list)
# and the observed sequence is compared against both. These mutants are the proof, one per
# heavy task, injected at the anchor — which sits BEFORE the fast lints, i.e. in the one
# position `observed_heavy` alone could never see.
for t in "${HEAVY_TASKS[@]}"; do
	mut_leak="$(mutate_hook "leak-${t//[^a-zA-Z0-9]/_}" \
 "if [ \"\$gate_class\" = \"fast\" ]; then task $t; fi")"
	if [ -z "$mut_leak" ]; then
 check "(I-04) MUTANT 'feature runs task $t' is CAUGHT" "no mutant built" 1
 continue
	fi
	honours_fast "$mut_leak" "$FAST_FEAT"
	[ "$?" -ne 0 ]
	check "(I-04) MUTANT 'feature runs task $t' is CAUGHT" "mutant dies (rc=${e2e_rc})" $?
done

# --- (J-01) A TASK THAT RUNS AFTER THE HOOK RETURNS IS STILL A TASK THAT RAN --------------
# Round 7's first mutant: `(sleep 1; task tokens:check) >/dev/null 2>&1 &` in the fast lane.
# Every assertion above passed — the observed sequence at the moment the hook exited WAS the
# declared fast list — and a heavy task ran a second later. The equality was true and the
# claim it supports ("a feature ref runs NO heavy task") was false, which is worse than a
# missing check: it is a check that certifies the opposite of what it measures.
# The drain in e2e_run is what closes it; these are the mutants that prove the drain works,
# one per heavy task, so no hand-maintained list can be incomplete this time either.
for t in "${HEAVY_TASKS[@]}"; do
	mut_defer="$(mutate_hook "defer-${t//[^a-zA-Z0-9]/_}" \
 "if [ \"\$gate_class\" = \"fast\" ]; then (sleep 1; task $t) >/dev/null 2>&1 & fi")"
	if [ -z "$mut_defer" ] || ! bash -n "$mut_defer" 2>/dev/null; then
 check "(J-01) MUTANT 'feature DEFERS task $t' is CAUGHT" "no usable mutant" 1
 continue
	fi
	honours_fast "$mut_defer" "$FAST_FEAT"
	[ "$?" -ne 0 ]
	check "(J-01) MUTANT 'feature DEFERS task $t' is CAUGHT" "mutant dies (rc=${e2e_rc})" $?
done

# ...and the drain must not have turned every honest run into a wait: the real hook still
# reports its own status, not the harness's 125, and the fast lane is still clean.
honours_fast "$HOOK" "$FAST_FEAT"
[ "$?" -eq 0 ] && [ "$e2e_rc" -eq 0 ]
check "(J-01) and the real hook still passes the drained observation" "rc=${e2e_rc}" $?
# AND THE THIRD COPY, the banner. Same argument as H-02 made for the heavy list: the hook
# prints a list of fast lints that a developer reads as "this is what ran", and a printed list
# maintained by hand drifts from the calls beside it. Parsed and compared as a SEQUENCE, not
# grepped: `i18n` is a substring of `i18n-anchors`, so a substring test would pass for the
# wrong reason exactly where this is supposed to be exact.
banner_fast_list() { # -> one short lint name per line, in banner order
	printf '%s\n' "$e2e_out" |
 sed -n '/FAST lints (/,/)\./p' |
 sed 's/^pre-push:[[:space:]]*//; s/^FAST lints (//; s/)\..*$//' |
 tr '+' '\n' |
 sed 's/^[[:space:]]*//; s/[[:space:]]*$//; /^$/d'
}
fast_short="$(printf '%s\n' "${FAST_LINTS[@]}" | sed 's/^lint://')"
e2e_run "$HOOK" "$CLASSIFY" "$FAST_FEAT"
[ "$(banner_fast_list)" = "$fast_short" ]
banner_fast_rc=$?
check "(I-04) the hook's FAST banner names exactly the declared lints" \
	"banner=[$(banner_fast_list | tr '\n' ' ')]" "$banner_fast_rc"

# The same equality must bite in the MISSING direction too: a fast lint silently dropped is
# how "the gate stopped running" looks like a green push. One mutant per declared fast lint,
# because the four that happened to be asserted by hand were not the four that mattered.
for t in "${FAST_LINTS[@]}"; do
	mut_fastdrop="$(mutate_drop_task "$t")"
	if [ -z "$mut_fastdrop" ] || ! bash -n "$mut_fastdrop" 2>/dev/null; then
 check "(I-04) MUTANT 'no task $t' is CAUGHT in the fast lane" "no usable mutant" 1
 continue
	fi
	honours_fast "$mut_fastdrop" "$FAST_FEAT"
	[ "$?" -ne 0 ]
	check "(I-04) MUTANT 'no task $t' is CAUGHT in the fast lane" "mutant dies (rc=${e2e_rc})" $?
done
echo
echo "G-05/H-06 — the toolchain check is unconditional, with ONE named exception"

# The rule as written was absolute: "missing task or classifier -> failure (never green)".
# Round 2 made it true by moving the check in front of the `skip` verdict. Round 3 measured
# the price with real git: `git push --delete stale-branch` on a machine without go-task was
# refused, so tidying a branch required disabling the whole hook. H-06 re-decides — a push
# whose EVERY line is a deletion has nothing to gate in any lane, gets its own verdict, and
# is the ONE exception; it is named here, in the hook and in CLAUDE.md/CONTRIBUTING.md.
env -i PATH=/usr/bin:/bin HOME="$WORK" bash -c 'command -v task' >/dev/null 2>&1
[ "$?" -ne 0 ]
check "(i) precondition: 'task' really is absent in the probe env" "no task on the stub PATH" $?

notask_case() { # <label> <stdin>
	local out rc
	out="$(cd "$ROOT" && env -i PATH=/usr/bin:/bin HOME="$WORK" bash "$HOOK" <<<"$2" 2>&1)"
	rc=$?
	[ "$rc" -ne 0 ] && case "$out" in *"'task' not found"*) true ;; *) false ;; esac
	check "(G-05) no task, $1 -> REFUSED" "exit=${rc}" $?
}
notask_case "feature push" "$FAST_FEAT"
notask_case "gate-lock ref" "refs/heads/l $SOME $LOCK_REF $ZERO"
notask_case "empty stdin" ""
notask_case "malformed line" "refs/heads/f $SOME refs/heads/f"
# A deletion travelling with anything else is NOT the exception: the classifier only says
# `delete` when every line is one, so the exception cannot be used as a carrier.
notask_case "deletion + a gate-lock ref" "(delete) $ZERO refs/heads/dead $SOME
refs/heads/l $SOME $LOCK_REF $ZERO"
notask_case "deletion + a feature push" "(delete) $ZERO refs/heads/dead $SOME
$FAST_FEAT"

# ...and the exception itself: the operation that needs no toolchain because it runs no gate.
notask_run() { # <stdin> -> prints the output, returns the hook's status
	(cd "$ROOT" && env -i PATH=/usr/bin:/bin HOME="$WORK" bash "$HOOK" <<<"$1" 2>&1)
}
notask_delete_out="$(notask_run "(delete) $ZERO refs/heads/dead $SOME")"
notask_delete_rc=$?
[ "$notask_delete_rc" -eq 0 ]
check "(H-06) no task, a PURE deletion -> ALLOWED" "exit=${notask_delete_rc}" $?

case "$notask_delete_out" in *"named deletion exception"*) true ;; *) false ;; esac
check "(H-06) and the hook NAMES the exception it is applying" "exception named" $?

case "$notask_delete_out" in *"task"*"not found"*) false ;; *) true ;; esac
check "(H-06) without ever looking for the toolchain it does not need" "no task probe" $?

# Two deletions in one push are still a deletion push.
notask_run "(delete) $ZERO refs/heads/a $SOME
(delete) $ZERO refs/heads/b $SOME" >/dev/null
notask_delete2_rc=$?
[ "$notask_delete2_rc" -eq 0 ]
check "(H-06) no task, TWO deletions in one push -> ALLOWED" "exit=${notask_delete2_rc}" $?

out_i="$(cd "$ROOT" && env -i PATH=/usr/bin:/bin HOME="$WORK" bash "$HOOK" <<<"$FAST_FEAT" 2>&1)"
case "$out_i" in *"CANNOT run"*) true ;; *) false ;; esac
check "(i) and says the gate could not look, not that it passed" "'CANNOT run' stated" $?

# WITH a toolchain present, the skip classes must still cost nothing: exit 0, zero tasks.
skip_case() { # <label> <stdin>
	e2e_run "$HOOK" "$CLASSIFY" "$2"
	[ "$e2e_rc" -eq 0 ] && [ ! -s "$e2e_log" ]
	check "(G-05) with task present, $1 -> 0 and NO task runs" "rc=${e2e_rc}" $?
}
skip_case "branch deletion" "(delete) $ZERO refs/heads/dead $SOME"
skip_case "gate-lock ref" "refs/heads/l $SOME $LOCK_REF $ZERO"
skip_case "deletion + gate-lock ref" "(delete) $ZERO refs/heads/dead $SOME
refs/heads/l $SOME $LOCK_REF $ZERO"
echo
echo "G-03 — an unusable verdict refuses, and 'unusable' now means what it says"

# The hook used to read the first WORD of the first LINE. A classifier printing `fast` and
# then `full` was accepted, and a main push took the fast lane. Exactly one line, exactly
# `<verdict><TAB><non-empty reason>`.
mkdir -p "$WORK/badrule/scripts/lib" || exit 1
cp "$HOOK" "$WORK/badrule/pre-push" || exit 1
# Same reason as in e2e_setup: without the git-env sanitiser the hook refuses at exit 2
# before it ever reads a verdict, and these cases would then "pass" for the wrong reason —
# they assert a refusal ABOUT THE VERDICT, and a refusal about a missing sanitiser is a
# different refusal wearing the same exit code.
cp "$ROOT/scripts/lib/git-env.sh" "$WORK/badrule/scripts/lib/git-env.sh" || exit 1
stub_rule() {
	printf '#!/usr/bin/env bash\n%s\n' "$1" >"$WORK/badrule/scripts/prepush-refclass.sh"
}

bad_verdict_case() { # <label> <script body> [expected-message-fragment]
	local rc=0 out
	stub_rule "$1" || exit 1
	out="$(cd "$WORK/badrule" && bash ./pre-push <<<"$FAST_MAIN" 2>&1)" || rc=$?
	[ "$rc" -ne 0 ] && case "$out" in *"${3:-unusable classification}"*) true ;; *) false ;; esac
	check "(G-03) $2 -> REFUSED" "exit=${rc}" $?
}
bad_verdict_case 'exit 3' "a FAILING classifier" "classification FAILED"
bad_verdict_case 'echo "maybe"' "a bare unknown word"
bad_verdict_case 'printf "fast\tfirst\nfull\tsecond\n"' "two CONTRADICTORY verdict lines"
bad_verdict_case 'printf "fast\tone\nfast\ttwo\n"' "two lines even if they agree"
bad_verdict_case 'printf "fast\n"' "a verdict with NO reason"
bad_verdict_case 'printf "fast\t\n"' "a verdict with an EMPTY reason"
bad_verdict_case 'printf "fast\treason\textra\n"' "an extra TAB-separated field"
bad_verdict_case 'printf "fast\treason\r\n"' "a CR in the verdict line"
bad_verdict_case 'printf "fast reason\n"' "a SPACE where the TAB belongs"
bad_verdict_case 'true' "no output at all"
bad_verdict_case 'printf " fast\treason\n"' "a leading space before the verdict"

# (H-05) THE LINES `$(...)` USED TO EAT. Command substitution strips EVERY trailing newline,
# so the hook compared a string that had already lost them: a classifier printing two lines,
# the second empty, satisfied "exactly one line" and its `fast` was obeyed on a main push.
# Measured in round 3 with a mutated classifier — `wc -l` said 2, the hook said 0 and ran
# the fast lane. The verdict is now captured with a sentinel, so the LFs survive to be judged.
bad_verdict_case 'printf "fast\treason\n\n"' "a trailing blank line after the verdict"
bad_verdict_case 'printf "fast\treason\n\n\n\n"' "several trailing blank lines"
bad_verdict_case 'printf "fast\treason"' "a verdict with NO terminating LF"
bad_verdict_case 'printf "\nfast\treason\n"' "a blank line BEFORE the verdict"
# And the same for the class that skips work rather than doing it: an unusable `delete` must
# not become the cheap exception.
bad_verdict_case 'printf "delete\treason\n\n"' "a trailing blank line after 'delete'"

# (I-03) THE BYTE `$(...)` DOES NOT LOSE — IT DELETES. Round 4 made the trailing LFs visible
# with a sentinel, but the verdict still travelled through a command substitution, and Bash
# 5.2 strips NUL bytes on the way in (with a warning on stderr nobody reads in a hook).
# Measured in round 5: `fa<NUL>st<TAB>reason<LF>` reached the checks as `fast` and DOWNGRADED
# a main push; `de<NUL>lete` reached them as `delete` and took the no-toolchain exception. The
# class of output the verdict contract exists to reject was the one class it could not see, so
# the classifier's stdout is now captured to a FILE and judged as bytes: a NUL anywhere in it
# is the third answer, I COULD NOT LOOK.
bad_verdict_case 'printf "fa\000st\treason\n"' "a NUL INSIDE the verdict word" "NUL byte"
bad_verdict_case 'printf "de\000lete\treason\n"' "a NUL inside 'delete'" "NUL byte"
bad_verdict_case 'printf "fast\treason\n\000"' "a NUL after a valid verdict line" "NUL byte"
bad_verdict_case 'printf "\000fast\treason\n"' "a NUL before the verdict" "NUL byte"
bad_verdict_case 'printf "fast\trea\000son\n"' "a NUL inside the reason" "NUL byte"

# The classifier's real output must of course still be accepted — a contract this strict is
# worth nothing if the only implementation fails it.
e2e_run "$HOOK" "$CLASSIFY" "$FAST_FEAT"
[ "$e2e_rc" -eq 0 ]
check "(G-03) the REAL classifier satisfies the strict contract" "rc=${e2e_rc}" $?

# And a missing classifier is still the third answer.
mkdir -p "$WORK/norule/scripts/lib" || exit 1
cp "$HOOK" "$WORK/norule/pre-push" || exit 1
# The sanitiser must be present so the refusal under test is the one about the MISSING
# CLASSIFIER. Leaving it out makes the hook refuse one line earlier, about the sanitiser,
# and this assertion would then be satisfied by a refusal it never meant to provoke.
cp "$ROOT/scripts/lib/git-env.sh" "$WORK/norule/scripts/lib/git-env.sh" || exit 1
rc_missing=0
(cd "$WORK/norule" && bash ./pre-push <<<"$FAST_FEAT" >"$WORK/norule.out" 2>&1) || rc_missing=$?
[ "$rc_missing" -ne 0 ] && grep -q "prepush-refclass.sh is missing" "$WORK/norule.out"
check "a MISSING classifier refuses the push" "exit=${rc_missing}, reason named" $?

echo
echo "THE MUTEX — this split changed WHO takes it, and nothing else"

# The host mutex is NOT part of this change: it is byte-for-byte the one this repository
# already had (the hook's N-4 states its known weaknesses, and DEFERRED D-1 the work that was
# taken back out). What the split DID change is who reaches it — every lane on the box used
# to, and now only main and tags do. That is the only property asserted here, and it is
# asserted against a stub `git` (M-4): no lifecycle, no contention, no remote.
e2e_run "$HOOK" "$CLASSIFY" "$FAST_MAIN"
called_like "git commit-tree" && called_like "${E2E_LOCK_OID}:$LOCK_REF"
check "a main push TAKES the host mutex" "lock ref created" $?

called_like "git push --no-verify -q origin :$LOCK_REF"
check "...and releases it on the way out" "lock ref deleted" $?

e2e_run "$HOOK" "$CLASSIFY" "$FAST_FEAT"
! called_like "git commit-tree" && ! called_like "git push"
check "a feature push never reaches the mutex at all" "no lock traffic" $?

e2e_run "$HOOK" "$CLASSIFY" "$FAST_TAG"
called_like "${E2E_LOCK_OID}:$LOCK_REF"
check "a tag push takes it too" "lock ref created" $?

echo
echo "WIRING and DOCUMENTATION"

grep -q 'scripts/prepush-refclass.sh' "$HOOK"
check "the hook consults the classifier" "wired" $?

grep -q 'gate_class' "$HOOK"
check "the hook branches on the verdict it got" "verdict consumed" $?

# The mutex is the host-wide resource this split exists to stop every lane from taking.
# It must be unreachable unless the verdict was `full`.
awk '/gate_class" != "full"/ {seen=1} /acquire_gate_lock$/ && !seen {bad=1} END {exit bad}' "$HOOK"
check "the host mutex sits BEHIND the full-gate branch" "no lock before the fast exit" $?

# The hook must not re-read stdin after the classifier: it is consumed to EOF, and a `cat`
# round trip was what let malformed input be rewritten before the rule ever saw it (G-01).
# CODE, not commentary: the hook's own header explains the defect by name, so an
# unanchored grep would match the explanation and never the thing it forbids.
! grep -qE '^[[:space:]]*push_refs=' "$HOOK"
check "(G-01) the hook does not round-trip stdin through a variable" "classifier reads stdin" $?

# G-06: the executable policy and the written one must agree. These files are the active
# operating instructions for every lane; two of them used to describe the OLD hook (full
# gate on every push) while the hook did something else.
# export-closure: hub-only CLAUDE.md — it contains private AI-agent and maintainer workflow for the development hub, not public contributor policy.
# export-closure: hub-only sessions/status/HUB.md — it records live private session coordination and integration status, not public product behavior.
# export-closure: hub-only docs/ai-context/WORKFLOW.md — it governs private agent sessions and hub gates, not the published product.
docs_missing=""
grep -q 'prepush-refclass' "$ROOT/CONTRIBUTING.md" ||
	docs_missing="$docs_missing CONTRIBUTING.md"
if [ -f "$ROOT/CLAUDE.md" ]; then
	grep -q 'prepush-refclass' "$ROOT/CLAUDE.md" || docs_missing="$docs_missing CLAUDE.md"
else
	printf 'skip CLAUDE.md — private AI-agent and maintainer workflow is hub-only\n'
fi
if [ -f "$ROOT/sessions/status/HUB.md" ]; then
	grep -q 'prepush-refclass' "$ROOT/sessions/status/HUB.md" ||
 docs_missing="$docs_missing sessions/status/HUB.md"
else
	printf 'skip sessions/status/HUB.md — live private session coordination is hub-only\n'
fi
if [ -f "$ROOT/docs/ai-context/WORKFLOW.md" ]; then
	grep -q 'prepush-refclass' "$ROOT/docs/ai-context/WORKFLOW.md" ||
 docs_missing="$docs_missing docs/ai-context/WORKFLOW.md"
else
	printf 'skip docs/ai-context/WORKFLOW.md — private agent-session workflow is hub-only\n'
fi
[ -z "$docs_missing" ]
check "(G-06) every available operating doc names the branch-aware rule" \
	"${docs_missing:-all available docs do}" $?

stale=""
doc_forbids() { # <path> <label> <normalized phrase>
	local body
	body="$(tr '\n' ' ' <"$1" | tr -s ' ')"
	case "$body" in *"$3"*) stale="$stale $2" ;; esac
}
if [ -f "$ROOT/CLAUDE.md" ]; then
	doc_forbids "$ROOT/CLAUDE.md" CLAUDE.md \
 'build:go` + `task test`. `task lint` completo'
fi
doc_forbids "$ROOT/CONTRIBUTING.md" CONTRIBUTING.md \
	'which the `pre-push` hook also runs'
if [ -f "$ROOT/docs/ai-context/WORKFLOW.md" ]; then
	doc_forbids "$ROOT/docs/ai-context/WORKFLOW.md" docs/ai-context/WORKFLOW.md \
 'The `pre-push` hook runs all of this automatically'
fi
if [ -f "$ROOT/sessions/status/HUB.md" ]; then
	doc_forbids "$ROOT/sessions/status/HUB.md" sessions/status/HUB.md 'fast-lints (2-3 min)'
fi
[ -z "$stale" ]
check "(G-06) no doc still says the full gate runs on every push" "${stale:-none stale}" $?

timing_missing=""
if [ -f "$ROOT/CLAUDE.md" ]; then
	grep -q '4m15s' "$ROOT/CLAUDE.md" || timing_missing="$timing_missing CLAUDE.md"
fi
if [ -f "$ROOT/sessions/status/HUB.md" ]; then
	grep -q '4m15s' "$ROOT/sessions/status/HUB.md" ||
 timing_missing="$timing_missing sessions/status/HUB.md"
fi
[ -z "$timing_missing" ]
check "(G-06) available hub docs use the MEASURED 4m15s timing" \
	"${timing_missing:-all available timing docs do}" $?

# (H-02) THE THIRD COPY OF THE LIST. The hook's calls and its banner are compared above;
# this is the one contributors actually read. Round 3 found it listing five of the seven,
# missing `tokens:check` and `test:web` — the same defect as the battery's, in prose.
contrib_body="$(tr '\n' ' ' <"$ROOT/CONTRIBUTING.md" | tr -s ' ')"
contrib_want="($(printf '`%s`, ' "${HEAVY_TASKS[@]}" | sed 's/, $//'))"
case "$contrib_body" in *"$contrib_want"*) true ;; *) false ;; esac
contrib_rc=$?
check "(H-02) CONTRIBUTING.md lists exactly the declared ${#HEAVY_TASKS[@]}" "${contrib_want}" "$contrib_rc"

# (H-02b) THE FOURTH COPY: CLAUDE.md — closes half of LIMITATION M-5 above (2026-08-16, META-06).
# M-5 said it plainly and nobody had acted on it: "the exact heavy sequence is compared against
# CONTRIBUTING.md only … so a fast/heavy list edited in WORKFLOW.md, CLAUDE.md or HUB.md can stay
# wrong". And it HAD gone wrong — CLAUDE.md was missing `lint:format-ratchet` while every other
# copy carried it. Another lane had already restored the line by the time this check landed, which
# is exactly why the check is the fix and the line was not: a copy that only a human notices is a
# copy that drifts again. CLAUDE.md is the file every session is ORDERED to obey, so a stale gate
# list there is worse than in the contributor doc.
#
# Compared through a NORMALISER, not literally: CLAUDE.md marks some entries bold (`**`x`**`) and
# wraps the list across four lines. Demanding one exact byte string would have forced the prose to
# be reformatted to suit the test, and the next author would quietly undo it. Stripping `**` and
# collapsing whitespace compares the CLAIM instead of the typography.
#
# Guarded by -f: the public export ships without CLAUDE.md, and an absent hub-only document is not
# a failure — it is a different tree.
if [ -f "$ROOT/CLAUDE.md" ]; then
	claude_body="$(sed 's/\*\*//g' <"$ROOT/CLAUDE.md" | tr '\n' ' ' | tr -s ' ')"
	case "$claude_body" in *"$contrib_want"*) true ;; *) false ;; esac
	claude_rc=$?
	check "(H-02b) CLAUDE.md lists exactly the declared ${#HEAVY_TASKS[@]}" "${contrib_want}" "$claude_rc"
fi

# (H-06) THE EXCEPTION MUST BE NAMED WHERE LANES LOOK. An undocumented exception to a
# fail-closed rule is indistinguishable from a bug the next reader will "fix".
h06_missing=""
grep -q 'named deletion exception' "$HOOK" || h06_missing="$h06_missing .githooks/pre-push"
if [ -f "$ROOT/CLAUDE.md" ]; then
	grep -q 'borrado puro' "$ROOT/CLAUDE.md" || h06_missing="$h06_missing CLAUDE.md"
fi
grep -q 'pure-deletion push' "$ROOT/CONTRIBUTING.md" || h06_missing="$h06_missing CONTRIBUTING.md"
if [ -f "$ROOT/docs/ai-context/WORKFLOW.md" ]; then
	grep -q 'pure-deletion push' "$ROOT/docs/ai-context/WORKFLOW.md" ||
 h06_missing="$h06_missing docs/ai-context/WORKFLOW.md"
fi
if [ -f "$ROOT/sessions/status/HUB.md" ]; then
	grep -q 'borrado puro' "$ROOT/sessions/status/HUB.md" ||
 h06_missing="$h06_missing sessions/status/HUB.md"
fi
[ -z "$h06_missing" ]
check "(H-06) hook + available docs NAME the deletion exception" \
	"${h06_missing:-all available docs name it}" $?

# ...and none of them may still promise the old absolute rule, which is now false.
stale_h06=""
doc_forbids_h06() { # <path> <label> <normalized phrase>
	local body
	body="$(tr '\n' ' ' <"$1" | tr -s ' ')"
	case "$body" in *"$3"*) stale_h06="$stale_h06 $2" ;; esac
}
if [ -f "$ROOT/CLAUDE.md" ]; then
	doc_forbids_h06 "$ROOT/CLAUDE.md" CLAUDE.md \
 'hasta borrar una rama pide `--no-verify`'
fi
doc_forbids_h06 "$ROOT/CONTRIBUTING.md" CONTRIBUTING.md \
	'refuses every push, deletions included'
if [ -f "$ROOT/docs/ai-context/WORKFLOW.md" ]; then
	doc_forbids_h06 "$ROOT/docs/ai-context/WORKFLOW.md" docs/ai-context/WORKFLOW.md \
 'refused** (deletions included)'
fi
if [ -f "$ROOT/sessions/status/HUB.md" ]; then
	doc_forbids_h06 "$ROOT/sessions/status/HUB.md" sessions/status/HUB.md \
 'incondicional**, también para borrados'
fi
[ -z "$stale_h06" ]
check "(H-06) and none still says deletions are refused" "${stale_h06:-none stale}" $?

echo
echo "LEGACY REGRESSION — the rule this replaced disagreed, and must keep disagreeing"

# Reproduces the pre-2026-08-02 decision exactly: content or nothing, and one environment
# variable downgrades whatever is left. ROUND 2: it is compared against the verdict the
# CURRENT classifier actually returns, not against a hard-coded expectation — the previous
# version would have reported the same divergences with the hook unplugged entirely.
legacy_verdict() {
	local fast_push="$1" input="$2" has_content=0 oid
	while read -r _lref oid _rref _roid || [ -n "${_lref:-}" ]; do
 case "$oid" in *[!0]*) has_content=1 ;; esac
	done <<<"$input"
	if [ "$has_content" -eq 0 ]; then
 echo skip
	elif [ "$fast_push" = "1" ]; then
 echo fast
	else
 echo full
	fi
}

divergences=0
legacy_case() { # <label> <fast_push> <stdin>
	local got_legacy got_now
	got_legacy="$(legacy_verdict "$2" "$3")"
	got_now="$(OLIVARES_FAST_PUSH="$2" bash "$CLASSIFY" <<<"$3" | cut -f1)"
	if [ "$got_legacy" != "$got_now" ]; then
 divergences=$((divergences + 1))
 printf ' legacy disagreed on %-28s legacy=%-5s now=%s\n' "$1" "$got_legacy" "$got_now"
	fi
}

legacy_case "(a) main" 0 "refs/heads/main $SOME refs/heads/main $ZERO"
legacy_case "(b) feature" 0 "$FAST_FEAT"
legacy_case "(c) tag" 0 "$FAST_TAG"
legacy_case "(d) feature+main" 0 "$FAST_FEAT
refs/heads/main $SOME refs/heads/main $ZERO"
legacy_case "(e) deletion" 0 "(delete) $ZERO refs/heads/dead $SOME"
legacy_case "(f) gate-lock" 0 "refs/heads/l $SOME $LOCK_REF $ZERO"
legacy_case "(g) fast-push+main" 1 "refs/heads/main $SOME refs/heads/main $ZERO"
legacy_case "(h) fast-push+feature" 1 "$FAST_FEAT"
legacy_case "(k) malformed line" 0 "refs/heads/f $SOME refs/heads/f"
legacy_case "(s) status ref" 0 "$STATUS_LIVE"

[ "$divergences" -ge 3 ]
check "the legacy rule fails at least 3 of these scenarios" "${divergences} divergences" $?

echo
echo "prepush-refclass: ${pass} passed, ${fail} failed ($(($(date +%s) - BAT_T0))s wall)"
# Los `FAIL` se imprimen inline, pero quedan sepultados entre CIENTOS de lineas `ok` y dentro de
# una corrida de MAS DE CIEN lints. (Sin cifras a proposito: las dos que habia aqui —«~339» y
# «~100»— ya estaban caducadas el mismo dia que las escribi, y una de ellas la habia corregido yo
# en CLAUDE.md ese mismo turno. El argumento no necesita el numero: sigue siendo cierto con 336
# lineas o con 500. Un dato que solo ilustra se quita; uno que manda se ancla y se re-mide.)
# Una medida real desde el hook dio 337/2 y el operador NO pudo saber cuales cayeron — tres
# corridas y una hipotesis equivocada por el camino.
# Nombrarlos otra vez al final hace la ULTIMA linea autosuficiente. Medido 2026-08-25: nadie
# parsea esta salida fuera de esta bateria, asi que repetir no rompe a ningun consumidor.
if [ "$fail" -ne 0 ]; then
	printf 'prepush-refclass: los %d escenario(s) en rojo, repetidos porque arriba quedan sepultados:%s\n' \
 "$fail" "$FAILED_NAMES"
	exit 1
fi
