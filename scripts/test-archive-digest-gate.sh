#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Regression for the CIRCULAR-BOOTSTRAP hole in the OTA phase-2 job.
#
# The job used to extract and RUN the binary out of the published linux/amd64 archive
# with nothing having checked that archive's digest first, then treat what the binary
# printed as independent verification. Since the expected fingerprints are PUBLIC repo
# variables, a substituted archive could print them and exit 0.
#
# This proves the gate that closes it: scripts/verify-archive-digest.sh rejects a
# substituted archive, and — the part that actually matters — it does so BEFORE the
# archive is opened, so the hostile payload never executes. The test's payload writes
# a marker file when it runs; the assertion is that the marker does NOT exist.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-archive-gate.XXXXXX")"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

# ${TMPDIR:-/tmp} may be mounted noexec (the dev container's /tmp is): stubs then
# die at execve with EACCES — see test-assert-cosign-binary.sh for the measured
# signature. Probe; fall back to a repo-local (exec) tempdir.
printf '#!/bin/sh\nexit 0\n' >"$WORK/.execprobe" && chmod +x "$WORK/.execprobe"
if ! "$WORK/.execprobe" >/dev/null 2>&1; then
	rm -rf "$WORK"
	WORK="$(mktemp -d "$ROOT/.tmpexec.XXXXXX")" || exit 1
fi
rm -f "$WORK/.execprobe"

GATE="$ROOT/scripts/verify-archive-digest.sh"
VERSION="26.8.0"
ARCHIVE="olivares_${VERSION}_linux_amd64.tar.gz"
FIPS="olivares_${VERSION}_fips_linux_amd64.tar.gz"

# Build an archive whose `olivares` binary announces itself by touching a marker —
# this stands in for "the workflow extracted it and ran it".
make_archive() {
  local out="$1" marker="$2" stage
  stage="$(mktemp -d "$WORK/stage.XXXXXX")"
  cat >"$stage/olivares" <<EOF
#!/bin/sh
touch "$marker"
# A substituted binary knows the expected fingerprints: they are PUBLIC repo variables.
echo "olivares $VERSION license-key=release/deadbeef ota-key=release/cafebabe"
echo "OK: stable manifest for $VERSION is bound to the signed checksums"
EOF
  chmod 0755 "$stage/olivares"
  tar -czf "$out" -C "$stage" olivares
  rm -rf "$stage"
}

# The workflow's own sequence: gate, THEN extract, THEN run.
simulate_phase2() {
  local archive="$1"
  bash "$GATE" "$WORK/checksums.txt" "$archive" >/dev/null 2>&1 || return 1
  mkdir -p "$WORK/community"
  tar -xzf "$archive" -C "$WORK/community" olivares
  chmod 0755 "$WORK/community/olivares"
  "$WORK/community/olivares" >/dev/null
}

fail() { echo "FATAL: $*" >&2; exit 1; }

echo "==> building the genuine release archive + the checksums.txt cosign signed"
make_archive "$WORK/$ARCHIVE" "$WORK/ran-genuine"
make_archive "$WORK/$FIPS" "$WORK/ran-fips"
( cd "$WORK" && sha256sum "$ARCHIVE" "$FIPS" > checksums.txt )

echo "==> the genuine archive passes the gate and is then extracted and run"
simulate_phase2 "$WORK/$ARCHIVE" || fail "the genuine archive must pass the gate"
[ -f "$WORK/ran-genuine" ] || fail "test harness bug: the genuine binary never ran"
rm -f "$WORK/ran-genuine" "$WORK/community/olivares"

echo "==> THE ATTACK: the published archive is swapped after checksums.txt was signed"
# checksums.txt is cosign-signed in CI and cannot follow the swap.
make_archive "$WORK/$ARCHIVE" "$WORK/ran-substituted"
if simulate_phase2 "$WORK/$ARCHIVE"; then
  fail "a substituted archive passed phase 2 — the bootstrap is still circular"
fi
[ ! -f "$WORK/ran-substituted" ] || \
  fail "the substituted binary EXECUTED: the gate must reject the archive BEFORE it is opened"
echo "    substituted archive rejected, and its payload never executed"

# A red row must assert WHY it is red, not just that it is. `>/dev/null 2>&1` plus a
# bare `if ... then fail` accepts ANY non-zero exit, and this gate exits non-zero for
# usage errors and missing files too — so deleting the duplicate-entry guard outright,
# or renaming the checksums file, would be indistinguishable from the property holding.
# Every rejection below now names the diagnostic the gate is supposed to produce.
refuses() { # refuses <what> <expected-substring> -- <cmd...>
  local what="$1" want="$2"; shift 3
  local out rc=0
  out="$("$@" 2>&1)" || rc=$?
  if [ "$rc" -eq 0 ]; then
    fail "$what (the gate accepted it)"
  fi
  case "$out" in
    *"$want"*) : ;;
    *) fail "$what — refused with exit $rc but never said '$want'; that is a refusal for the wrong reason:
$out" ;;
  esac
}

echo "==> an archive absent from checksums.txt fails closed"
cp "$WORK/$ARCHIVE" "$WORK/olivares_26.9.0_linux_amd64.tar.gz"
refuses "an archive nothing authenticates must not pass" "is NOT listed in" -- \
  bash "$GATE" "$WORK/checksums.txt" "$WORK/olivares_26.9.0_linux_amd64.tar.gz"

echo "==> the FIPS variant's line may not vouch for the base archive"
# Exact-field matching, not substring: `olivares_<v>_linux_amd64.tar.gz` must never be
# satisfied by the `olivares_<v>_fips_linux_amd64.tar.gz` entry.
( cd "$WORK" && sha256sum "$FIPS" > fips-only.txt )
refuses "the base archive was accepted against the FIPS variant's checksums entry" \
  "is NOT listed in" -- bash "$GATE" "$WORK/fips-only.txt" "$WORK/$ARCHIVE"

echo "==> a duplicated entry is refused rather than resolved last-wins"
( cd "$WORK" && sha256sum "$ARCHIVE" > dup.txt && sed 's/^./0/' dup.txt >> dup.txt )
refuses "a checksums.txt naming one file twice must be refused" \
  "one file, several answers" -- bash "$GATE" "$WORK/dup.txt" "$WORK/$ARCHIVE"

echo "PASS: the phase-2 archive gate binds the artifact before anything executes it"
