#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for check-c03-16-connect-prep.sh. The positive fixture copies BOTH
# live index.ts and connect/handler.ts. Every negative asserts exact rc AND the
# phrase of the guard that must fire: an exit 1 for the wrong reason is a
# harness failure.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c03-16-connect-prep.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0316prep.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

LW="commercial/license-worker/src"
JSON_REL="design/c03-16-connect-prep-2026-08-20.json"
DOC_REL="design/C03-16-CONNECT-PREP-2026-08-20.md"
IDX_REL="$LW/index.ts"
HANDLER_REL="$LW/connect/handler.ts"

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts" \
    "$TMP/tree/$LW/connect"
  cp "$ROOT/$JSON_REL" "$TMP/tree/design/"
  cp "$ROOT/$DOC_REL" "$TMP/tree/design/"
  cp "$ROOT/$IDX_REL" "$TMP/tree/$LW/"
  cp "$ROOT/$HANDLER_REL" "$TMP/tree/$LW/connect/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c03-16-connect-prep.sh"
}
run() {
  local rc=0
  unset OLIVARES_ENT_DIR \
    OLIVARES_C0316P_JSON OLIVARES_C0316P_DOC \
    OLIVARES_C0316P_IDX OLIVARES_C0316P_HANDLER || true
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c03-16-connect-prep.sh" \
    >"$TMP/out" 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}
expect() {
  local want="$1" needle="$2" label="$3" got
  run
  got="$(cat "$TMP/rc")"
  if [ "$got" != "$want" ]; then
    bad "$label — rc=$got, want $want ($(head -c 300 "$TMP/err"))"
    return
  fi
  if [ "$want" != 0 ] && ! command grep -qF -- "$needle" "$TMP/err"; then
    bad "$label — rc=$want but the message does not name its guard; got: $(head -c 300 "$TMP/err")"
    return
  fi
  if [ "$want" = 0 ]; then
    if ! command grep -qF -- "2026-08-20 /connect HOLD record" "$TMP/out"; then
      bad "$label — CLEAN stdout lost the dated-record half ($(head -c 300 "$TMP/out"))"
      return
    fi
    if ! command grep -qF -- "non-stub handler" "$TMP/out"; then
      bad "$label — CLEAN stdout lost the current-source half ($(head -c 300 "$TMP/out"))"
      return
    fi
    if command grep -qiE 'release[- ]ready' "$TMP/out"; then
      bad "$label — CLEAN stdout claimed release readiness ($(head -c 300 "$TMP/out"))"
      return
    fi
  fi
  ok "$label"
}
mutate() {
  python3 - "$TMP/tree/$1" "$2" "$3" <<'PY'
import sys
path, needle, replacement = sys.argv[1:4]
text = open(path, encoding="utf-8").read()
if text.count(needle) != 1:
    print(f"mutant did not apply: {needle!r} occurs {text.count(needle)} times in {path}", file=sys.stderr)
    sys.exit(3)
open(path, "w", encoding="utf-8").write(text.replace(needle, replacement))
PY
}
json_flag() {
  python3 - "$TMP/tree/$JSON_REL" "$1" "$2" <<'PY'
import json, sys
p, key, raw = sys.argv[1:4]
d = json.load(open(p, encoding="utf-8"))
if raw == "True":
    d[key] = True
elif raw == "False":
    d[key] = False
else:
    d[key] = raw
json.dump(d, open(p, "w", encoding="utf-8"))
PY
}

stage
expect 0 "" "live current construction and 2026-08-20 prep record are CLEAN"

stage
json_flag remainder_applied True
expect 1 "remainder_applied must stay false" "mutant (remainder-applied) is killed"

stage
json_flag overlay_remeasured_in_this_gate True
expect 1 "overlay remasure leaked" "mutant (overlay remasure leaked) is killed"

stage
json_flag connect_landed True
expect 1 "connect_landed must stay false" "mutant (connect_landed rewritten to true) is killed"

stage
json_flag u_f known
expect 1 "u_f must stay UNKNOWN" "mutant (u_f no longer UNKNOWN) is killed"

stage
json_flag u_d known
expect 1 "u_d must stay UNKNOWN" "mutant (u_d no longer UNKNOWN) is killed"

stage
rm -f "$TMP/tree/$JSON_REL"
expect 2 "missing design/c03-16-connect-prep-2026-08-20.json" "missing JSON is COULD NOT LOOK"

stage
rm -f "$TMP/tree/$HANDLER_REL"
expect 2 "missing commercial/license-worker/src/connect/handler.ts" \
  "missing current handler is COULD NOT LOOK"

stage
rm -f "$TMP/tree/$IDX_REL"
expect 2 "missing commercial/license-worker/src/index.ts" \
  "missing current index is COULD NOT LOOK"

stage
: > "$TMP/tree/$HANDLER_REL"
expect 1 "current connect/handler.ts does not export the accepted async handleConnect signature" \
  "empty handler is a finding"

stage
printf '%s\n' 'export async function handleConnect() {}' > "$TMP/tree/$HANDLER_REL"
expect 1 "current connect/handler.ts does not export the accepted async handleConnect signature" \
  "named empty handleConnect export is a finding"

stage
printf '%s\n' \
  'export async function handleConnect(env: Env, request: Request, store: Store, nowSec: number): Promise<Response> { return connectError(422, "protocol_invalid"); }' \
  > "$TMP/tree/$HANDLER_REL"
expect 1 "current connect/handler.ts is a stub" \
  "signature-only stub handler is a finding"

stage
mutate "$IDX_REL" \
  'import { handleConnect } from "./connect/handler.ts";' \
  '// import { handleConnect } from "./connect/handler.ts";'
expect 1 "current index.ts does not import handleConnect from ./connect/handler.ts" \
  "comment-only handleConnect import is a finding"

stage
mutate "$IDX_REL" \
  'import { handleConnect } from "./connect/handler.ts";' \
  ''
expect 1 "current index.ts does not import handleConnect from ./connect/handler.ts" \
  "removed handleConnect import is a finding"

stage
mutate "$IDX_REL" \
  '        return await handleConnect(env, request, store, nowSec());' \
  '        return json({ error: "disconnected" }, 404);'
expect 1 "current index.ts /connect/ route does not call handleConnect" \
  "disconnected /connect/ route is a finding"

stage
mutate "$IDX_REL" \
  '      if (url.pathname.startsWith("/connect/")) {
        return await handleConnect(env, request, store, nowSec());
      }' \
  ''
expect 1 "current index.ts does not dispatch pathname /connect/" \
  "removed /connect/ route is a finding"

stage
printf '\n// history: no handleConnect import; `/connect` not landed; export async function handleConnect() {}\n' >> \
  "$TMP/tree/$IDX_REL"
printf '\n/* history: return await handleConnect(env, request, store, nowSec()); */\n' >> \
  "$TMP/tree/$HANDLER_REL"
expect 0 "" "no-fire: comments quoting absence or the dispatch stay CLEAN"

stage
for call in \
  'return await handleChallenge(env, request, store, nowSec);' \
  'return await handleDeployments(env, request, store, nowSec);' \
  'return await handleRefresh(env, request, store, nowSec);' \
  'return await handleRotate(env, request, store, nowSec, rotate[1]);' \
  'return await handleDelete(env, request, store, nowSec, del[1]);'; do
  mutate "$HANDLER_REL" "$call" "return connectError(422, \"protocol_invalid\"); // $call"
done
expect 1 "current connect/handler.ts is a stub" \
  "all five dispatch calls only in inline comments are a finding"

stage
mutate "$IDX_REL" \
  'import { handleConnect } from "./connect/handler.ts";' \
  'const disconnected = true; // import { handleConnect } from "./connect/handler.ts";'
expect 1 "current index.ts does not import handleConnect from ./connect/handler.ts" \
  "inline-comment-only import is a finding"

stage
mutate "$IDX_REL" \
  '        return await handleConnect(env, request, store, nowSec());' \
  '        return json({ error: "disconnected" }, 404); // return await handleConnect(env, request, store, nowSec());'
expect 1 "current index.ts /connect/ route does not call handleConnect" \
  "inline-comment-only route call is a finding"

stage
expect 0 "" "no-fire: live construction stays CLEAN"

echo "check-c03-16-connect-prep selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
