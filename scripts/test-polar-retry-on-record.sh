#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2; pwd)"
CHECK="$ROOT/scripts/check-polar-retry-on-record.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/polar-retry.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/scripts" \
    "$TMP/tree/commercial/license-worker/test" \
    "$TMP/tree/core/license/testdata"
  cp -a "$ROOT/commercial/license-worker/src" \
    "$TMP/tree/commercial/license-worker/"
  cp "$ROOT/commercial/license-worker/package.json" \
    "$TMP/tree/commercial/license-worker/"
  cp "$ROOT/commercial/license-worker/test/webhook.test.ts" \
    "$ROOT/commercial/license-worker/test/fakes.ts" \
    "$TMP/tree/commercial/license-worker/test/"
  cp "$ROOT/core/license/testdata/wireformat_vectors.json" \
    "$TMP/tree/core/license/testdata/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-polar-retry-on-record.sh"
}

run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" \
    bash "$TMP/tree/scripts/check-polar-retry-on-record.sh" \
    >"$TMP/out" 2>"$TMP/err" || rc=$?
  printf '%s\n' "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
  ok "live persisted-licence behavior is CLEAN"
else
  bad "live behavior rc=$(cat "$TMP/rc"), want 0 ($(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/webhook.ts" <<'PY'
import sys
p = sys.argv[1]
text = open(p, encoding="utf-8").read()
old = "          blob: authoritative.blob,\n"
new = '          blob: "counterfactual-not-the-record",\n'
if text.count(old) != 1:
    raise SystemExit("authoritative delivery argument is not unique")
open(p, "w", encoding="utf-8").write(text.replace(old, new, 1))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && \
   grep -Fq 'the retry must mail the licence ON RECORD' "$TMP/err"; then
  ok "mutant: a compilable wrong delivery blob is killed by observed bytes"
else
  bad "wrong delivery blob rc=$(cat "$TMP/rc"), want behavioral 1 ($(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/license-worker/test/webhook.test.ts" <<'PY'
import sys
p = sys.argv[1]
text = open(p, encoding="utf-8").read()
old = 'test("the RETRY mails the licence on record, not a freshly re-signed one"'
new = 'test("renamed retry witness"'
if text.count(old) != 1:
    raise SystemExit("named retry witness is not unique")
open(p, "w", encoding="utf-8").write(text.replace(old, new, 1))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -Fq 'did not execute' "$TMP/err"; then
  ok "mutant: removing the named behavioral witness is killed"
else
  bad "renamed witness rc=$(cat "$TMP/rc"), want missing-witness 1 ($(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/webhook.ts" <<'PY'
import sys
p = sys.argv[1]
text = open(p, encoding="utf-8").read()
old = "        const authoritative = inserted ? record : (await store.getLicenseById(record.id)) ?? record;\n"
new = """        const authoritative = inserted
          ? record
          : (await store.getLicenseById(record.id)) ?? record;
"""
if text.count(old) != 1:
    raise SystemExit("authoritative read-back assignment is not unique")
open(p, "w", encoding="utf-8").write(text.replace(old, new, 1))
PY
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
  ok "no-fire: equivalent TypeScript formatting preserves behavior"
else
  bad "format-only source change rc=$(cat "$TMP/rc"), want 0 ($(cat "$TMP/err"))"
fi

stage
rm "$TMP/tree/commercial/license-worker/test/webhook.test.ts"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
  ok "missing behavioral witness is COULD NOT LOOK"
else
  bad "missing witness rc=$(cat "$TMP/rc"), want 2 ($(cat "$TMP/err"))"
fi

stage
rm "$TMP/tree/commercial/license-worker/test/fakes.ts"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
  ok "missing focused-test dependency is COULD NOT LOOK"
else
  bad "missing test dependency rc=$(cat "$TMP/rc"), want 2 ($(cat "$TMP/err"))"
fi

printf 'test-polar-retry-on-record: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
