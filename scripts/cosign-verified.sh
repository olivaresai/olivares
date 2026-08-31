#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# cosign-verified.sh — run cosign as the VERIFIED BYTES, not as a name on PATH and not as a
# pathname that was authenticated once at the top of the job.
#
# WHY IT EXISTS, IN TWO PARTS.
#
# 1. GORELEASER CANNOT TEMPLATE `cmd:`. An earlier revision of `.goreleaser.yaml` set
#    `cmd: "{{ .Env.OLIVARES_COSIGN_BIN }}"`, reasoning that the signing command should be
#    the absolute path the invariant verified. `goreleaser check` accepted it — and it would
#    have failed every real release, because in pinned GoReleaser v2.17.0 `cmd` goes
#    straight to `exec.CommandContext` with no template pass:
#
#        internal/pipe/sign/sign.go:249   cmd := exec.CommandContext(ctx, cfg.Cmd, args...)
#
#    while `args`, `signature`, `certificate` and `stdin` ARE templated (`:208,:276,:280,
#    :217`). GoReleaser would have tried to execute a file literally named
#    `{{ .Env.OLIVARES_COSIGN_BIN }}`. `goreleaser check` validates structure, not
#    execution — a clean check is not evidence the command can run. `args` IS templated and
#    a literal path needs no templating at all, so the wrapper goes in `args` and `cmd:` is
#    the literal `bash`.
#
# 2. A PATHNAME IS NOT AN EXECUTABLE IDENTITY. `assert-cosign-binary.sh` hashes the file
#    once, at the top of the job, and exports its path. Between that moment and the actual
#    signing there are QEMU, Buildx, registry logins, GoReleaser, uploads and downloads —
#    many of them third-party actions. Exporting a path to `GITHUB_ENV` publishes a NAME to
#    later steps; it does not pin an inode. So this wrapper RE-AUTHENTICATES the bytes
#    immediately before each invocation, through the same digest table.
#
# DECLARED LIMIT, AND IT IS NOT CLOSED. This narrows the window to the interval between the
# re-hash and the `exec` a few microseconds later; it does not eliminate it. Only executing
# the very descriptor that was hashed (`fexecve` or equivalent) would, and that is not
# reachable from POSIX shell. If the threat model must exclude an adversary who can write to
# that path concurrently, as the same UID, inside the runner, then this control does not
# provide it and an out-of-process mechanism is required. Saying so is the point: an
# unverified "cannot happen" is indistinguishable from a hole.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

bin="${OLIVARES_COSIGN_BIN:-}"
if [ -z "$bin" ]; then
	echo "::error::cosign-verified: OLIVARES_COSIGN_BIN is not set." >&2
	echo "scripts/assert-cosign-binary.sh must run earlier in this job: it resolves cosign," >&2
	echo "authenticates its bytes against the upstream published digests, and exports the path." >&2
	echo "Refusing to fall back to whatever 'cosign' PATH resolves to — that fallback would make" >&2
	echo "the verification decorative." >&2
	exit 1
fi
case "$bin" in
/*) ;;
*)
	echo "::error::cosign-verified: OLIVARES_COSIGN_BIN='$bin' is not an absolute path." >&2
	exit 1
	;;
esac

# Re-authenticate NOW, against the same reviewed table. --quiet keeps the release log
# readable when this runs once per artifact; a failure is never quiet.
if ! bash "$ROOT/scripts/assert-cosign-binary.sh" --check-path "$bin" --quiet; then
	echo "::error::cosign-verified: $bin no longer matches an approved published digest." >&2
	echo "It was authenticated earlier in this job, so its bytes changed since. Nothing is signed." >&2
	exit 1
fi

# --- SIGNING MODE (release-rehearsal design §A.2.7/§C.2). The key/no-tlog behaviour is
# centralized HERE, in the one launcher every signer already routes through, so a later
# attestation cannot forget the rehearsal flags. COSIGN_MODE classifies the caller:
#   keyless (default)  — production behaviour, byte-for-byte what this wrapper always did.
#   key                — rehearsal: every signing-capable subcommand gets the per-run key
#                        AND `--tlog-upload=false` APPENDED, so no project-owned signature
#                        can create a public transparency-log record. Appended LAST because
#                        pflag booleans are last-occurrence-wins (measured) — a stray
#                        earlier `--tlog-upload=true` loses to ours, never the reverse. The
#                        `=false` form is mandatory: the space form does NOT bind (measured: `--tlog-upload false` signs the literal file `false`).
# Non-signing subcommands (verify*, copy, generate-key-pair, …) pass through unchanged in
# both modes: verification never needs the private key injected, and `copy` must keep the
# exact signatures it is moving. Measured with cosign v2.6.4 (2026-07-30): sign-blob/
# attest-blob with `--key` + `--tlog-upload=false` exit 0, write signature/bundle, create
# no tlog entry, and verify offline with the public half; a `--output-certificate` flag in
# key mode is accepted and simply writes no certificate.
case "${COSIGN_MODE:-keyless}" in
keyless) ;;
key)
	case "${1:-}" in
	sign | sign-blob | attest | attest-blob)
		if [ -z "${COSIGN_KEY:-}" ]; then
			echo "::error::cosign-verified: COSIGN_MODE=key but COSIGN_KEY is unset. Refusing to sign." >&2
			exit 1
		fi
		case "$COSIGN_KEY" in
		/*) ;;
		*)
			echo "::error::cosign-verified: COSIGN_KEY='$COSIGN_KEY' is not an absolute path." >&2
			exit 1
			;;
		esac
		if [ ! -f "$COSIGN_KEY" ]; then
			echo "::error::cosign-verified: COSIGN_KEY '$COSIGN_KEY' does not exist." >&2
			exit 1
		fi
		# Fail-closed: key mode exists to sign WITHOUT a public record. Any other
		# declared tlog intent is a misconfiguration, not a preference to honour.
		if [ "${COSIGN_TLOG_UPLOAD:-}" != "false" ]; then
			echo "::error::cosign-verified: COSIGN_MODE=key requires COSIGN_TLOG_UPLOAD=false (got '${COSIGN_TLOG_UPLOAD:-}'). Key mode must never create a transparency-log entry." >&2
			exit 1
		fi
		subcmd="$1"
		shift
		set -- "$subcmd" "$@" --key "$COSIGN_KEY" --tlog-upload=false
		;;
	esac
	;;
*)
	echo "::error::cosign-verified: COSIGN_MODE='${COSIGN_MODE}' is not a mode. Only 'keyless' and 'key' exist." >&2
	exit 1
	;;
esac

exec "$bin" "$@"
