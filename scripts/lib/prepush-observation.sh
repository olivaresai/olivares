# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# prepush-observation.sh — GATE-O1. The shell half of the opt-in observation of the
# feature hook's actual top-level `task` occurrences. Sourced by `.githooks/pre-push`
# ONLY after that hook's own support inventory has passed, and only for the actual
# `fast` ref class.
#
# ⛔ THIS FILE OBSERVES. IT DOES NOT DECIDE, SKIP, CACHE, SCHEDULE OR RETRY.
# Every literal `task ...` line of the hook still executes exactly once, in the
# foreground, with its own arguments, environment prefix, subshell, descriptors, cwd
# and umask, and the status the hook sees is the status the task returned. If anything
# in here fails, the failure is an OBSERVATION failure: the wrapper still runs the task
# and still returns the task's own status. There is no path from this file to a gate
# refusal, and that is the whole point of it.
#
# ── WHY A FUNCTION NAMED `task`, AND WHY IT IS THE SMALLEST THING THAT WORKS ─────────
# The hook types 251 literal calls whose exact order, cut marker and per-call shape are
# themselves gated (scripts/test-prepush-refclass.sh compares the observed sequence
# against a declared list; check-taskfile-graph.sh and check-hook-leg-wiring.sh key on
# the same literal forms). Rewriting those lines into a dispatcher would change the very
# text three gates read. A shell function named `task` leaves all 251 lines untouched
# and is inherited by the hook's existing subshells, so `( unset …; task lint:commit-identity )`
# is observed without being rewritten.
#
# ── THE FOUR THINGS THIS FILE GETS WRONG IF IT IS CARELESS, ALL MEASURED ─────────────
#
#  1. `command task "$@"` DOES NOT PRESERVE LOOKUP WHEN LOOKUP IS A FUNCTION. Measured
#     on bash 5.2.15: an inherited exported `task` function returning 7 is bypassed by
#     `command`, the external file runs, returns 0, and the hook's suffix executes — a
#     different push. The hook therefore DECLINES before this file is even sourced
#     unless `type -t task` is `file`. Nothing here adapts, emulates or replaces an
#     inherited function; an existing exported function keeps its original state.
#
#  2. A `local` WHOSE NAME IS ALREADY EXPORTED CHANGES THE CHILD'S ENVIRONMENT.
#     Measured: with `__olivares_gate_obs_rc=preexisting` exported, `local
#     __olivares_gate_obs_rc=0` makes the child see `0`. A naming convention alone does
#     not detect that; the hook sweeps the WHOLE reserved prefix — variables of every
#     attribute (plain, exported, readonly, indexed and associative arrays) and
#     functions — and declines on any hit, before this file is sourced.
#
#  3. UNDER `set -e`, A FAILING RECORDER KILLS THE HOOK BEFORE `return`. Saving the
#     child's status is not enough if a later observation command exits nonzero outside
#     a condition. EVERY recorder-side operation below is inside an `if`, a `case` or a
#     `|| :`, and the wrapper's last statement is `return` with the saved status.
#
#  4. A RECORDER THAT READS INHERITED STDIN STEALS THE CHILD'S INPUT. Measured in the
#     construction review: the child received `line-two` instead of `line-one`. Every
#     recorder invocation here redirects stdin from /dev/null, captures a bounded
#     protocol line, discards recorder stderr, and keeps no descriptor open across the
#     task child.
#
# ── THE RESERVED NAMESPACE, ENUMERATED (the hook checks all of it before sourcing) ───
#   functions: __olivares_gate_obs_begin  __olivares_gate_obs_start
#              __olivares_gate_obs_end    __olivares_gate_obs_diag
#              __olivares_gate_obs_token_ok  __olivares_gate_obs_oid_ok
#              __olivares_gate_obs_capture   __olivares_gate_obs_bytes_ok
#              __olivares_gate_obs_locale_ready
#   variables: __olivares_gate_obs_dir        __olivares_gate_obs_attempt
#              __olivares_gate_obs_recorder   __olivares_gate_obs_python
#              __olivares_gate_obs_diagnosed  __olivares_gate_obs_rc
#              __olivares_gate_obs_occ        __olivares_gate_obs_line
#              __olivares_gate_obs_name       __olivares_gate_obs_extra
#              __olivares_gate_obs_out        __olivares_gate_obs_commit
#              __olivares_gate_obs_tree       __olivares_gate_obs_tracked
#   plus the function `task` itself, which is installed EXPLICITLY UNEXPORTED and whose
#   unexported attribute is verified after installation.
#
# ── WHAT THIS IS NOT ────────────────────────────────────────────────────────────────
# It is compatibility with supported bash state, NOT a security boundary. A hostile
# interpreter or startup script that replaces bash's own primitives cannot be validated
# from inside itself, and nothing here claims otherwise.

# Bounded, locale-independent token validation. Explicit byte enumerations rather than
# `[a-z]`, because bracket ranges follow the locale and this repository has already been
# bitten by that (the classifier's byte sweep exists for the same reason). A token is a
# NAME, never a path: `/`, `.` and a leading `-` are unrepresentable in it by grammar.
__olivares_gate_obs_token_ok() {
	case "${1:-}" in
	*[!abcdefghijklmnopqrstuvwxyz0123456789_-]*) return 1 ;;
	[abcdefghijklmnopqrstuvwxyz]*) ;;
	*) return 1 ;;
	esac
	[ "${#1}" -ge 8 ] && [ "${#1}" -le 80 ]
}

# ── BYTE MODE, ESTABLISHED RATHER THAN ASSUMED ──────────────────────────────────────
#
# ⛔ THE BOUND IS IN BYTES, AND REVISION 2 ONLY ASSUMED IT. The reader bounds `read -n`
# to 129, and `-n` counts CHARACTERS. Revision 2 wrote `local LC_ALL=C` and continued
# whatever happened. Independent review showed both halves of that being wrong on the
# actual adapter, and revision3 probe-01 reproduced them:
#
#   · with an inherited `readonly LC_ALL=C.UTF-8`, the assignment FAILS, bash prints
#     `local: LC_ALL: readonly variable` — leaking this file's absolute path into the
#     push output — and the capture carried on in the caller's locale; and
#   · in that locale the same six-argument read stored 129 CHARACTERS = 258 BYTES, so
#     the 129-byte claim was simply false for a state the bootstrap admitted.
#
# So byte mode is now MEASURED, not declared. A two-byte UTF-8 sequence has length two
# when the shell counts bytes and length one when it counts characters; nothing external
# is consulted and no locale database has to exist.
__olivares_gate_obs_bytes_ok() {
	local __olivares_gate_obs_probe=$'\303\251'
	[ "${#__olivares_gate_obs_probe}" = 2 ]
}

# Is byte mode reachable WITHOUT changing anything the caller owns?
#
# Three states, and the middle one is why this is not a plain assignment:
#   · already counting bytes (LC_ALL=C, LC_ALL unset in the C locale, POSIX): accepted
#     as it stands, and nothing is assigned — an inherited `readonly LC_ALL=C` is a
#     perfectly supported state and revision 2 emitted a diagnostic for it anyway;
#   · not counting bytes but `LC_ALL` is assignable: a function-local shadows it for
#     the duration of the call and bash restores it on return, so the caller's locale
#     and the task's locale are untouched;
#   · not counting bytes and `LC_ALL` is readonly: UNSUPPORTED. Observation declines.
#     The diagnostic is suppressed because bash's own message carries this file's path,
#     and a bounded fixed code is what this contract allows on stderr.
#
# ⚠ `LC_ALL` IS NOT AN OBSERVER NAME, and it is the one local in this file outside the
# reserved prefix. It cannot be prefixed: it is the POSIX variable whose real name the
# shell reads to decide how it counts. It is declared only as a FUNCTION LOCAL, only
# after the check above proves it is needed, and it is never exported, never assigned
# globally and never left set when the wrapper runs the task.
__olivares_gate_obs_locale_ready() {
	__olivares_gate_obs_bytes_ok && return 0
	local LC_ALL=C 2>/dev/null || return 1
	__olivares_gate_obs_bytes_ok
}

# An object id, or nothing. Same explicit-byte reasoning as the token check above, and
# the same refusal to guess: a stub `git`, a shallow fixture or a repository this hook
# cannot read all produce "unavailable", which the record then states as such.
__olivares_gate_obs_oid_ok() {
	case "${1:-}" in
	"" | *[!0123456789abcdef]*) return 1 ;;
	esac
	[ "${#1}" = 40 ] || [ "${#1}" = 64 ]
}

# ONE bounded, nonsecret line per shell, and only the first time. It names an observation
# exit code and nothing else: no path, no argv, no environment, no errno text carrying a
# user value. 251 copies of a diagnostic would be its own defect.
__olivares_gate_obs_diag() {
	if [ "${__olivares_gate_obs_diagnosed:-0}" = "0" ]; then
		__olivares_gate_obs_diagnosed=1
		printf 'pre-push: gate observation unavailable (obs-rc=%s). Tasks, their statuses and the gate verdict are unaffected.\n' \
			"${1:-0}" >&2
	fi
	return 0
}

# ── THE PROTOCOL READER, AND IT IS BOUNDED BEFORE BASH STORES ANYTHING ──────────────
#
# ⛔ COMMAND SUBSTITUTION CANNOT READ THIS PROTOCOL, AND THE FIRST REVISION USED IT.
# `$(recorder)` was returned by independent review as O1-F2, and the four losses are
# measured, not argued (revision2 probes/bounded-read.out, bash 5.2.15):
#
#   · it allocates the recorder's ENTIRE stdout before any validation runs — a 5001-byte
#     reply became a 5000-character bash string;
#   · it DELETES NUL bytes (`abc<NUL>123` arrived as `abc123`), so a token followed by a
#     NUL was indistinguishable from the token;
#   · it strips EVERY trailing newline, so `tok<LF><LF>` and `tok<LF>` were the same
#     value even though the contract admits exactly one line; and
#   · a reply with no terminating newline at all became the same value again.
#
# The remedy is the idiom this hook already uses for its own classifier verdict, and for
# exactly the same reason: the reply goes to a FILE and is read back as BYTES. One
# bounded read then decides every malformed case at once (probes/bounded-read2.out):
#
#     IFS= read -r -d '' -n 129 raw <"$reply"
#
#   · returning 0 means the read stopped on a NUL delimiter OR on the 129-byte bound.
#     Both are refusals, so a hostile or broken reply of any size costs at most 129
#     bytes of bash memory — the 5001-byte case stores 129 and refuses.
#   · returning nonzero means EOF with no NUL and fewer than 129 bytes, so `raw` holds
#     the exact bytes the recorder wrote, newlines and carriage returns included.
#
# `local LC_ALL=C` makes `-n` count BYTES rather than characters, and it is a function
# local: the task child runs in the wrapper, after this function has returned, so its
# locale is untouched (measured in probes/locale-and-cost.out).
#
# The recorder's OWN exit status is preserved because a plain `>` redirection is not a
# pipeline and not a substitution; when the recorder failed, its status is what the
# caller sees, and the reader's verdict is reported only when the recorder succeeded.
#
# Reader-side statuses are 90..93 so a single diagnostic can name which of the four
# refusals happened without printing any of the bytes it refused.
__olivares_gate_obs_capture() { # <token|nodata> <recorder argv...>
	local __olivares_gate_obs_shape="${1:-token}"
	shift || return 90
	local __olivares_gate_obs_reply=""
	local __olivares_gate_obs_out=""
	local __olivares_gate_obs_rc=0
	local __olivares_gate_obs_rd=0

	__olivares_gate_obs_reply="$(command mktemp "${TMPDIR:-/tmp}/olivares-gate-obs.XXXXXX" 2>/dev/null)" ||
		__olivares_gate_obs_reply=""
	[ -n "$__olivares_gate_obs_reply" ] || return 90

	"$__olivares_gate_obs_python" "$__olivares_gate_obs_recorder" "$@" \
		</dev/null >"$__olivares_gate_obs_reply" 2>/dev/null || __olivares_gate_obs_rc=$?

	# Byte mode for the read below, established without touching the caller's locale
	# and without letting bash print this file's path if it cannot be established.
	if ! __olivares_gate_obs_bytes_ok; then
		if ! local LC_ALL=C 2>/dev/null || ! __olivares_gate_obs_bytes_ok; then
			command rm -f -- "$__olivares_gate_obs_reply" 2>/dev/null || :
			return 94
		fi
	fi
	IFS= read -r -d '' -n 129 __olivares_gate_obs_out <"$__olivares_gate_obs_reply" ||
		__olivares_gate_obs_rd=$?
	command rm -f -- "$__olivares_gate_obs_reply" 2>/dev/null || :

	# The recorder's own failure outranks the reader's opinion of its output.
	[ "$__olivares_gate_obs_rc" = 0 ] || return "$__olivares_gate_obs_rc"
	# NUL byte, or more than 128 bytes: refused with 129 bytes read, never more.
	# Written as a full `if` and not `[ … ] && return`, because the false branch of an
	# `&&` list is itself a nonzero statement and would hand errexit the shell in the
	# ordinary case. Every observation statement in this file is inside a condition.
	if [ "$__olivares_gate_obs_rd" = 0 ]; then
		return 91
	fi

	if [ "$__olivares_gate_obs_shape" = nodata ]; then
		# `end` completes a record and returns NO data. A byte here is a protocol
		# violation even if it would otherwise look like a valid token.
		[ -z "$__olivares_gate_obs_out" ] || return 92
		return 0
	fi

	# Exactly one line: one terminating LF and no other, at most 128 bytes in total.
	case "$__olivares_gate_obs_out" in
	*$'\n') ;;
	*) return 93 ;;
	esac
	local __olivares_gate_obs_tok="${__olivares_gate_obs_out%$'\n'}"
	case "$__olivares_gate_obs_tok" in
	*$'\n'*) return 93 ;;
	esac
	[ "${#__olivares_gate_obs_out}" -le 128 ] || return 93
	__olivares_gate_obs_token_ok "$__olivares_gate_obs_tok" || return 93
	printf '%s\n' "$__olivares_gate_obs_tok"
	return 0
}

# Start one occurrence. Prints the validated occurrence token on stdout and returns 0;
# on any failure prints nothing and returns the recorder's own observation status.
# Called from a command substitution, so everything it sets is local to that subshell —
# which is exactly why the ordinal comes from the RECORDER and not from a shell counter.
__olivares_gate_obs_start() {
	local __olivares_gate_obs_line="${1:-0}"
	shift || return 2
	local __olivares_gate_obs_name="${1:-}"
	local __olivares_gate_obs_extra=0
	[ "$#" -gt 0 ] || return 2
	__olivares_gate_obs_extra=$(($# - 1))
	[ -n "$__olivares_gate_obs_name" ] || return 2
	__olivares_gate_obs_capture token start \
		"$__olivares_gate_obs_dir" "$__olivares_gate_obs_attempt" \
		"$__olivares_gate_obs_name" "$__olivares_gate_obs_extra" \
		"$__olivares_gate_obs_line"
}

# Complete one occurrence. The reply must carry NO data at all, and that is checked
# rather than discarded: a completion that answers with bytes is not this protocol.
__olivares_gate_obs_end() {
	__olivares_gate_obs_capture nodata end \
		"$__olivares_gate_obs_dir" "$__olivares_gate_obs_attempt" \
		"${1:-}" "${2:-0}"
}

# Initialise the attempt and, only if that succeeds, install the wrapper.
#
# Returns 0 when the wrapper is installed, nonzero when observation declines. A decline
# leaves the hook exactly as it found it: no `task` function, no shell option changed,
# no existing value overwritten.
__olivares_gate_obs_begin() {
	local __olivares_gate_obs_out=""
	local __olivares_gate_obs_commit="unavailable"
	local __olivares_gate_obs_tree="unavailable"
	local __olivares_gate_obs_tracked="unavailable"

	[ -n "${__olivares_gate_obs_dir:-}" ] || return 2

	# The interpreter must be an ORDINARY EXTERNAL COMMAND, resolved the same way the
	# hook resolves everything else. Its version is enforced by the recorder itself, in
	# its own process, without installing anything.
	__olivares_gate_obs_python="$(type -P python3 2>/dev/null)" || __olivares_gate_obs_python=""
	if [ -z "$__olivares_gate_obs_python" ] || [ ! -x "$__olivares_gate_obs_python" ]; then
		__olivares_gate_obs_diag "no-python3"
		return 2
	fi
	__olivares_gate_obs_recorder="scripts/prepush-observation.py"
	if [ ! -r "$__olivares_gate_obs_recorder" ]; then
		__olivares_gate_obs_diag "no-recorder"
		return 2
	fi
	# The bounded protocol reader needs a private temporary file per reply. Both tools
	# are invoked through the `command` builtin the hook already verified, so a shell
	# function cannot stand in for either; if the environment lacks them, observation
	# declines rather than falling back to an unbounded read.
	if [ -z "$(type -P mktemp 2>/dev/null)" ] || [ -z "$(type -P rm 2>/dev/null)" ]; then
		__olivares_gate_obs_diag "no-mktemp"
		return 2
	fi
	# Decline ONCE, here, rather than 251 times inside the wrapper: if the shell cannot
	# be put into byte mode, the reader's bound is not the bound this contract states
	# and observation has no business starting.
	if ! __olivares_gate_obs_locale_ready; then
		__olivares_gate_obs_diag "locale"
		return 2
	fi

	# SOURCE CONTEXT, AND IT IS CONTEXT. HEAD and its tree describe the repository at
	# initialisation. They are NOT the bytes of the hook snapshot that is executing:
	# that snapshot was unlinked before this line ran, and the record says so rather
	# than letting a reader infer whole-sequence source verification from a commit id.
	#
	# GIT_OPTIONAL_LOCKS=0 on the dirtiness probe so an observer cannot make the index
	# refresh write while a gate is comparing the tree against its own snapshot.
	if __olivares_gate_obs_out="$(git rev-parse --verify --quiet HEAD 2>/dev/null)" &&
		__olivares_gate_obs_oid_ok "$__olivares_gate_obs_out"; then
		__olivares_gate_obs_commit="$__olivares_gate_obs_out"
	fi
	if __olivares_gate_obs_out="$(git rev-parse --verify --quiet 'HEAD^{tree}' 2>/dev/null)" &&
		__olivares_gate_obs_oid_ok "$__olivares_gate_obs_out"; then
		__olivares_gate_obs_tree="$__olivares_gate_obs_out"
	fi
	if GIT_OPTIONAL_LOCKS=0 git diff --quiet HEAD -- >/dev/null 2>&1; then
		__olivares_gate_obs_tracked="clean"
	else
		case "$?" in
		1) __olivares_gate_obs_tracked="differs" ;;
		*) __olivares_gate_obs_tracked="unavailable" ;;
		esac
	fi

	__olivares_gate_obs_out="$(
		__olivares_gate_obs_capture token init \
			"$__olivares_gate_obs_dir" fast \
			"$__olivares_gate_obs_commit" "$__olivares_gate_obs_tree" \
			"$__olivares_gate_obs_tracked" \
			"${BASH_VERSINFO[0]:-0}.${BASH_VERSINFO[1]:-0}.${BASH_VERSINFO[2]:-0}"
	)" || {
		__olivares_gate_obs_diag "$?"
		return 2
	}
	# The capture already validated the grammar, the single line and the byte bound;
	# this repeats the token test on the value this function is about to keep, because
	# the command substitution above is the one place where a trailing newline is
	# stripped again and a second opinion costs nothing.
	if ! __olivares_gate_obs_token_ok "$__olivares_gate_obs_out"; then
		__olivares_gate_obs_diag "bad-token"
		return 2
	fi
	__olivares_gate_obs_attempt="$__olivares_gate_obs_out"

	# ── THE WRAPPER ──────────────────────────────────────────────────────────────
	# One foreground `command task "$@"` through an explicit conditional, the exact
	# saved status returned last, and every observation call guarded. No pipeline, no
	# tee, no pseudo-terminal, no timeout, no trap, no descriptor kept open across it.
	task() {
		local __olivares_gate_obs_line="${BASH_LINENO[0]}"
		local __olivares_gate_obs_occ=""
		local __olivares_gate_obs_rc=0
		if __olivares_gate_obs_occ="$(__olivares_gate_obs_start "$__olivares_gate_obs_line" "$@")"; then
			:
		else
			__olivares_gate_obs_diag "$?"
			__olivares_gate_obs_occ=""
		fi
		if ! __olivares_gate_obs_token_ok "${__olivares_gate_obs_occ:-}"; then
			__olivares_gate_obs_occ=""
		fi
		if command task "$@"; then
			__olivares_gate_obs_rc=0
		else
			__olivares_gate_obs_rc=$?
		fi
		if [ -n "$__olivares_gate_obs_occ" ]; then
			__olivares_gate_obs_end "$__olivares_gate_obs_occ" "$__olivares_gate_obs_rc" ||
				__olivares_gate_obs_diag "$?"
		fi
		return "$__olivares_gate_obs_rc"
	}

	# EXPLICITLY UNEXPORTED, AND VERIFIED. Redefining an exported function KEEPS its
	# export attribute, so omitting `export -f` is not the same as being unexported.
	# `declare -pF` prints `declare -fx task` when it is exported and `declare -f task`
	# when it is not; anything else is a state this file does not understand, and not
	# understanding it is a reason to withdraw rather than to proceed.
	case "$(declare -pF task 2>/dev/null)" in
	"declare -f task") ;;
	*)
		# It never assigns to __olivares_gate_obs_dir: that name may be an injected READONLY
		# the hook's guard already rejected, and an observer that kills the hook while
		# withdrawing is worse than one that never started.
		unset -f task 2>/dev/null || :
		__olivares_gate_obs_attempt=""
		__olivares_gate_obs_diag "exported-wrapper"
		return 2
		;;
	esac
	return 0
}
