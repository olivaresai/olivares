#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-build-bin-parity.sh — the engine binary is compiled ONE way outside a
# release. This asserts that, two ways, and it exists because the property failed
# silently for as long as nobody looked.
#
# WHAT FAILED (2026-08-09, on a real first-run debug). Six places compiled
# ./cmd/olivares into the canonical artifact path bin/olivares, each writing the
# flag set out longhand, and FIVE had drifted from Taskfile.yml's build:bin:
# three dropped -trimpath entirely and all five dropped the -X stamps. Measured
# on one box, same commit: the -trimpath-less build embedded 1507 occurrences of
# the repository path and 2236 of the builder's $HOME, which is precisely what
# build:bin's own comment says -trimpath is there to prevent. An ordinary
# `task e2e:web` put that binary at bin/olivares and left it there.
#
# TWO ASSERTIONS, because either alone passes while the defect is present:
#
#   G-1 (grep, static): no `go build` outside scripts/lib/build-bin.sh may write
#       bin/olivares. This is the one that catches a SIXTH caller added tomorrow —
#       the actual failure mode. It cannot be satisfied by fixing today's five.
#
#   G-2 (build, dynamic): the helper's output actually carries the properties.
#       G-1 is a spelling check: a helper that silently stopped passing -trimpath
#       would keep every caller "compliant" and every binary leaky. So compile a
#       throwaway and read the bytes back. Skipped (not failed, and it says so)
#       when the Go toolchain cannot link here — a check that could not look must
#       never report that it looked.
#
# NOT in the push gate: it compiles the engine, which is minutes, not the seconds
# a fast lint may cost. It belongs to the release run beside test:console-walk.
#
# Usage: bash scripts/test-build-bin-parity.sh
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
# This script pairs `mktemp -d` with git, which makes it a member of the class
# lib/git-env.sh exists for — and `task lint:git-env` caught it the first time it
# ran, correctly. The hazard is not hypothetical here: G-2 asks git for the commit
# to compare against the stamp, and GIT_DIR OUTRANKS `-C`, so under an exported
# GIT_DIR (every hook, from every linked worktree) that question would be answered
# by a DIFFERENT repository than the one being built. The assertion would then
# compare this tree's binary against another tree's HEAD and fail — or worse, pass.
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
olivares_git_env_isolate
cd "$ROOT"

fails=0
note() { printf '%s\n' "$*"; }
bad() { printf 'FAIL: %s\n' "$*" >&2; fails=$((fails + 1)); }

# --- G-1: nobody hand-rolls a build of the canonical artifact --------------------
note '==> G-1  no raw `go build` writes bin/olivares'
# Match a `go build` whose -o target is the canonical artifact. Judged by REGEX,
# not by fixed substrings, because the first version was a spelling check and the
# contrast demonstrated its evasions: `go  build` (two spaces) and `-o=bin/...`
# both read as ABSENT. It also never opened a Makefile or a Dockerfile.
#
# WHAT IT STILL CANNOT SEE, said plainly rather than implied by silence: a build
# split across a line continuation, an output path held in a variable other than
# BIN, a command assembled from fragments, and any file type not in the list
# below. G-1 raises the cost of drifting; only G-2 reads the artifact.
offenders=""
while IFS= read -r f; do
  [ "$f" = "scripts/lib/build-bin.sh" ] && continue
  [ -f "$f" ] || continue
  # Resolve the script's own BIN= assignment so `-o "$BIN"` is judged by where it
  # actually points; a script whose BIN is a scratch path is not this class.
  binval="$(sed -n 's/^[[:space:]]*BIN=\(.*\)$/\1/p' "$f" | head -1)"
  case "$binval" in
    *bin/olivares*) bin_is_canonical=1 ;;
    *) bin_is_canonical=0 ;;
  esac
  # `go` + whitespace + `build`, then anywhere on the line `-o` followed by `=` or
  # whitespace and an optionally-quoted path ending in bin/olivares.
  hits="$(grep -nE 'go[[:space:]]+build' "$f" \
            | grep -vE '^[0-9]+:[[:space:]]*#' \
            | grep -E -- '-o[[:space:]=]+"?([^"[:space:]]*/)?bin/olivares"?' || true)"
  [ -n "$hits" ] && offenders="${offenders}${f}: ${hits}"$'\n'
  if [ "$bin_is_canonical" = 1 ]; then
    hits="$(grep -nE 'go[[:space:]]+build' "$f" \
              | grep -vE '^[0-9]+:[[:space:]]*#' \
              | grep -E -- '-o[[:space:]=]+"?\$\{?BIN\}?"?' || true)"
    [ -n "$hits" ] && offenders="${offenders}${f}: ${hits}"$'\n'
  fi
done < <(git ls-files 'scripts/*.sh' 'scripts/**/*.sh' Taskfile.yml \
           'Makefile*' '*/Makefile*' 'Dockerfile*' '*/Dockerfile*' '*.mk')

if [ -n "$offenders" ]; then
  bad 'a raw `go build` writes the canonical bin/olivares; source scripts/lib/build-bin.sh instead:'
  printf '%s' "$offenders" >&2
else
  note '    ok — every caller goes through build_olivares_bin'
fi

# --- G-2: the helper's output really carries the properties ----------------------
note '==> G-2  the helper stamps the build and trims the paths'
# THE PROBE DIRECTORY MUST BE ABLE TO *EXECUTE* WHAT WE BUILD, AND `mktemp -d` IS NOT
# ENOUGH. Measured 2026-08-11 by on the box every session here runs on: /tmp is
# mounted noexec (`rw,nosuid,nodev,noexec,…`), so a plain `mktemp -d` puts the probe on a
# filesystem that refuses execve. The build links, `strings` still reads the artifact, and
# then `"$probe/olivares" version 2>/dev/null` is rc=126 "Permission denied" with its
# stderr discarded — indistinguishable, here, from an empty answer.
#
# So this check reported "the built binary produced no output … re-run on a quieter box"
# and exited 2, on an IDLE box, DETERMINISTICALLY. It is in the pre-push fast lints
# (.githooks/pre-push), which means every push from this host was rejected by a gate that
# could never look, and the message sent the reader to load that was not there. Two
# sessions were stuck on it the same afternoon.
#
# The environment fact was already known and already written down — lib/exec-workdir.sh
# has carried it since 2026-08-04 — and this file simply did not use it. That is the part
# worth remembering: the cost was not discovering noexec, it was a second copy of
# "get a temp dir" that the first fix could not reach.
#
# The 2026-08-09 note below about intermittent empty execs on a loaded box is kept: this
# does not disprove it, and a retry is cheap. But noexec is not a flake and must not be
# reported as one — and the failure path below does NOT merely name it, it reads the exit
# code and asks findmnt. The two halves came from two lanes on 2026-08-11 and neither
# subsumes the other: this one PREVENTS the case, that one DIAGNOSES what is left.
# lib/exec-workdir.sh already owns this environment fact and PROVES a candidate can run a
# file before handing it back — checking `test -x` AND execve, because on a noexec mount
# the permission bit reads false too. This is its fourth consumer. Writing a private
# picker here is exactly the drift lib/build-bin.sh's own header was written against: one
# hard-won environment fact, one file.
# shellcheck source=lib/exec-workdir.sh
. "$ROOT/scripts/lib/exec-workdir.sh" || {
  printf '\nbuild-bin parity: cannot source lib/exec-workdir.sh — COULD NOT LOOK. Not a pass.\n' >&2
  exit 2
}
if ! probe="$(olivares_pick_exec_workdir build-bin-parity)"; then
  note '    SKIPPED — no scratch directory on this host has a REAL execute bit, so the'
  note '              artifact could not be run and NOTHING was measured about the stamp.'
  note '              This is noexec, not load: point TMPDIR at an exec-capable path.'
  printf '\nbuild-bin parity: G-2 COULD NOT LOOK (nowhere to execute the probe).\n' >&2
  exit 2
fi
trap 'rm -rf "$probe"' EXIT
# shellcheck source=lib/build-bin.sh
. "$ROOT/scripts/lib/build-bin.sh"
skipped=0
if ! build_olivares_bin "$probe/olivares" >"$probe/build.log" 2>&1; then
  skipped=1
  note "    SKIPPED — the toolchain could not link here; NOTHING was measured, which is not a pass:"
  sed 's/^/      /' "$probe/build.log" >&2 || true
else
  # -trimpath: no absolute build path may survive into the artifact.
  for needle in "$ROOT" "${HOME:-/nonexistent-home}"; do
    [ -n "$needle" ] || continue
    n="$(strings -a "$probe/olivares" | grep -c -- "$needle" || true)"
    if [ "$n" != "0" ]; then
      bad "-trimpath did not take: $n occurrence(s) of '$needle' embedded in the binary"
    fi
  done
  # -X stamps: in a git checkout the build must be able to name its own commit.
  if git -C "$ROOT" rev-parse --short HEAD >/dev/null 2>&1; then
    want="$(git -C "$ROOT" rev-parse --short HEAD)"
    # KEEP THE STDERR AND THE EXIT CODE. `2>/dev/null || true` collapses two
    # different states into the same empty string: an exec the kernel REFUSED and
    # an exec that ran and printed nothing. They have opposite remedies, and the
    # message below used to name only the second one.
    exec_err="$probe/version.err"
    got="$("$probe/olivares" version 2>"$exec_err")" || exec_rc=$?
    exec_rc="${exec_rc:-0}"
    # Retry ONCE before concluding anything, but ONLY for the silent case.
    # Measured 2026-08-09: on a box running three sessions this exec came back
    # empty intermittently while the very same binary printed its version fine a
    # second later. One retry turns a flake into a measurement; two consecutive
    # silences are a real answer, and the branch below still refuses to call that
    # a pass. A REFUSED exec is not a flake — sleeping on it only wastes a second.
    if [ -z "$got" ] && [ "$exec_rc" != 126 ]; then
      sleep 1
      exec_rc=0
      got="$("$probe/olivares" version 2>"$exec_err")" || exec_rc=$?
    fi
    # EMPTY IS NOT "THE STAMP DID NOT TAKE". Measured 2026-08-09 on a loaded box:
    # the build returned 0, the binary existed, and running it produced NOTHING —
    # and this check announced `the -X stamp did not take: says []`, sending the
    # reader to the ldflags for a problem that was never there. An answer we could
    # not obtain is the could-not-look case, and this file already has one.
    #
    # AND EMPTY IS NOT "A BUSY BOX" EITHER — measured 2026-08-11 by another lane.
    # This gate blocked two consecutive pushes advising "re-run on a quieter box"
    # with 7,5 GiB of headroom free and the OOM counter unmoved. The cause was the
    # MOUNT: `mktemp -d` lands under /tmp, this container mounts /tmp `noexec`, and
    # so the binary that had just linked could not be exec'd at all. Same empty
    # string, third distinct cause — hence read the exit code instead of guessing.
    if [ "$exec_rc" = 126 ] || { [ -z "$got" ] && grep -qi 'permission denied' "$exec_err" 2>/dev/null; }; then
      skipped=1
      note "    SKIPPED — the binary BUILT and then could NOT BE EXECUTED (exit $exec_rc):"
      note "              $(head -1 "$exec_err" 2>/dev/null)"
      if command -v findmnt >/dev/null 2>&1; then
        case "$(findmnt -no OPTIONS -T "$probe" 2>/dev/null)" in
          *noexec*) note "              The filesystem holding $probe is mounted NOEXEC. It is not the" ;;
          *)        note "              The mount does not say noexec, so check permissions/ACLs. It is not the" ;;
        esac
      else
        note "              Most likely a noexec scratch filesystem. It is not the"
      fi
      note "              toolchain and not the load: re-run with an exec-able TMPDIR, e.g."
      note "                  TMPDIR=/workspace/.olivares-tmptest bash ${0##*/}"
    elif [ -z "$got" ]; then
      skipped=1
      note "    SKIPPED — the built binary produced no output; NOTHING was measured about the stamp."
      note "              (it linked and it EXECUTED, so this is neither the toolchain nor the"
      note "               mount — re-run on a quieter box)"
    else
      case "$got" in
        *"commit $want"*) : ;;
        *) bad "the -X stamp did not take: \`olivares version\` says [$got], expected commit $want" ;;
      esac
      case "$got" in
        *'built unknown'*) bad "the -X date stamp did not take: \`olivares version\` still says 'built unknown'" ;;
      esac
    fi
  else
    note '    (no git checkout — stamp assertion skipped, -trimpath still checked)'
  fi
  # Only claim ok when something was actually read back. Printing it above a
  # SKIPPED line is how "could not look" gets skimmed as "clean".
  if [ "$fails" = 0 ] && [ "$skipped" = 0 ]; then
    note '    ok — 0 embedded build paths, commit and date stamped'
  fi
fi

if [ "$fails" != 0 ]; then
  printf '\nbuild-bin parity: %d failure(s)\n' "$fails" >&2
  exit 1
fi
# A skipped G-2 must never print as a clean run: "ok" would claim the artifact was
# read back when it was never built. Exit 2 — could not look — not 0.
if [ "$skipped" != 0 ]; then
  # NOT "the binary was never built": that line was false in two of the three ways
  # G-2 can end up empty — it links fine and then either cannot be exec'd, or runs
  # and says nothing. Naming a cause the script did not measure is what sent a
  # reader to the toolchain for a mount option. The reason printed above IS the
  # measurement; this line only refuses to call it clean.
  printf '\nbuild-bin parity: G-1 ok, G-2 COULD NOT LOOK (nothing was read back from the artifact).\n' >&2
  printf 'This is not a clean run. See the reason above and re-run before believing it.\n' >&2
  exit 2
fi
printf '\nbuild-bin parity: ok (G-1 static + G-2 read back from the artifact)\n'
