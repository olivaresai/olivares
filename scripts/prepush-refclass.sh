#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# prepush-refclass.sh — decide WHICH gate a `git push` deserves, from its refs alone.
#
# Reads the pre-push protocol on stdin (one "<local ref> <local oid> <remote ref>
# <remote oid>" line per ref) and prints ONE line to stdout:
#
#     <verdict><TAB><reason>            verdict ∈ full | fast | skip | delete
#
# WHY THIS IS A SEPARATE FILE. The classification is the whole policy, and inside the
# hook it could only ever be exercised by running the gate it decides — an hour of
# build+test per case. Extracted, `scripts/test-prepush-refclass.sh` proves every
# scenario in seconds, not gates (it prints its own wall time with the tally — a duration
# in prose here went stale once already, round 12 M-02), including the ones nobody can
# stage by hand (a multi-ref push whose strictest ref must win, a tag, a missing tool, a
# malformed line, a NUL byte).
#
# THE THREE FAMILIES (GATES v3, 2026-08-02). The hook used to run the same full gate for
# every ref while the written policy said otherwise and the lanes complied by hand with
# an environment variable. This file is the policy made executable:
#
#   refs/heads/main   -> full   the branch everything merges into; host mutex included.
#   refs/tags/*       -> full   a release tag is not published on quick lints. The
#                               release workflows only ever fire at tag time, so a tag
#                               is the LAST moment a heavy defect can still be caught
#                               before it is signed and published.
#   refs/heads/*      -> fast   feature lanes: fast lints, no heavy gate, no mutex.
#   refs/gate-locks/* -> skip   the hook's own mutex signalling, never content.
#   refs/session-claims/*   -> skip   a reserved session number, never content.
#   refs/integration-claims/*
#                     -> skip   the canon's anti-collision claim: a pointer, never content,
#                               and MANDATED before an integrator pushes a batch.
#   refs/status/live  -> fast   the ONE canonical status stream (D-4-status): lane status
#                               documents only, cannot alter main. EXACT ref — any other
#                               refs/status/* name takes the deny-closed default below.
#   anything else     -> full   DENY-CLOSED: an unrecognised namespace is not a licence
#                               to run less. "I do not know what this is" must cost more
#                               than a feature branch, never less.
#
# THE FOURTH VERDICT, `delete` (round 4, 2026-08-02). A push where EVERY line is a deletion
# is the one operation that carries no commits at all: there is nothing to lint, build or
# test, in any lane. It used to be folded into `skip` — and round 3 measured the price of
# that: because the hook's `task` check is unconditional and runs before a `skip` is
# honoured, `git push --delete stale-branch` on a machine without go-task was REFUSED, so a
# lane had to disable the whole hook (`--no-verify`) to tidy up a branch. Deletions now get
# their own verdict, the hook names the exception, and every other class keeps the
# unconditional toolchain requirement. A push that mixes a deletion with anything else is
# NOT a deletion push: one gate-lock ref or one commit-carrying ref and the verdict is the
# ordinary one, so the exception cannot be used as a carrier.
#
# STRICTEST WINS. `git push --all`, `git push --follow-tags` and `git push origin a b`
# all feed several lines at once. A push that touches main or a tag is a main/tag push,
# whatever else travels with it.
#
# OLIVARES_FAST_PUSH IS NO LONGER A UNIVERSAL ESCAPE. It downgrades nothing on main or
# on a tag — there it is reported as ignored, and the full gate runs. On a feature branch
# the fast lane is now the DEFAULT, so the variable is inert rather than forbidden.
#
# ---------------------------------------------------------------------------------------
# DENY-CLOSED PARSING (round 2, 2026-08-02). The first revision split each line on
# whitespace and never checked what it got: three fields, five fields, a truncated OID, a
# ref of `refs/heads/foo..bar` (which `git check-ref-format` rejects) or a CRLF line all
# scored `fast`, and a one-field line scored `skip`. The verdict a malformed line deserves
# is the strictest one — a line this script cannot read is the definition of "I do not know
# what this is". Blank lines were silently dropped, which is the same defect wearing a hat.
#
# So: EVERY non-empty stdin line must match the protocol EXACTLY or it is promoted to
# `full` and named in the reason. Only genuine end-of-input with zero lines is `skip`.
#
# WHAT "EXACTLY" MEANS, measured against real git (2.39.5) on 2026-08-02 rather than
# assumed — the fixtures live in scripts/test-prepush-refclass.sh:
#
#   git push origin main             -> "refs/heads/main <oid> refs/heads/main <oid>"
#   git push origin HEAD             -> local token "HEAD", remote "refs/heads/main"
#   git push origin HEAD~0:refs/…    -> local token "HEAD~0"   (a rev expression)
#   git push origin main^{}:refs/…   -> local token "main^{}"  (idem)
#   git push origin :refs/heads/x    -> local token "(delete)", local oid all zeros
#   a sha256 repository              -> 64-hex OIDs on BOTH sides
#   every record, single- or multi-ref -> terminated by LF, the last one included
#     (measured 2026-08-02 by hexdumping the hook's stdin for a push, a three-ref push
#      and a deletion; `od -c` showed the trailing \n in all three)
#
# The local token is therefore NOT a ref and must not be validated as one; it also decides
# nothing here. The three fields that DO decide — both OIDs and the REMOTE ref — are
# validated strictly: 40/64 lowercase hex, and a full refname under refs/.
#
# ---------------------------------------------------------------------------------------
# HOW BYTE-EXACT THIS IS, AND WHERE THE LIMIT SITS (round 4, 2026-08-02). Round 3 measured
# two real breaks in the "byte-exact, equivalent to git" claim this header used to make, and
# both are fixed below — but the claim itself is now stated with its limits rather than as
# an equivalence nobody can sustain in bash:
#
#   1. BYTES IN, BYTES JUDGED. `read` deletes NUL bytes, and a record with no terminating
#      LF was parsed as if it had one — so `refs/heads/fea<NUL>ture/x` and an unterminated
#      line both scored `fast`. The stream is now read ONCE with `read -r -d ''`: a NUL is
#      DETECTED (it terminates that read, which is how we know it was there) and the whole
#      push is deny-closed, and a non-empty stream whose last byte is not LF is likewise
#      deny-closed. Git always sends the LF (measured above); anything that does not is not
#      git and does not get the benefit of the doubt.
#   2. LOCALE, NOT BYTES. `[[:graph:]]`/`[[:cntrl:]]` are locale-dependent: under C.UTF-8
#      they classify U+0085 and U+2028 as control characters, while git forbids only ASCII
#      controls, space and DEL — so `refs/heads/a<U+0085>b`, which `git check-ref-format`
#      ACCEPTS and which pushes fine, was classified `full`. This script now forces
#      `LC_ALL=C`, which makes every class in it a BYTE class, and the field pattern admits
#      any byte that is not an ASCII control, DEL or space — git's own rule.
#
# WHAT IS CLAIMED, THEN: for refnames under `refs/`, this validator agrees with
# `git check-ref-format` on every case the battery sweeps — all 255 single-byte injections
# and the structural forms (empty component, `..`, leading dot, `.lock`, `@{`, `//`,
# trailing `/` or `.`, and the reserved `~^:?*[\`). That sweep is evidence, not a proof of
# equivalence: git's rules live in C (refs.c) and are reimplemented here in bash on purpose,
# because a gate that shells out per line is one more thing that can be missing when it
# matters. What makes the residual gap SAFE is the direction of the failure: anything this
# file does not recognise — including a destination outside `refs/`, which git fully
# qualifies before the hook ever sees it — is classified `full`, never less. A divergence
# can therefore cost a lane an hour of gate it did not owe; it cannot buy a cheaper gate.
set -uo pipefail
# Byte semantics, not the caller's locale: see limit (2) above. Every character class in
# this file — the field pattern, the ref validator, the sanitizer — is a class of BYTES.
export LC_ALL=C
export LC_CTYPE=C
export LANG=C

if [ "$#" -gt 0 ]; then
	echo "usage: $0  (reads the git pre-push stdin protocol)" >&2
	exit 2
fi

LEVEL_SKIP=1
LEVEL_FAST=2
LEVEL_FULL=3

level=$LEVEL_SKIP
detail=""
lines_seen=0
content_refs=0
deletion_refs=0

OID_RE='[0-9a-f]{40}|[0-9a-f]{64}'
# A protocol FIELD is one or more bytes that are neither an ASCII control (DEL included)
# nor whitespace — git's own character rule, and under LC_ALL=C these classes are bytes, so
# a high-bit byte (0x80..0xff, i.e. any UTF-8 refname) is a legal field character here
# exactly as it is for git. See limit (2) in the header.
FIELD_RE='[^[:cntrl:][:space:]]+'
LINE_RE="^(${FIELD_RE}) (${OID_RE}) (${FIELD_RE}) (${OID_RE})$"

# promote <level> <detail> — strictest wins; the FIRST ref of the winning class names it.
promote() {
	if [ "$1" -gt "$level" ] || { [ "$1" -eq "$level" ] && [ -z "$detail" ]; }; then
		level="$1"
		detail="$2"
	fi
}

# sanitize <text> — a malformed line must be NAMED in the reason, and the reason is printed
# to a terminal and split on TAB by the hook. Anything non-printable becomes '?', and the
# result is truncated: an attacker-supplied ref must not be able to smuggle an escape
# sequence, a tab or a newline into the hook's output.
#
# It publishes into $sanitized instead of printing: EVERY ref goes through here, and
# `$(sanitize …)` forks a subshell per line. Measured on this machine, that fork was the
# whole cost of a big push — 5000 refs took 8s with it and well under 1s without. A
# `git push --mirror` must not pay a second per hundred tags to be told it is `fast`.
sanitized=""
sanitize() {
	local s="$1" out="" ch i
	# Fast path: the overwhelmingly common ref is short and entirely printable ASCII, and
	# then sanitizing is the identity function.
	if [ "${#s}" -le 60 ] && [[ $s =~ ^[[:print:]]*$ ]]; then
		sanitized="$s"
		return 0
	fi
	for ((i = 0; i < ${#s}; i++)); do
		ch="${s:i:1}"
		case "$ch" in
		[[:print:]]) out="$out$ch" ;;
		*) out="$out?" ;;
		esac
		if [ "${#out}" -ge 60 ]; then
			out="${out}..."
			break
		fi
	done
	sanitized="$out"
}

# valid_remote_ref <ref> — the rules `git check-ref-format` enforces, for the subset that
# can legitimately appear as a push destination: a full refname of at least two components.
# Pure bash on purpose: this runs inside the hook's own stdin loop, and a gate that shells
# out per line would be one more thing that can be missing when it matters. The battery
# sweeps it against git itself (255 single-byte injections + the structural forms); the
# header states what that evidence does and does not license us to claim.
valid_remote_ref() {
	local ref="$1" rest comp
	case "$ref" in
	refs/*) ;;
	*) return 1 ;;
	esac
	# ASCII control characters, space, DEL, and the characters git reserves. Under LC_ALL=C
	# (forced at the top of this file) [[:cntrl:]] is exactly bytes 0x00-0x1f plus 0x7f —
	# ASCII, as git defines it, and NOT the locale's idea of a control character.
	case "$ref" in
	*[[:cntrl:]]* | *' '* | *'~'* | *'^'* | *':'* | *'?'* | *'*'* | *'['* | *'\'* | *$'\177'*)
		return 1
		;;
	esac
	# Sequences git forbids anywhere in the name.
	case "$ref" in
	*..* | *@\{* | *//* | */ | *. | @) return 1 ;;
	esac
	rest="$ref"
	while [ -n "$rest" ]; do
		comp="${rest%%/*}"
		if [ "$comp" = "$rest" ]; then
			rest=""
		else
			rest="${rest#*/}"
		fi
		[ -n "$comp" ] || return 1
		case "$comp" in
		.* | *.lock) return 1 ;;
		esac
	done
	return 0
}

# deny_stream <reason> — the STREAM itself is not the protocol, so no line in it can be
# trusted enough to classify. Strictest, named, and no parsing of what follows.
deny_stream() {
	printf 'full\tFULL gate (%s — deny-closed, strictest), 0 ref(s) with commits\n' "$1"
	exit 0
}

# --- READ THE STREAM ONCE, BYTE-EXACTLY -------------------------------------------------
# `read -r -d ''` stops at the first NUL, so a successful read is the PROOF that stdin
# carried one — the only way to notice a byte bash would otherwise delete on the way in. On
# a NUL-free stream it returns non-zero at EOF with the bytes intact, trailing LF included:
# the ordinary `while read` loop it replaces could not tell "line\n" from "line".
raw=""
if IFS= read -r -d '' raw; then
	deny_stream "NUL byte in the pre-push protocol"
fi
if [ -n "$raw" ] && [ "${raw: -1}" != $'\n' ]; then
	deny_stream "last protocol record is not LF-terminated"
fi

# Strip the ONE trailing LF that terminates the last record and read the rest line by line.
# The here-string appends exactly one LF, which is why the strip comes first: without it a
# `git push --all` would grow a phantom blank line, and a blank line is `full` here.
#
# The obvious alternative — splitting the buffer with ${body%%…}/${body#…} — is QUADRATIC:
# measured on this machine at 3s for 1000 refs and 51s for 5000, because every iteration
# copies the remaining buffer. `git push --mirror` on a repo with thousands of tags is not
# a hypothesis, and a classifier that takes a minute to say `fast` would be turned off.
# This loop runs in the CURRENT shell (a here-string is a redirection, not a pipe), so the
# counters it increments are the ones printed at the end.
line=""
if [ -n "$raw" ]; then
	while IFS= read -r line; do
		lines_seen=$((lines_seen + 1))

		# DENY-CLOSED, step 1: the shape. A line that does not match the protocol exactly —
		# wrong field count, a truncated or upper-case OID, a stray CR, a blank line — is not
		# a line this script may reason about, so it costs the strictest gate.
		if [[ ! $line =~ $LINE_RE ]]; then
			if [ -z "${line//[[:space:]]/}" ]; then
				promote "$LEVEL_FULL" "blank line in the pre-push protocol — deny-closed, strictest"
			else
				sanitize "$line"
				promote "$LEVEL_FULL" \
					"malformed pre-push line '${sanitized}' — deny-closed, strictest"
			fi
			continue
		fi
		local_token="${BASH_REMATCH[1]}"
		local_oid="${BASH_REMATCH[2]}"
		remote_ref="${BASH_REMATCH[3]}"

		# DENY-CLOSED, step 2: the field that decides. `git check-ref-format` rejects
		# `refs/heads/` and `refs/heads/foo..bar`; so must the rule that reads them.
		if ! valid_remote_ref "$remote_ref"; then
			sanitize "$remote_ref"
			promote "$LEVEL_FULL" \
				"invalid remote ref '${sanitized}' — deny-closed, strictest"
			continue
		fi

		# Every ref this rule NAMES back to the hook goes through the same sanitizer as a
		# malformed one: a valid refname may still carry high-bit bytes (git allows them, and
		# so does this file since round 4), and the reason is printed to a terminal.
		sanitize "$remote_ref"
		safe_ref="$sanitized"

		# A deletion has an all-zero local oid (length-agnostic: sha1 and sha256 both).
		# There are no commits to test, so it contributes nothing to the gate.
		#
		# AND THE LOCAL TOKEN MUST AGREE (round 5, I-05). The token used to be discarded, so
		# ANY line with a zero local OID was counted as a deletion — `refs/heads/main <zeros>
		# refs/heads/main <nonzero>` scored `delete`, and the hook's named exception then
		# exited 0 with no toolchain and no gate. Git never emits that tuple: a zero local OID
		# IS the order to delete, and git writes one of exactly two tokens beside it —
		# `(delete)` for `git push :ref` / `--delete`, or the literal all-zero OID for an
		# explicit `0000…:refs/heads/x` refspec (both measured against git 2.39.5 in round 5).
		# Everything else is a shape this protocol does not have, and the strictest verdict is
		# what an unrecognised shape costs everywhere else in this file.
		case "$local_oid" in
		*[!0]*) ;;
		*)
			if [ "$local_token" != "(delete)" ] && [ "$local_token" != "$local_oid" ]; then
				sanitize "$local_token"
				promote "$LEVEL_FULL" \
					"zero OID with local token '${sanitized}' — not a deletion git emits, strictest"
				continue
			fi
			deletion_refs=$((deletion_refs + 1))
			promote "$LEVEL_SKIP" "deletion of ${safe_ref}"
			continue
			;;
		esac
		content_refs=$((content_refs + 1))

		case "$remote_ref" in
		refs/gate-locks/*)
			promote "$LEVEL_SKIP" "gate-lock ref ${safe_ref} — signalling, never content"
			;;
		refs/integration-claims/*)
			# THE SAME CLASS AS THE MUTEX ABOVE, and it is here because the canon MANDATES
			# publishing one of these before an integrator pushes a batch — it is a step of
			# the sanctioned workflow, not an optional convenience.
			#
			# It used to fall through to the `*` arm and score FULL ("unrecognised ref"),
			# which measured, on 2026-08-09, as a two-and-a-half-hour gate to publish a
			# POINTER. The consequence is the one the deletion exception already exists to
			# prevent: the only way to follow the anti-collision protocol was
			# `git push --no-verify`, i.e. the rule taught every lane to disable the entire
			# hook on a step that gates nothing.
			#
			# It carries no content of its own. The OID it names reached the remote through
			# a ref that WAS gated — a branch, or main — or it is not reachable at all and
			# naming it publishes nothing. There is nothing here to lint, build or test that
			# its own push did not already cover.
			promote "$LEVEL_SKIP" "integration-claim ref ${safe_ref} — signalling, never content"
			;;
		refs/session-claims/*)
			# THE SAME CLASS AGAIN, for the reservation protocol that came out of four session
			# number collisions on 2026-08-19/20. A lane that has picked a number but has
			# nothing pushable yet publishes `refs/session-claims/S<N>-<lane>` and the number
			# stops being an announcement in a mailbox and becomes something countable.
			#
			# ⛔ IT IS DELIBERATELY **NOT** `refs/heads/claim/*`, and that corrects my own
			#    proposal rather than someone else's. The integrator adopted the protocol
			#    within ten minutes and reasonably read "a claim ref" as a branch, publishing
			#    refs/heads/claim. Under refs/heads a skip verdict would let
			#    ARBITRARY CONTENT through the branch lane, and this classifier cannot check
			#    that a claim carries none: it is PURE BASH ON PURPOSE (see valid_remote_ref)
			#    because "a gate that shells out per line would be one more thing that can be
			#    missing when it matters", so there is no `merge-base --is-ancestor` available
			#    here to enforce the emptiness the arm above merely asserts.
			#
			#    Outside refs/heads the question does not arise: nothing is ever built, merged
			#    or released from this namespace, so there is nothing for content to reach.
			#    The safety comes from WHERE it lives, not from a check we would have to
			#    remember to run.
			#
			# The cost of getting the namespace wrong is the one the integration-claim arm
			# already measured: falling through to `*` scores FULL, a reservation costs two and
			# a half hours, and the only way to reserve is `--no-verify` — teaching every lane
			# to disable the whole hook on a step that gates nothing.
			promote "$LEVEL_SKIP" "session-claim ref ${safe_ref} — a reserved number, never content"
			;;
		refs/archive/*)
			# PRESERVATION, NOT DELIVERY — and unlike the two arms above this one DOES carry
			# content, so the reason has to be different and it is: nothing is ever built,
			# merged, released or deployed from refs/archive/*. It is write-only. A ref lands
			# here precisely because the work behind it was ABANDONED, and abandoned work is
			# usually stale against today's main by weeks — the oldest branches archived on
			# 2026-08-10 were five weeks old and would not compile.
			#
			# So a full gate on this namespace does not qualify anything, it makes preservation
			# IMPOSSIBLE: the only ways past a red gate are to delete the work or to push with
			# --no-verify. Measured the same day — 56 branches were archived and every one of
			# them scored FULL as an "unrecognised ref", i.e. 2h30 per rescued branch.
			#
			# That is the third instance of one pattern, after the mutex and the claim: a
			# namespace that gates nothing scoring strictest, and the rule thereby teaching
			# every lane to disable the whole hook. Preserving work must never cost more than
			# losing it.
			#
			# ⚠ THE LIMIT, NAMED: this is an exemption by TRUST, not by proof. Ungated content
			# does reach the server here, and the only thing keeping that safe is that nothing
			# consumes the namespace. If anything ever merges FROM refs/archive/*, this arm is
			# wrong and must go.
			promote "$LEVEL_SKIP" "archive ref ${safe_ref} — write-only preservation, never a build or merge source"
			;;
		refs/status/live)
			# ONE exact ref, not a namespace (D-4-status, 2026-08-03,
			# an internal design note (not shipped)). The canonical status
			# stream carries lane status documents only and cannot alter main, so it
			# earns the fast lints — lint:export included. Every OTHER refs/status/*
			# name deliberately falls through to the deny-closed default below: the
			# exact ref is the capability.
			promote "$LEVEL_FAST" "canonical status stream ${safe_ref}"
			;;
		refs/heads/main)
			promote "$LEVEL_FULL" "refs/heads/main is being pushed"
			;;
		refs/tags/*)
			promote "$LEVEL_FULL" "release tag ${safe_ref} is being pushed"
			;;
		refs/heads/*)
			promote "$LEVEL_FAST" "feature ref ${safe_ref}"
			;;
		*)
			promote "$LEVEL_FULL" "unrecognised ref '${safe_ref}' — classified strictest"
			;;
		esac
	done <<<"${raw%$'\n'}"
fi

# TRUE end-of-input is the ONLY silent skip: git ran the hook with nothing to push. One
# blank line is not that (it took the malformed arm above and is already `full`).
if [ "$lines_seen" -eq 0 ]; then
	printf 'skip\tno ref lines on stdin — nothing to gate\n'
	exit 0
fi

[ -n "$detail" ] || detail="no ref carries new commits"

case "$level" in
"$LEVEL_FULL") verdict=full ;;
"$LEVEL_FAST") verdict=fast ;;
*)
	# `delete` is the SKIP level narrowed to the one push that carries nothing at all: EVERY
	# line was a deletion. One gate-lock ref, one commit-carrying ref or one line this rule
	# could not read and the count no longer matches, so the ordinary verdict stands — the
	# named exception in the hook cannot be used as a carrier for anything else.
	if [ "$deletion_refs" -eq "$lines_seen" ]; then
		verdict=delete
	else
		verdict=skip
	fi
	;;
esac

# The escape hatch, reported by the same authority that decides the class, so the hook
# never re-implements the rule while printing it.
escape=""
if [ "${OLIVARES_FAST_PUSH:-0}" = "1" ]; then
	case "$verdict" in
	full) escape=" — OLIVARES_FAST_PUSH=1 IGNORED here: main and tags always take the full gate" ;;
	fast) escape=" — OLIVARES_FAST_PUSH=1 is inert: the fast lane is already the default here" ;;
	esac
fi

# ONE line, ONE verdict, a non-empty reason and no TAB inside it: the hook refuses
# anything else, so this contract is enforced from both ends.
case "$verdict" in
full)
	printf 'full\tFULL gate (%s), %d ref(s) with commits%s\n' \
		"$detail" "$content_refs" "$escape"
	;;
fast)
	printf 'fast\tFAST lints only (%s), %d ref(s) with commits%s\n' \
		"$detail" "$content_refs" "$escape"
	;;
delete)
	printf 'delete\tdeletion-only push (%s), %d ref(s) deleted, no commits to gate\n' \
		"$detail" "$deletion_refs"
	;;
skip)
	printf 'skip\tnothing to gate (%s)\n' "$detail"
	;;
esac
