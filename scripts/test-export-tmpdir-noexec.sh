#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# test-export-tmpdir-noexec.sh — the noexec preflight in export-public.sh, both ways.
#
# Why a test for four lines: the guard it replaces was WORSE THAN ABSENT. It carried the
# right warning on `go build`, and `go build` SUCCEEDS on a noexec mount, so it never fired
# and the warning never printed. A guard placed on the wrong operation reads, to anyone
# skimming, exactly like a guard that works.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# export-closure: hub-only scripts/export-public.sh — the exporter itself is in SCRIPTS_BLOCK
# and deliberately does not travel: the public tree is the RESULT of running it, so a copy
# there would be a second implementation that can drift. This battery is the exporter's own
# test and only ever runs in the hub, so the reference is hub-only by construction.
# export-closure: hub-only scripts/export-public.sh — the exporter is in SCRIPTS_BLOCK and
# deliberately does not travel: the public tree is the RESULT of running it, so a copy there
# would be a second implementation that can drift. Declared AND guarded at THIS call site,
# not once at the top: an early return can be removed later, leaving the call bare, and the
# published tree would exit 127 instead of naming the reason.
if [ -f "$ROOT/scripts/export-public.sh" ]; then
  SUT="$ROOT/scripts/export-public.sh"
else
  SUT=""
fi
pass=0; fail=0
ok()  { printf 'ok    %-58s %s\n' "$1" "${2:-}"; pass=$((pass+1)); }
bad() { printf 'FAIL  %-58s %s\n' "$1" "${2:-}"; fail=$((fail+1)); }

# NEGATIVE control: a noexec TMPDIR must be refused, with rc 2 and the cause named.
# Take the first noexec mount that is actually WRITABLE, not just the first noexec mount.
# An earlier draft stopped at the first match — often /proc or a read-only one — and then
# reported "cannot measure" on a box where /tmp is noexec and writable. A SKIP that is
# wrong is worse than a failure: it looks like the box's limitation, not the test's.
# The predicate is "noexec AND usable as TMPDIR", not "noexec AND writable". An earlier
# draft stopped at /dev/mqueue — writable and noexec, but `mktemp -d` there fails outright
# with "Operation not permitted". The battery then measured a DIFFERENT failure (rc=1, no
# mount named) and reported the preflight broken while it worked correctly on /tmp.
noexec_dir=""
while read -r _t _o; do
  case ",$_o," in *,noexec,*) ;; *) continue ;; esac
  [ -d "$_t" ] && [ -w "$_t" ] || continue
  _probe="$(mktemp -d --tmpdir="$_t" 2>/dev/null)" || continue
  rmdir "$_probe" 2>/dev/null
  noexec_dir="$_t"; break
done < <(findmnt -rno TARGET,OPTIONS 2>/dev/null)
if [ -z "$noexec_dir" ]; then
  printf 'SKIP  %-58s %s\n' "no writable noexec mount on this box" "cannot measure"
else
  # export-closure: hub-only scripts/export-public.sh — see above; guarded with `if`, never
  # `[ -f X ] && cmd`, because a list ending in && with a false left side returns non-zero and
  # under `set -e` would kill the battery with a red that is not the test's.
  if [ ! -f "$SUT" ]; then
    printf 'SKIP  %-58s %s\n' "the exporter is not in this tree (hub-only)" "cannot measure"
    rc=2; out="hub-only dependency absent"
  else
  out="$(TMPDIR="$noexec_dir" bash "$SUT" --check 2>&1)"; rc=$?
  fi
  [ "$rc" = 2 ] && ok "a noexec TMPDIR exits 2, not 1" "rc=$rc" \
                || bad "a noexec TMPDIR exits 2, not 1" "rc=$rc"
  case "$out" in
    *"is mounted noexec"*) ok "and NAMES the mount, not chmod" ;;
    *) bad "and NAMES the mount, not chmod" "said: $(printf '%s' "$out" | head -1 | cut -c1-70)" ;;
  esac
  case "$out" in
    *"task lint:export"*) ok "and tells the operator what to run instead" ;;
    *) bad "and tells the operator what to run instead" ;;
  esac
fi

# POSITIVE control: an executable TMPDIR must NOT trip the preflight. Without this, a guard
# that refuses everything would pass every case above.
#
# It is deliberately time-boxed instead of run to completion. `--check` performs the whole
# export and takes minutes; this battery only asks whether the PREFLIGHT fired, and the
# preflight is the first thing the script does. So: run it briefly, kill it, and assert the
# noexec message never appeared. Killing it is the expected outcome here, not a failure —
# measuring a 4-line guard must not cost a full export on a box other lanes are pushing on.
exec_tmp="$ROOT/.export-tmp-selftest"; mkdir -p "$exec_tmp"
# export-closure: hub-only scripts/export-public.sh — see above. Same guard, same reason.
if [ ! -f "$SUT" ]; then
  printf 'SKIP  %-58s %s\n' "the exporter is not in this tree (hub-only)" "cannot measure"
  out="hub-only dependency absent"
else
out="$(TMPDIR="$exec_tmp" timeout 25 bash "$SUT" --check 2>&1)"
fi
case "$out" in
  *"is mounted noexec"*) bad "an executable TMPDIR is NOT refused" "the preflight fired on a good dir" ;;
  *) ok "an executable TMPDIR is NOT refused" "got past the preflight" ;;
esac
rm -rf "$exec_tmp"

printf '\ntest-export-tmpdir-noexec: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
