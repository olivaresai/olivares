#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# cosign-guard.sh — refuse, rather than merely discourage, any cosign command on a
# development machine that could write to the public Sigstore transparency log.
#
# Note the wording. An earlier header said "make an accidental public record IMPOSSIBLE",
# and then the paragraph below listed the ways it is not: an absolute path, `command -p`, a
# shell alias, or a shell that never exported this directory all walk straight past a PATH
# shim. A control that overstates itself in its own first line teaches people to discount
# the caveats underneath.
#
# WHY THIS EXISTS. On 2026-07-25, while measuring whether cosign v3 can sign without a
# transparency-log entry, this project created TWO PERMANENT, UNDELETABLE records in the
# public Rekor instance (logIndex 2247019100 and 2247019486, kind hashedrekord). The
# containment that would have prevented it already existed — in
# scripts/check-cosign-contract.sh — and was simply not invoked. Containment that depends
# on a human remembering to invoke it is not containment.
#
# THE TRAP IS REAL, AND THE OBVIOUS MITIGATIONS DO NOT WORK:
#   * under cosign v3 the transparency log is ON BY DEFAULT;
#   * `--use-signing-config=false` does NOT disable it — it only stops the service URLs
#     being fetched from TUF. Measured: with that flag, both `--bundle` and
#     `--output-signature` uploaded to the real public Rekor;
#   * a `--signing-config` with no transparency-log service DOES avoid the upload, but it
#     is read by the same binary whose behaviour is under test, so it is not an
#     independent barrier — it is the same circularity, relocated.
#
# THE POLICY, IN ONE SENTENCE: a subcommand that can publish is DENIED unless the
# invocation itself proves the transparency log is off, and even then it runs contained.
#
#   * read-only verbs (an explicit allowlist)      -> allowed, contained
#   * publication-capable verbs WITH an explicit
#     tlog-disabling flag (`--tlog-upload=false`)  -> allowed, contained, announced
#   * everything else, including verbs this guard
#     has never heard of                           -> DENIED
#
# The middle case is not a loophole, it is the reason the guard can be ON by default:
# scripts/check-cosign-contract.sh must sign — with a throwaway key and the log disabled —
# to prove the release contract at all, and a containment that forces that fixture to be
# disabled is a containment nobody will leave switched on. An earlier revision denied it
# outright, which would have made activation (below) break the very fixture that proves
# the signing contract.
#
# WHAT THE PROXY IS WORTH, MEASURED. Every non-denied invocation runs with HTTP(S)_PROXY
# pointing at a refused loopback port, which today stops cosign reaching Sigstore even when
# it fully intends to:
#     Error: … uploading to rekor: … proxyconnect tcp: dial tcp 127.0.0.1:1:
#     connect: connection refused
# That is version-sensitive defence in depth, and it is what covers the case where a future
# cosign accepts `--tlog-upload=false` and ignores it. It is NOT a guarantee.
#
# HONEST LIMIT, AND IT IS THE IMPORTANT PARAGRAPH. This is a PATH shim. It covers only what
# resolves `cosign` through a PATH that has this directory first. It does NOT cover an
# absolute path (`/usr/local/bin/cosign`), `command -p`, a shell alias, or an interactive
# shell that never exported the guard directory.
#
# A reviewer proposed making activation declarative by putting the guard directory first in
# the Taskfile's `env:`. MEASURED 2026-07-25: go-task CANNOT override PATH that way. A
# sibling variable in the same `env:` block received its templated value correctly while
# PATH was passed through unchanged, at both global and per-task scope (go-task 3.51.1).
# Only an in-command prefix works, so activation is done by scripts/with-cosign-guard.sh
# and ASSERTED with `--status`, rather than assumed from a setting that does nothing.
#
# A real out-of-process barrier — a network namespace or an egress firewall — is not
# available here: `unshare -rn` returns "Operation not permitted" in this container. Where
# one IS available (CI, the rehearsal runner), it must be the control and this shim is only
# the workstation layer.
#
# USAGE
#   Install:   task cosign:guard                 (also run by `task setup`, and asserted)
#   Verify:    scripts/cosign-guard.sh --status
#   Escape:    OLIVARES_COSIGN_ALLOW_PUBLIC_LOG=1 cosign …
set -euo pipefail

GUARD_VERSION=3
DEAD_PROXY="http://127.0.0.1:1"
SHIM_MARKER="olivares-cosign-guard-shim"

# find_real_cosign prints the first executable `cosign` on PATH that is NOT one of our
# shims. Skipping by marker is what makes two shims on PATH (two worktrees, say) terminate:
# an earlier revision searched heuristically and an adversarial probe of exactly that shape
# timed out.
find_real_cosign() {
	local d cand
	local -a dirs
	IFS=: read -ra dirs <<<"$PATH"
	for d in "${dirs[@]}"; do
		[ -n "$d" ] || continue
		cand="$d/cosign"
		[ -x "$cand" ] && [ -f "$cand" ] || continue
		grep -qF "$SHIM_MARKER" "$cand" 2>/dev/null && continue
		printf '%s\n' "$cand"
		return 0
	done
	return 1
}

# --- self-service subcommands (not a cosign invocation) --------------------------------
case "${1:-}" in
--status)
	# ACTIVATION IS ABOUT PATH, NOT ABOUT AN INHERITED VARIABLE. An earlier revision
	# returned success as soon as OLIVARES_COSIGN_GUARD was set, so a process that had
	# inherited it after PATH was reset reported ACTIVE while being entirely unguarded.
	# The question this answers is only ever: does `cosign` resolve to the shim?
	resolved="$(command -v cosign 2>/dev/null || true)"
	if [ -n "$resolved" ] && grep -qF "$SHIM_MARKER" "$resolved" 2>/dev/null; then
		echo "cosign-guard: ACTIVE — 'cosign' on PATH resolves to the guard shim ($resolved)"
		if [ -n "${OLIVARES_COSIGN_GUARD:-}" ]; then
			echo "cosign-guard: this process is itself inside a guarded invocation (version ${OLIVARES_COSIGN_GUARD})"
		fi
		echo "cosign-guard: NOT covered by a PATH shim: absolute paths, 'command -p', shell aliases,"
		echo "cosign-guard: and shells that never exported this directory. Only an egress rule covers those."
		exit 0
	fi
	echo "cosign-guard: NOT ACTIVE — 'cosign' on PATH is ${resolved:-absent} and is not the guard shim." >&2
	echo "Install it with 'task cosign:guard' and put its directory FIRST on PATH." >&2
	exit 1
	;;
--install)
	target="${2:-}"
	[ -n "$target" ] || {
		echo "usage: $0 --install <dir>" >&2
		exit 2
	}
	self="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/$(basename -- "$0")"

	# Resolve the REAL cosign ONCE and embed its absolute path: a recorded absolute path
	# cannot loop. If cosign is not installed YET, install the shim anyway with an empty
	# path — the guard must exist BEFORE the tool it guards, or the window between
	# installing cosign and re-running setup is unguarded. In that case the shim resolves
	# lazily at invocation time (still skipping shims, so still no loop).
	realbin="$(find_real_cosign || true)"

	mkdir -p "$target"
	# NEVER truncate a binary that is not ours. Overwriting an unrelated `cosign` would
	# silently destroy the very tool the release path depends on.
	if [ -e "$target/cosign" ] && ! grep -qF "$SHIM_MARKER" "$target/cosign" 2>/dev/null; then
		echo "::error::cosign-guard: $target/cosign exists and is NOT a guard shim; refusing to overwrite it." >&2
		exit 1
	fi

	# EXPLICIT INTERPRETER and QUOTED paths: scripts here are tracked mode 100644, so
	# `exec <script>` would die with "Permission denied" on a fresh checkout, and an
	# unquoted path breaks on a directory containing spaces. OLIVARES_COSIGN_SHIM is what
	# tells the guard it was reached legitimately rather than run as a cosign replacement.
	cat >"$target/cosign" <<SHIM
#!/usr/bin/env bash
# $SHIM_MARKER
export OLIVARES_COSIGN_SHIM=1
export OLIVARES_COSIGN_REAL="$realbin"
exec bash "$self" "\$@"
SHIM
	chmod 0755 "$target/cosign"
	if [ -n "$realbin" ]; then
		echo "cosign-guard: shim installed at $target/cosign -> $realbin"
	else
		echo "cosign-guard: shim installed at $target/cosign (no cosign on PATH yet; it will be resolved on first use)"
	fi
	echo "Put it FIRST on PATH:  export PATH=\"$target:\$PATH\""
	echo "Tasks that invoke cosign go through scripts/with-cosign-guard.sh, which prepends it and"
	echo "asserts --status. An INTERACTIVE shell is covered only once you export the line above."
	exit 0
	;;
esac

# --- locate the real cosign ------------------------------------------------------------
# Reached ONLY through the shim. Running this script directly as a cosign replacement is
# refused, because then nothing distinguishes it from the binary it is standing in for.
if [ -z "${OLIVARES_COSIGN_SHIM:-}" ]; then
	echo "::error::cosign-guard: the shim did not supply a usable real cosign path (OLIVARES_COSIGN_REAL=${OLIVARES_COSIGN_REAL:-unset})." >&2
	echo "Re-install with 'task cosign:guard'; do NOT invoke this script directly as a cosign replacement." >&2
	exit 127
fi

real="${OLIVARES_COSIGN_REAL:-}"
if [ -z "$real" ] || [ ! -x "$real" ]; then
	real="$(find_real_cosign || true)"
fi
if [ -z "$real" ] || [ ! -x "$real" ]; then
	echo "::error::cosign-guard: no cosign is installed on this machine (the guard shim is on PATH, the tool is not)." >&2
	echo "Install cosign, then re-run 'task cosign:guard' so the shim records its absolute path." >&2
	exit 127
fi

# --- classify the invocation: DEFAULT DENY ----------------------------------------------
# An earlier revision inspected only argv[0] and treated every unrecognised verb as safe.
# That failed OPEN twice over: `cosign --verbose sign-blob …` slipped past because argv[0]
# was a flag, and any publishing subcommand added by a future cosign inherited permission
# by default. The classification is now: skip leading global flags, then allow ONLY an
# explicit read-only allowlist. Anything else — including a verb this guard has never heard
# of — is publication-capable until proven otherwise.
sub=""
skip_next=0
for arg in "$@"; do
	if [ "$skip_next" = "1" ]; then
		skip_next=0
		continue
	fi
	case "$arg" in
	--)
		break
		;;
	--*=*) ;; # inline value, no separate operand
	-d | --verbose | --help | -h)
		: # boolean global flags
		;;
	--output-file | --timeout | -t)
		skip_next=1
		;;
	-*)
		# An unknown flag may or may not take an operand. Do not guess: keep scanning,
		# which at worst finds the subcommand one argument late and denies it.
		:
		;;
	*)
		sub="$arg"
		break
		;;
	esac
done

# Read-only verbs. Everything absent from this list is publication-capable, including
# future ones.
case "$sub" in
"" | version | help | completion | env | public-key | generate-key-pair | signing-config | \
	initialize | verify | verify-blob | verify-attestation | verify-blob-attestation | \
	tree | triangulate | dockerfile | manifest | policy | download | save | load)
	publication_capable=0
	;;
*)
	publication_capable=1
	;;
esac

# Does the invocation itself prove the transparency log is off?
#
# THREE MEASURED FACTS SHAPE THIS, AND EACH ONE BROKE AN EARLIER VERSION:
#
# 1. `--use-signing-config=false` does NOT disable the log. Measured 2026-07-25: with that
#    flag, both `--bundle` and `--output-signature` uploaded to the real public Rekor.
#
# 2. The SPLIT spelling means the OPPOSITE. `--tlog-upload` is a pflag boolean with
#    `NoOptDefVal="true"`, so it consumes no operand. Measured on the pinned v2.6.4:
#        $ cosign sign-blob --tlog-upload false realfile.txt
#        Error: signing false: upload to tlog: user declined the prompt
#    "signing false" — the word became the FILE — and "upload to tlog" — the log was ON.
#
# 3. THE LAST OCCURRENCE WINS, and a value-bearing option swallows the next token. pflag
#    calls Set for every occurrence, so `--tlog-upload=false --tlog-upload=true` ends with
#    the log ON; and `--oidc-client-id --tlog-upload=false` feeds the safety flag to
#    --oidc-client-id as its VALUE while the real flag stays at its default, true. A scan
#    that stopped at the first match, over a finite list of value-bearing options, permitted
#    both. That list can never be complete — cosign may add an option at any release.
#
# SO THE RULE DOES NOT TRY TO REIMPLEMENT PFLAG. It requires the containment flag to be
# UNAMBIGUOUS BY POSITION: the very first argument after the subcommand, in its inline form,
# with no second `--tlog-upload` anywhere in the vector. Nothing can precede it to swallow
# it, and nothing after it can override it. An invocation that genuinely wants containment
# can always be written that way; one that cannot is not one this guard should reason about.
tlog_disabled=0
if [ "$publication_capable" = "1" ] && [ -n "$sub" ]; then
	# Find the subcommand's position, then look at exactly the next argument.
	idx=0
	sub_idx=-1
	for arg in "$@"; do
		if [ "$sub_idx" -lt 0 ] && [ "$arg" = "$sub" ]; then
			sub_idx=$idx
		fi
		idx=$((idx + 1))
	done
	if [ "$sub_idx" -ge 0 ]; then
		next_idx=$((sub_idx + 2)) # $1-based
		next_arg="${!next_idx:-}"
		case "$next_arg" in
		--tlog-upload=false | --tlog-upload=0 | --tlog-upload=FALSE | --tlog-upload=False)
			tlog_disabled=1
			;;
		esac
	fi
	# Any SECOND mention, in either direction, makes the parse ambiguous to a reader even
	# when pflag would resolve it. Refuse rather than reason about precedence.
	if [ "$tlog_disabled" = "1" ]; then
		mentions=0
		for arg in "$@"; do
			case "$arg" in
			--tlog-upload | --tlog-upload=*) mentions=$((mentions + 1)) ;;
			esac
		done
		if [ "$mentions" -ne 1 ]; then
			tlog_disabled=0
			echo "cosign-guard: '${sub}' names --tlog-upload ${mentions} times; the last occurrence is the one pflag applies, so this is refused as ambiguous." >&2
		fi
	fi
fi

if [ "${OLIVARES_COSIGN_ALLOW_PUBLIC_LOG:-0}" = "1" ]; then
	# The one deliberate escape, and the ONLY path that reaches the real network. The
	# banner names the subcommand only — NOT the argument vector, which can carry a KMS
	# URI, a token or a key reference.
	{
		echo "############################################################################"
		echo "# cosign-guard: CONTAINMENT DISABLED by OLIVARES_COSIGN_ALLOW_PUBLIC_LOG=1"
		echo "# This invocation CAN write to the PUBLIC Sigstore transparency log."
		echo "# Such records are PERMANENT and CANNOT be deleted."
		echo "#   subcommand: ${sub:-<none>}   (arguments withheld: they may carry secrets)"
		echo "############################################################################"
	} >&2
	exec "$real" "$@"
fi

if [ "$publication_capable" = "1" ] && [ "$tlog_disabled" = "0" ]; then
	{
		echo "::error::cosign-guard: '${sub:-<unrecognised>}' can publish and this invocation does not disable the transparency log; it is DENIED on this machine."
		echo "This guard DEFAULT-DENIES: a verb it does not recognise is treated as capable of publishing."
		echo "This is a denial, not a warning: on 2026-07-25 an uncontained probe created two permanent"
		echo "public Rekor records that cannot be removed."
		echo ""
		echo "To sign with the log provably off (what the contract fixture does), pass it explicitly:"
		echo "    cosign $sub --tlog-upload=false ..."
		echo "Note that --use-signing-config=false does NOT count: it was measured to upload anyway."
		echo ""
		echo "If you genuinely need the PUBLIC log, say so and understand the record is forever:"
		echo "    OLIVARES_COSIGN_ALLOW_PUBLIC_LOG=1 cosign $sub ..."
		echo ""
		echo "Release signing does NOT go through this machine: it runs in the release workflow, where"
		echo "the barrier must be an egress deny, not this shim."
	} >&2
	exit 1
fi

if [ "$publication_capable" = "1" ]; then
	# Allowed because the invocation disables the log explicitly — and still contained,
	# because a flag is a request to cosign, not a property of the network.
	echo "cosign-guard: '${sub}' permitted — --tlog-upload=false is present and egress is refused anyway." >&2
fi

# --- contained execution -----------------------------------------------------------------
# Defence in depth, not the control. Applied to every remaining subcommand because a
# development container has no business reaching Sigstore at all — `verify-blob` fetches
# TUF material, for instance.
#
# NARROW EXCEPTION, READ-ONLY VERBS ONLY. Verifying an upstream signature genuinely needs
# the network (TUF root, certificate chain), and it CANNOT create a transparency-log record
# — that is what "read-only" means here. scripts/update-cosign-digests.sh needs exactly
# this and nothing more. The exception is refused for anything publication-capable even
# when the variable is set, so it can never become a second way to sign.
if [ "${OLIVARES_COSIGN_ALLOW_VERIFY_NETWORK:-0}" = "1" ]; then
	if [ "$publication_capable" = "1" ]; then
		echo "::error::cosign-guard: OLIVARES_COSIGN_ALLOW_VERIFY_NETWORK applies to read-only verbs only; '${sub}' is not one." >&2
		exit 1
	fi
	echo "cosign-guard: network permitted for read-only '${sub}' (cannot write to the transparency log)." >&2
	export OLIVARES_COSIGN_GUARD="$GUARD_VERSION"
	exec "$real" "$@"
fi

export HTTP_PROXY="$DEAD_PROXY" HTTPS_PROXY="$DEAD_PROXY" ALL_PROXY="$DEAD_PROXY"
export http_proxy="$DEAD_PROXY" https_proxy="$DEAD_PROXY" all_proxy="$DEAD_PROXY"
# NO_PROXY would punch a hole straight through the barrier for the very hosts being
# contained, so it is cleared rather than trusted.
unset NO_PROXY no_proxy
export OLIVARES_COSIGN_GUARD="$GUARD_VERSION"

exec "$real" "$@"
