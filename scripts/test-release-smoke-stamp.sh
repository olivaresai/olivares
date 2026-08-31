#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for the build-metadata leg of scripts/release-smoke.sh (§1, "traceable build
# metadata"). It exercises the leg through the REAL script against a stub binary, because the
# thing under test is the predicate the script applies to `olivares version` output — not the
# product. Every case names which direction it proves; a battery that only feeds it good input
# cannot tell a working check from a check that says yes to everything.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Overridable so the mutation control can point this battery at a pre-cure copy of the script:
# a battery that cannot be run against the defect it describes is a claim, not a control.
SMOKE="${SMOKE_UNDER_TEST:-${ROOT}/scripts/release-smoke.sh}"
# The scratch dir lives inside the repo, not under TMPDIR. This box mounts /tmp noexec, and
# this battery's whole method is executing a stub binary: from /tmp the stub cannot run, the
# smoke rejects every case for a reason that has nothing to do with the leg under test, and
# the nine reject-cases all "pass". Measured, not guessed -- `task lint:release-smoke-stamp`
# printed 9 passed / 3 failed the first time it ran, and only the three accept-cases dissented.
WORK="$(mktemp -d "${ROOT}/.smoke-stamp.XXXXXX")"
trap 'rm -rf "${WORK}"' EXIT
pass=0; fail=0

# Fail-closed self-check: if the stub cannot execute, this battery cannot answer anything, and
# the honest exit is 2 -- "could not look" -- not a red and certainly not a green.
printf '%s\n' '#!/bin/sh' 'echo alive' > "${WORK}/selftest"
chmod +x "${WORK}/selftest" 2>/dev/null || true
if [ "$("${WORK}/selftest" 2>/dev/null || true)" != "alive" ]; then
  echo "release-smoke-stamp: CANNOT LOOK - cannot execute a stub under ${WORK}" >&2
  echo "  this battery works by running a fake binary; without exec permission every case" >&2
  echo "  reads 'rejected' and the negative cases pass for the wrong reason." >&2
  exit 2
fi

# Runs the real smoke against a stub that prints $1 for `version`, and answers whether the
# build-metadata leg accepted it. Only leg 1 is under test: the stub cannot embed plugins, so
# the script always dies later — the verdict is leg 1's own OK line, not the exit code.
leg1_verdict() {
  printf '%s\n' '#!/bin/sh' 'case "$1" in' "  version) echo \"$1\";;" \
    '  --version) echo "olivares version 26.8.0";;' '  *) exit 9;;' 'esac' > "${WORK}/fake"
  chmod +x "${WORK}/fake"
  # No pipeline here, on purpose. Under `set -o pipefail` a `bash smoke | grep -q` reports the
  # SMOKE's status, not grep's match, and the smoke always dies later on the stub's missing
  # plugins -- so every case would read "rejected", including a correct one. The first run of
  # this battery did exactly that: 9 green over a leg that accepts real stamps fine.
  local out
  out="$(bash "${SMOKE}" "${WORK}/fake" 2>&1 || true)"
  case "${out}" in
  *"OK: version/commit/date stamped"*) echo accepted ;;
  *) echo rejected ;;
  esac
}
check() { # <what> <expected> <version string>
  local got; got="$(leg1_verdict "$3")"
  if [ "${got}" = "$2" ]; then pass=$((pass+1)); printf '  ok   %-52s %s\n' "$1" "${got}"
  else fail=$((fail+1)); printf '  FAIL %-52s got %s, want %s\n' "$1" "${got}" "$2"; fi
}

echo "release-smoke stamp leg — cases:"
# Accepts what the pipeline actually produces. Without this the whole battery is satisfiable
# by a leg that rejects everything, which would be a green battery over a dead release.
check "a real stamp is accepted"                accepted 'olivares 26.8.0 (commit 47b745890, built 2026-08-31T02:20:22Z)'
check "a real stamp, date-only form"            accepted 'olivares 26.8.0 (commit 47b745890, built 2026-08-31)'
check "long sha is accepted"                    accepted 'olivares 26.8.0 (commit 47b7458905250fb7dd56, built 2026-08-31T02:20:22Z)'
# The ldflags-less build: the three defaults of cmd/olivares/main.go:36-38.
check "unstamped build (all three defaults)"    rejected 'olivares dev (commit none, built unknown)'
check "commit none"                             rejected 'olivares 26.8.0 (commit none, built 2026-08-31T02:20:22Z)'
check "built unknown"                           rejected 'olivares 26.8.0 (commit 47b745890, built unknown)'
# The ldflags run whose template resolved to nothing. These are what the literal-string checks
# missed; each one alone is a binary the old leg called traceable.
check "empty date"                              rejected 'olivares 26.8.0 (commit 47b745890, built )'
check "empty commit"                            rejected 'olivares 26.8.0 (commit , built 2026-08-31T02:20:22Z)'
check "both empty"                              rejected 'olivares 26.8.0 (commit , built )'
check "Go zero time (measured on the export)"   rejected 'olivares 26.8.0 (commit 47b745890, built 0001-01-01T00:00:00Z)'
check "unexpanded goreleaser template"          rejected 'olivares 26.8.0 (commit {{.ShortCommit}}, built 2026-08-31T02:20:22Z)'
check "a word where a date belongs"             rejected 'olivares 26.8.0 (commit 47b745890, built yesterday)'

echo
echo "release-smoke-stamp: ${pass} passed, ${fail} failed"
[ "${fail}" -eq 0 ]
