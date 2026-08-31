#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Polar retry must deliver the licence already persisted for the order.
# 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail

say() { printf '%s\n' "$*"; }
fail() { say "check-polar-retry-on-record: FAIL — $*" >&2; exit 1; }
cannot() { say "check-polar-retry-on-record: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || true)}"
[ -n "$ROOT" ] || cannot "not inside a git work tree"
WORKER="${OLIVARES_POLAR_WORKER_DIR:-$ROOT/commercial/license-worker}"
TEST="$WORKER/test/webhook.test.ts"
SOURCE="$WORKER/src/webhook.ts"
FAKES="$WORKER/test/fakes.ts"
PACKAGE="$WORKER/package.json"
VECTORS="$ROOT/core/license/testdata/wireformat_vectors.json"
WITNESS='the RETRY mails the licence on record, not a freshly re-signed one'

[ -d "$WORKER" ] || cannot "missing $WORKER"
for required in "$TEST" "$SOURCE" "$FAKES" "$PACKAGE" "$VECTORS"; do
  [ -r "$required" ] || cannot "missing $required"
done
command -v node >/dev/null || cannot "no Node.js runtime"

_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base" || cannot "cannot create $_tmp_base"
_out="$(mktemp "$_tmp_base/polar-retry-check.XXXXXX")" \
  || cannot "cannot allocate test output"
trap 'rm -f "$_out"' EXIT

_rc=0
(cd "$WORKER" || exit 2
 node --test --test-reporter=tap \
   --test-name-pattern="^${WITNESS}$" \
   test/webhook.test.ts) >"$_out" 2>&1 || _rc=$?

case "$_rc" in
  0) ;;
  *)
    sed 's/^/check-polar-retry-on-record: /' "$_out" >&2
    if grep -Fq 'the retry must mail the licence ON RECORD' "$_out"; then
      fail "the retry did not deliver the persisted licence"
    fi
    cannot "the focused witness failed before its persisted-blob assertion"
    ;;
esac

if grep -Fxq '1..0' "$_out" || \
   ! grep -Fxq "# Subtest: $WITNESS" "$_out" || \
   ! grep -Fxq "ok 1 - $WITNESS" "$_out"; then
  fail "the named persisted-licence witness did not execute"
fi
grep -Fxq '# tests 1' "$_out" \
  || fail "the focused persisted-licence witness did not run exactly once"
grep -Fxq '# pass 1' "$_out" \
  || fail "the focused persisted-licence witness did not pass"
grep -Fxq '# fail 0' "$_out" \
  || fail "the focused persisted-licence witness reported a failure"

say "check-polar-retry-on-record: CLEAN — the retry mails the persisted blob and expiry."
exit 0
