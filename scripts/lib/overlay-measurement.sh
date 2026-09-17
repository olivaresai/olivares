# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# overlay-measurement.sh — LT1. The shell half of the measurement Module's Interface.
# Sourced by the seven registered readers and by their batteries; both cross this seam.
#
# ⛔ WHAT IT DECOUPLES, AND WHY THE OLD SHAPE HAD TO GO. Every one of the seven readers
# compared a LIVE `origin/main` against the SHA its historical record pinned, so an
# Enterprise main advance that changed nothing the readers judge still turned all seven
# red and forced ten versioned metadata replacements. Reproduced with an actual same-tree
# child (identical tree object, `merge-base --is-ancestor` YES): seven reds over a tree
# byte-identical to the one they had just passed.
#
# The record stays IMMUTABLE and becomes what it always was — an observation about its own
# baseline. This Module validates it against the OLD objects it names, makes a NEW complete
# observation on the sealed current source, and hands the adapter the captured 40-hex so the
# adapter's own semantic predicates judge the CURRENT bytes. The predicates are not
# reimplemented here: the readers keep them.
#
# ── THE INTERFACE ────────────────────────────────────────────────────────────────────
#
#   olivares_overlay_measure_open <check-id> <record-path> <mode> [<overlay-dir>]
#       mode is `current` (a sibling overlay clone is named) or `static-only`.
#       0 · the current observation is open. On 0 and mode=current it EXPORTS:
#             OLIVARES_OVERLAY_CURRENT_SHA  the captured 40-hex the adapter must read at
#             OLIVARES_OVERLAY_CURRENT_PIN  the current public gitlink, or empty
#       1 · a demonstrated contract/schema/property/history finding
#       2 · a required input could not be observed, or custody could not be established
#       On 1 and 2 the reason is in OLIVARES_OVERLAY_MEASUREMENT_WHY and on stderr.
#
#   olivares_overlay_measure_close <reader-exit> <reader-detail>
#       Re-verifies that the source and the seal GENERATION did not move during this
#       invocation, writes the terminal record atomically into the caller-owned
#       observation directory, and emits the complete structured result to the ordinary
#       captured output. Returns the invocation's final 0/1/2.
#
#   olivares_overlay_measure_finish <reader-exit> <reader-detail>
#       close, then the ONE contracted translation to the adapter's exit. All seven use it.
#       Sets OLIVARES_OVERLAY_FINAL_WHY. See the contract above the function.
#
#   A REFUSED invocation is published too. Any refusal — including the seal, which is the
#   most common one — writes a terminal record with `observation: refused`, its own 1 or 2,
#   the context that was established and the inputs that were NOT read. It never invents a
#   captured identity, and the collector keeps its 1-versus-2 instead of reading the old
#   absence as a finding.
#
# ── FOUR THINGS THIS FILE GETS WRONG IF IT IS CARELESS ───────────────────────────────
#
#  1. GIT ENVIRONMENT FIRST, BEFORE THE FIRST GIT CALL. This Module drives TWO
#     repositories and reserves scratch, and `GIT_DIR` outranks `-C`: an inherited
#     selector would make every read land in another object store. `check-overlay-live-facts.sh`
#     already sources the isolator for exactly this reason; the Module is in the same class,
#     and failing to load it is 2 — never CLEAN.
#  2. THE SEAL GENERATION, NOT THE ACT, IS THE INVARIANT. `.githooks/pre-push` runs
#     `task lint:overlay-seal` TWICE in one act (the preceding lints exceeded the reader's
#     unchanged 900s limit on 2026-09-05) and `scripts/fetch-overlay-seal.sh` keeps the act
#     id, so the second seal legitimately carries a DIFFERENT SHA under the SAME act id.
#     An invariance written at act scope would return 2 for all seven exactly when the
#     overlay advanced between the two fetches — the case LT1 exists to make survivable.
#     So the generation is (epoch, seal content digest) and the scope is one invocation.
#  3. THE EIGHTH SEAL CONSUMER IS NOT ONE OF THE SEVEN. `scripts/check-int-12-no-land.sh`
#     reads the same seal and is a member of the hook's current-measurement group; seven
#     independent successes are NOT a proof of cross-leg snapshot isolation. Root's coarse
#     publication lock stays until a proof covers all EIGHT under one capture.
#  4. `$OLIVARES_ENT_DIR` STAYS RESOLVED IN EACH READER, ON A CODE LINE. The acta
#     classifier (`scripts/check-overlay-actas-class.sh:56,113-115`) calls an acta VIVA only
#     if some reader that names it also USES that variable in code. Absorbing the resolution
#     into this Module would silently drop all seven from `--list`, and
#     `scripts/remeasure-overlay-actas.sh:87` derives its whole working set from that list —
#     it would report "none needs re-measuring" over a set missing every member. The readers
#     therefore resolve and validate the variable themselves and pass the directory in.
#
# This file must be sourced, not executed. It uses `mktemp` for a file, never `mktemp -d`,
# and it builds no repository: it is not in the class `check-git-env-isolation.sh` polices,
# but it isolates anyway because point 1 above is about READS, not about sandboxes.

OLIVARES_OVERLAY_MEASUREMENT_WHY=""
OLIVARES_OVERLAY_CURRENT_SHA=""
OLIVARES_OVERLAY_CURRENT_PIN=""
OLIVARES_OVERLAY_RESULT=""
OLIVARES_OVERLAY_FINAL_WHY=""
_olivares_overlay_state=""
_olivares_overlay_overlay_dir=""
_olivares_overlay_custody=""

_olivares_overlay_obs_dir() {
	printf '%s\n' "${OLIVARES_OVERLAY_OBS_DIR:-${OLIVARES_ROOT:-.}/.overlay-observations}"
}

# Publish a terminal refusal for a shell-half failure. The Python half publishes its own;
# this covers the ones that happen BEFORE it is reached — the seal above all, which is the
# most common real refusal and used to leave the act with no record at all.
_olivares_overlay_refuse() { # <code> <predicate> <detail> [<unobserved-csv>]
	local code="$1" predicate="$2" detail="$3" unobs="${4:-}"
	local lib impl
	lib="$_OLIVARES_OVERLAY_LIB_DIR"
	impl="$lib/../overlay-measure.py"
	[ -r "$impl" ] || return 0
	command -v python3 >/dev/null 2>&1 || return 0
	python3 "$impl" refuse \
		--check "${_olivares_overlay_check:-}" --record "${_olivares_overlay_record:-}" \
		--mode "${_olivares_overlay_mode:-}" --act "${OLIVARES_ACT_ID:-}" \
		--seal-epoch "${_olivares_overlay_seal_epoch:-}" \
		--seal-digest "${_olivares_overlay_seal_digest:-}" \
		--current-sha "${_olivares_overlay_seal_sha:-}" \
		--code "$code" --predicate "$predicate" --detail "$detail" \
		--unobserved "$unobs" --observations "$(_olivares_overlay_obs_dir)" >/dev/null || return 0
	return 0
}

# Resolved AT SOURCE TIME, not per call: `${BASH_SOURCE[0]}` read inside a function is the
# defining file in bash and nothing at all in a shell that does not keep that array, and a
# library that mislocates itself reports "cannot source git-env.sh" for a file that is there.
_OLIVARES_OVERLAY_LIB_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)"

_olivares_overlay_lib_dir() {
	printf '%s\n' "$_OLIVARES_OVERLAY_LIB_DIR"
}

_olivares_overlay_cleanup() {
	[ -n "$_olivares_overlay_state" ] && rm -f -- "$_olivares_overlay_state" "$_olivares_overlay_state.tmp"
	_olivares_overlay_state=""
	return 0
}

# 40 hex, judged as BYTES and by an explicit enumeration: a bracket range follows the
# locale, and this repository has been bitten by that before.
_olivares_overlay_hex40() {
	case "${1:-}" in
	"" | *[!0123456789abcdef]*) return 1 ;;
	esac
	[ "${#1}" = 40 ]
}

olivares_overlay_measure_open() { # <check-id> <record-path> <mode> [<overlay-dir>]
	local check="${1:-}" record="${2:-}" mode="${3:-}" ent="${4:-}"
	local lib root seal epoch digest sha rc
	OLIVARES_OVERLAY_MEASUREMENT_WHY=""
	OLIVARES_OVERLAY_CURRENT_SHA=""
	OLIVARES_OVERLAY_CURRENT_PIN=""
	OLIVARES_OVERLAY_RESULT=""

	lib="$(_olivares_overlay_lib_dir)"
	root="${OLIVARES_ROOT:-$(cd -- "$lib/../.." && pwd)}"
	_olivares_overlay_check="$check"
	_olivares_overlay_record="$record"
	_olivares_overlay_mode="$mode"
	_olivares_overlay_seal_epoch=""
	_olivares_overlay_seal_digest=""
	_olivares_overlay_seal_sha=""

	# 1 · THE ISOLATOR, BEFORE THE FIRST GIT CALL. Fail-closed.
	# shellcheck source=/dev/null
	if ! . "$lib/git-env.sh"; then
		OLIVARES_OVERLAY_MEASUREMENT_WHY="cannot source $lib/git-env.sh (git-env isolation): with an inherited GIT_DIR every read below would land in another object store"
		_olivares_overlay_refuse 2 capture.git_env_isolation "$OLIVARES_OVERLAY_MEASUREMENT_WHY" 'overlay:current-main,overlay:baseline,community:record-objects'
		return 2
	fi

	if [ -z "$check" ] || [ -z "$record" ]; then
		OLIVARES_OVERLAY_MEASUREMENT_WHY="the Interface needs a registered check id and a historical record path"
		_olivares_overlay_refuse 2 interface.arguments "$OLIVARES_OVERLAY_MEASUREMENT_WHY" 'overlay:current-main,overlay:baseline'
		return 2
	fi
	if [ ! -r "$record" ]; then
		OLIVARES_OVERLAY_MEASUREMENT_WHY="historical record $record is not readable"
		_olivares_overlay_refuse 2 historical.record_readable "$OLIVARES_OVERLAY_MEASUREMENT_WHY" 'historical-record'
		return 2
	fi
	if [ ! -r "$lib/../overlay-measure.py" ]; then
		OLIVARES_OVERLAY_MEASUREMENT_WHY="missing $lib/../overlay-measure.py: the Module cannot measure without its implementation"
		_olivares_overlay_refuse 2 interface.implementation_present "$OLIVARES_OVERLAY_MEASUREMENT_WHY" 'overlay:current-main,overlay:baseline'
		return 2
	fi
	command -v python3 >/dev/null 2>&1 || {
		# Nothing to publish with: the record writer IS python3. Said, not hidden.
		OLIVARES_OVERLAY_MEASUREMENT_WHY="python3 is not on PATH, so this refusal cannot be retained either"
		return 2
	}

	_olivares_overlay_state="$(mktemp "${TMPDIR:-/tmp}/overlay-measure.XXXXXX")" || {
		OLIVARES_OVERLAY_MEASUREMENT_WHY="cannot reserve a scratch file for this invocation (TMPDIR=${TMPDIR:-unset})"
		_olivares_overlay_refuse 2 custody.scratch_reserved "$OLIVARES_OVERLAY_MEASUREMENT_WHY" 'overlay:current-main,overlay:baseline'
		return 2
	}

	case "$mode" in
	static-only)
		_olivares_overlay_run "$lib" --check "$check" --record "$record" --mode static-only \
			--act "${OLIVARES_ACT_ID:-}" --state "$_olivares_overlay_state" \
			--observations "$(_olivares_overlay_obs_dir)"
		return $?
		;;
	current) ;;
	*)
		OLIVARES_OVERLAY_MEASUREMENT_WHY="unknown observation mode '$mode' (current | static-only)"
		_olivares_overlay_refuse 2 interface.mode "$OLIVARES_OVERLAY_MEASUREMENT_WHY" 'overlay:current-main,overlay:baseline'
		return 2
		;;
	esac

	if [ -z "$ent" ]; then
		OLIVARES_OVERLAY_MEASUREMENT_WHY="a current observation needs the overlay clone the caller resolved"
		_olivares_overlay_refuse 2 capture.overlay_store "$OLIVARES_OVERLAY_MEASUREMENT_WHY" 'overlay:current-main,overlay:baseline'
		return 2
	fi
	_olivares_overlay_overlay_dir="$ent"

	# 2 · FRESHNESS IS REQUIRED, NOT ASSUMED (a repository gate). A frozen ref is not a verdict:
	# on 2026-08-29 the whole box compared against an origin/main one merge behind and every
	# gate said CLEAN, because the clone FETCHES over SSH (silently failing without a key)
	# and PUSHES over HTTPS. Unchanged: the seal must be of THIS act, recent, rc=0, and
	# describe THIS clone, or this is "could not look".
	# shellcheck source=/dev/null
	if ! . "$root/scripts/lib/overlay-seal.sh"; then
		OLIVARES_OVERLAY_MEASUREMENT_WHY="cannot load scripts/lib/overlay-seal.sh"
		_olivares_overlay_refuse 2 capture.seal_library "$OLIVARES_OVERLAY_MEASUREMENT_WHY" 'overlay:current-main,seal'
		return 2
	fi
	if ! overlay_seal_require "$ent"; then
		OLIVARES_OVERLAY_MEASUREMENT_WHY="$OVERLAY_SEAL_WHY"
		_olivares_overlay_refuse 2 capture.seal_fresh "$OLIVARES_OVERLAY_MEASUREMENT_WHY" 'overlay:current-main,overlay:baseline,community:record-objects'
		return 2
	fi

	seal="${OLIVARES_OVERLAY_SEAL:-${OLIVARES_ROOT:-.}/.overlay-fetch-seal}"
	line="$(head -1 "$seal" 2>/dev/null)" || line=""
	epoch="${line%% *}"
	sha="$(printf '%s' "$line" | awk '{print $3}')"
	digest="$(python3 -c 'import hashlib,sys;print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$seal" 2>/dev/null)" || digest=""
	if ! _olivares_overlay_hex40 "$sha" || [ -z "$digest" ]; then
		OLIVARES_OVERLAY_MEASUREMENT_WHY="the seal at $seal carries no usable generation identity"
		_olivares_overlay_refuse 2 capture.seal_generation "$OLIVARES_OVERLAY_MEASUREMENT_WHY" 'overlay:current-main,overlay:baseline'
		return 2
	fi

	_olivares_overlay_seal_epoch="$epoch"
	_olivares_overlay_seal_digest="$digest"
	_olivares_overlay_seal_sha="$sha"

	# The CAPTURE. Everything current is read at this 40-hex from here on, never at the
	# moving name: `overlay_seal_require` has just established that origin/main resolves to
	# it, and the Module re-checks that at entry and again at completion.
	_olivares_overlay_run "$lib" --check "$check" --record "$record" --mode current \
		--overlay "$ent" --community "$root" \
		--current-sha "$sha" --act "${OLIVARES_ACT_ID:-}" \
		--seal-epoch "$epoch" --seal-digest "$digest" --seal-path "$seal" \
		--state "$_olivares_overlay_state" --observations "$(_olivares_overlay_obs_dir)"
	rc=$?
	[ "$rc" = 0 ] || return "$rc"
	if ! _olivares_overlay_hex40 "$OLIVARES_OVERLAY_CURRENT_SHA"; then
		OLIVARES_OVERLAY_MEASUREMENT_WHY="the Module opened without a captured 40-hex current main"
		_olivares_overlay_refuse 2 capture.current_sha "$OLIVARES_OVERLAY_MEASUREMENT_WHY" 'overlay:current-main'
		return 2
	fi
	export OLIVARES_OVERLAY_CURRENT_SHA OLIVARES_OVERLAY_CURRENT_PIN
	return 0
}

# Run `overlay-measure.py open` and read its protocol from a FILE, as bytes. A command
# substitution would be the wrong reader for a protocol: it strips trailing newlines and
# deletes NUL bytes, so a malformed reply and a valid one become the same value. The
# implementation's own diagnostics go straight to the caller's stderr, unswallowed.
_olivares_overlay_run() { # <lib-dir> <open args...>
	local lib="$1" proto rc line key val
	shift
	proto="$(mktemp "${TMPDIR:-/tmp}/overlay-measure-proto.XXXXXX")" || {
		OLIVARES_OVERLAY_MEASUREMENT_WHY="cannot reserve a scratch file for the Module protocol"
		return 2
	}
	rc=0
	python3 "$lib/../overlay-measure.py" open "$@" >"$proto" || rc=$?
	while IFS= read -r line; do
		key="${line%%=*}"
		val="${line#*=}"
		case "$key" in
		OLIVARES_OVERLAY_CURRENT_SHA)
			_olivares_overlay_hex40 "$val" && OLIVARES_OVERLAY_CURRENT_SHA="$val"
			;;
		OLIVARES_OVERLAY_CURRENT_PIN)
			_olivares_overlay_hex40 "$val" && OLIVARES_OVERLAY_CURRENT_PIN="$val"
			;;
		OLIVARES_OVERLAY_WHY)
			OLIVARES_OVERLAY_MEASUREMENT_WHY="$val"
			;;
		esac
	done <"$proto"
	rm -f -- "$proto"
	case "$rc" in
	0 | 1 | 2) ;;
	*)
		OLIVARES_OVERLAY_MEASUREMENT_WHY="the measurement Module exited $rc, which is none of its three contracted answers"
		return 2
		;;
	esac
	if [ "$rc" != 0 ] && [ -z "$OLIVARES_OVERLAY_MEASUREMENT_WHY" ]; then
		OLIVARES_OVERLAY_MEASUREMENT_WHY="the measurement Module refused ($rc) without naming a predicate"
	fi
	return "$rc"
}

olivares_overlay_measure_close() { # <reader-exit> <reader-detail>
	local rexit="${1:-0}" detail="${2:-}" lib obs rc out
	lib="$(_olivares_overlay_lib_dir)"
	if [ -z "$_olivares_overlay_state" ] || [ ! -r "$_olivares_overlay_state" ]; then
		OLIVARES_OVERLAY_MEASUREMENT_WHY="close was called without an open invocation state"
		return 2
	fi
	obs="${OLIVARES_OVERLAY_OBS_DIR:-${OLIVARES_ROOT:-.}/.overlay-observations}"
	rc=0
	out="$(python3 "$lib/../overlay-measure.py" close \
		--state "$_olivares_overlay_state" \
		--overlay "$_olivares_overlay_overlay_dir" \
		--reader-exit "$rexit" --reader-detail "$detail" \
		--observations "$obs")" || rc=$?
	# First line is the custody protocol, the rest is the record. Strip it before the record
	# goes to the ordinary captured output: the caller needs to know WHICH fact drove a 2.
	_olivares_overlay_custody=""
	case "$out" in
	"OLIVARES_OVERLAY_CUSTODY="*)
		_olivares_overlay_custody="${out%%$'\n'*}"
		_olivares_overlay_custody="${_olivares_overlay_custody#OLIVARES_OVERLAY_CUSTODY=}"
		out="${out#*$'\n'}"
		;;
	esac
	# The complete structured result goes to the ordinary captured output of the leg.
	[ -n "$out" ] && printf '%s\n' "$out"
	OLIVARES_OVERLAY_RESULT="$out"
	_olivares_overlay_cleanup
	if [ "$rc" != 0 ] && [ "$rc" != 1 ] && [ "$rc" != 2 ]; then
		OLIVARES_OVERLAY_MEASUREMENT_WHY="the measurement Module's close exited $rc, which is none of its three contracted answers"
		return 2
	fi
	if [ "$rc" = 2 ]; then
		OLIVARES_OVERLAY_MEASUREMENT_WHY="the observation could not keep capture or result custody to completion"
	fi
	return "$rc"
}

# ⛔ ONE CONTRACTED TRANSLATION FOR ALL SEVEN ADAPTERS, because six of them had it wrong.
# Their tails asked `[ "$reader_rc" = 0 ] || fail` BEFORE `[ "$close_rc" = 0 ] || cannot`, so a
# reader finding SHORT-CIRCUITED a lost-custody verdict: an invocation that both found a
# contradiction and failed to retain its evidence exited 1, while this Module's own contract
# says a failed publication is 2 whatever the reader said. Both facts have to survive — the
# custody failure decides the exit, the reader's finding stays in the terminal predicates and
# on stderr — and a close that returns 1 must STAY 1 rather than being swept into a generic
# `cannot`. Writing that once is the only way seven tails agree.
#
#   reader 0 · custody ok      -> 0
#   reader 1 · custody ok      -> 1   (close 1 stays 1)
#   reader 2 · custody ok      -> 2
#   reader 0/1/2 · custody bad -> 2, naming the custody failure AND the reader's outcome
#
# Returns the final code and sets OLIVARES_OVERLAY_FINAL_WHY for the adapter's message.
olivares_overlay_measure_finish() { # <reader-exit> <reader-detail>
	local rexit="${1:-0}" detail="${2:-}" crc=0
	OLIVARES_OVERLAY_FINAL_WHY=""
	olivares_overlay_measure_close "$rexit" "$detail" || crc=$?
	if [ "$_olivares_overlay_custody" = failed ]; then
		OLIVARES_OVERLAY_FINAL_WHY="${OLIVARES_OVERLAY_MEASUREMENT_WHY:-result custody could not be kept}"
		case "$rexit" in
		1) OLIVARES_OVERLAY_FINAL_WHY="$OLIVARES_OVERLAY_FINAL_WHY — and the reader ALSO found a contradiction, kept in the terminal predicates and above: $detail" ;;
		2) OLIVARES_OVERLAY_FINAL_WHY="$OLIVARES_OVERLAY_FINAL_WHY — and the reader could not observe a required input either" ;;
		esac
		return 2
	fi
	case "$crc" in
	0) return 0 ;;
	1) OLIVARES_OVERLAY_FINAL_WHY="$detail"; return 1 ;;
	2) OLIVARES_OVERLAY_FINAL_WHY="${OLIVARES_OVERLAY_MEASUREMENT_WHY:-a required input of this observation could not be read}"; return 2 ;;
	esac
	OLIVARES_OVERLAY_FINAL_WHY="the Module's close exited $crc, which is none of its three contracted answers"
	return 2
}
