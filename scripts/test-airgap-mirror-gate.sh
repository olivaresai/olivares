#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Mutation matrix for the air-gap mirror (scripts/airgap-mirror.sh).
#
# WHY THIS EXISTS. These two scripts are the whole supply-chain story for an air-gapped
# install — the operator carries a tarball across the gap and this decides whether what
# comes out is what was signed. Nothing in the Taskfile or in any workflow runs them, so
# until 2026-08-01 nobody had ever observed them refusing anything, and three of their
# checks did not refuse:
#
#   * A digest that CHANGED while mirroring — the precise event a signature exists to
#     detect, an image substituted in the destination registry — printed
#     "WARN: digest changed (…)" and the script carried on to "OK — mirrored + verified
#     offline", exit 0.
#   * A bundle whose chart had NO .tgz.sig skipped verification entirely (the check sat
#     inside `if [ -f "${TGZ}.sig" ]`), pushed the chart to the private registry anyway,
#     and closed with the same OK banner. The ABSENCE of a signature read as success.
#     It composes with the bundler, which signed with `>/dev/null 2>&1` and announced
#     "(+ .sig)" from the fact that the command returned rather than from the file.
#   * A bundle with no images/ entries left the verification loop with nothing to
#     iterate over: zero images verified, and the same OK banner.
#
# Every tool is stubbed, so this is fast and needs no registry, no network and no keys.
# The stub directory cannot live under /tmp — that mount is noexec here.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$ROOT/scripts/airgap-mirror.sh"

# The stubbed cosign/crane/helm are EXECUTED off PATH, so this needs a location where
# execve works — and no such path can be assumed. The dev container mounts /tmp noexec;
# the CI runners have no /workspace at all. Baking either one in breaks the other: a
# hardcoded `/workspace/.olivares-tmptest` in the sibling matrix took down a required job
# with "mkdir: cannot create directory '/workspace': Permission denied". So each
# candidate is created, given a real script, and the script is RUN. Kept local rather
# than shared with scripts/test-gates-failclosed.sh: a battery that proves containment
# should not acquire a dependency on another battery to start up.
pick_exec_dir() {
	local base d
	for base in "${RUNNER_TEMP:-}" "${TMPDIR:-}" /workspace/.olivares-tmptest /tmp; do
		[ -n "$base" ] || continue
		mkdir -p "$base" 2>/dev/null || continue
		d="$(mktemp -d "$base/airgap-gate.XXXXXX" 2>/dev/null)" || continue
		if printf '#!/bin/sh\nexit 0\n' >"$d/probe" 2>/dev/null &&
			chmod +x "$d/probe" 2>/dev/null && "$d/probe" 2>/dev/null; then
			rm -f "$d/probe"
			printf '%s' "$d"
			return 0
		fi
		rm -rf "$d"
	done
	return 1
}

WORK="$(pick_exec_dir)" || {
	echo "test-airgap-mirror-gate: found no directory that permits execve; the stubbed" >&2
	echo "  cosign/crane/helm cannot be installed." >&2
	exit 2
}
mkdir -p "$WORK/bin" || exit 1
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

pass=0
fail=0

GOOD_DIGEST="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
BAD_DIGEST="sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
REF="registry.example.com/olivares@${GOOD_DIGEST}"

stub_tools() { # stub_tools <digest-crane-reports> [helm-exit]
	printf '#!/bin/sh\nexit 0\n' >"$WORK/bin/cosign"
	printf '#!/bin/sh\ncase "$1" in digest) echo "%s";; esac\nexit 0\n' "$1" >"$WORK/bin/crane"
	printf '#!/bin/sh\nexit %s\n' "${2:-0}" >"$WORK/bin/helm"
	chmod +x "$WORK/bin/cosign" "$WORK/bin/crane" "$WORK/bin/helm"
}

make_bundle() { # make_bundle <name> <with-images> <with-sig> ; echoes the tarball path
	local name="$1" with_images="$2" with_sig="$3"
	local b="$WORK/$name" root
	rm -rf "$b"
	root="$b/olivares-airgap-vTEST"
	mkdir -p "$root/chart"
	echo "a-public-key" >"$root/cosign.pub"
	: >"$root/digests.txt"
	if [ "$with_images" = yes ]; then
		mkdir -p "$root/images/img00"
		printf '%s\n' "$REF" >"$root/images/img00/ref.txt"
		printf '%s\n' "$REF" >"$root/digests.txt"
	fi
	: >"$root/chart/olivares-1.0.0.tgz"
	[ "$with_sig" = yes ] && : >"$root/chart/olivares-1.0.0.tgz.sig"
	tar -czf "$b.tar.gz" -C "$b" olivares-airgap-vTEST
	printf '%s' "$b.tar.gz"
}

run_mirror() { # run_mirror <tarball> ; sets OUT and RC
	OUT="$(env PATH="$WORK/bin:$PATH" bash "$GATE" --bundle "$1" --registry reg.internal:5000 2>&1)"
	RC=$?
}

red() { # red <name> <expected-substring>
	if [ "$RC" -eq 0 ]; then
		printf 'FAIL  %s: exited 0 — it reported a verified mirror\n' "$1"
		printf '%s\n' "$OUT" | sed 's/^/        /' | tail -6
		fail=$((fail + 1))
		return
	fi
	case "$OUT" in
	*"$2"*)
		printf 'ok    %s (exit %s)\n' "$1" "$RC"
		pass=$((pass + 1))
		;;
	*)
		printf 'FAIL  %s: exit %s but never said %q\n' "$1" "$RC" "$2"
		printf '%s\n' "$OUT" | sed 's/^/        /' | tail -6
		fail=$((fail + 1))
		;;
	esac
}

# --- positive control: a well-formed bundle still mirrors ---------------------------
# Without this row every check below could be satisfied by a script that refuses always.
stub_tools "$GOOD_DIGEST"
run_mirror "$(make_bundle ok yes yes)"
if [ "$RC" -eq 0 ] && case "$OUT" in *"OK — mirrored + verified offline"*) true ;; *) false ;; esac then
	printf 'ok    a well-formed bundle still mirrors (control)\n'
	pass=$((pass + 1))
else
	printf 'FAIL  control: a well-formed bundle no longer mirrors (exit %s)\n' "$RC"
	printf '%s\n' "$OUT" | sed 's/^/        /' | tail -6
	fail=$((fail + 1))
fi

# --- the digest did not survive the mirror ------------------------------------------
stub_tools "$BAD_DIGEST"
run_mirror "$(make_bundle swapped yes yes)"
red "a digest that changed while mirroring is refused" "the mirrored image is NOT the signed image"

# --- the chart carries no signature --------------------------------------------------
stub_tools "$GOOD_DIGEST"
run_mirror "$(make_bundle unsigned yes no)"
red "an unsigned chart is refused, not pushed" "no .sig in the bundle"

# --- the bundle carries no images ----------------------------------------------------
run_mirror "$(make_bundle empty no yes)"
red "a bundle with no images is refused" "no images/ entries"

# --- manifest and payload disagree ----------------------------------------------------
B="$(make_bundle mismatch yes yes)"
printf 'a@sha256:1\nb@sha256:2\n' >"$WORK/mismatch/olivares-airgap-vTEST/digests.txt"
tar -czf "$B" -C "$WORK/mismatch" olivares-airgap-vTEST
run_mirror "$B"
red "a digests.txt that disagrees with images/ is refused" "manifest and the payload disagree"

# --- the push itself fails -------------------------------------------------------------
stub_tools "$GOOD_DIGEST" 1
run_mirror "$(make_bundle pushfail yes yes)"
red "a failed helm push is not reported as a mirror" "helm push failed"

printf '\ntest-airgap-mirror-gate: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
