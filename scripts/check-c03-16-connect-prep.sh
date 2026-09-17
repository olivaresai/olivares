#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C03-16 unique leftover unique vs #969 (original OPEN product PR;
# no original check on origin/main). 0 CLEAN · 1 finding · 2 could not look.
#
# 2026-09-14 (R116 current-source guard): the 2026-08-20 JSON and doc remain a
# dated HOLD record of hub a06ee242a, including connect_landed=false and
# UNKNOWN overlay fields, and are still checked as that record. They do not
# describe the accepted current Worker. The source half used to require the
# handler, import and /connect/ route to be absent; that absence is why
# publication26 refused the live tree. The source half now pins the accepted
# construction instead: index.ts imports handleConnect from ./connect/handler.ts
# and dispatches pathname /connect/ to it; handler.ts exports async
# handleConnect and dispatches the accepted connect routes. Comments are
# stripped before matching, so a comment or an empty named export neither
# satisfies the positive nor rewrites the dated record. This is a structural
# source pin, not a release-readiness claim; runtime tests keep behavioral
# authority.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-16-connect-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-16-connect-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0316P_JSON:-design/c03-16-connect-prep-2026-08-20.json}"
DOC="${OLIVARES_C0316P_DOC:-design/C03-16-CONNECT-PREP-2026-08-20.md}"
IDX="${OLIVARES_C0316P_IDX:-commercial/license-worker/src/index.ts}"
HANDLER="${OLIVARES_C0316P_HANDLER:-commercial/license-worker/src/connect/handler.ts}"

for f in "$JSON" "$DOC" "$IDX" "$HANDLER"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `#969`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #969"
grep -F -q 'Unique leftover unique vs `hub-comercio/c03-16-connect`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original branch"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
grep -F -q '`/connect` not landed' "$DOC" \
  || fail "prepare doc lost /connect HOLD"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|/connect landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

python3 - "$IDX" "$HANDLER" <<'PY' || exit $?
import re, sys

def fail(msg):
    print(f"check-c03-16-connect-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c03-16-connect-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

def code(path):
    try:
        text = open(path, encoding="utf-8").read()
    except Exception as e:
        cannot(f"source not readable: {e}")
    # Consume quoted literals and escapes before recognizing comments. This
    # preserves URLs and escaped slashes in the current source, while calls
    # quoted in either whole-line or inline comments cannot satisfy a pin.
    # This lexical source guard does not establish TypeScript reachability.
    tokens = re.compile(
        r'''\\[\s\S]|"(?:\\[\s\S]|[^"\\])*"|'(?:\\[\s\S]|[^'\\])*'|`(?:\\[\s\S]|[^`\\])*`|(?P<comment>//[^\r\n]*|/\*[\s\S]*?\*/)'''
    )
    return tokens.sub(lambda match: " " if match.group("comment") is not None else match.group(), text)

idx, handler = (code(p) for p in sys.argv[1:3])

def need(src, name, needle, why):
    n = src.count(needle)
    if n != 1:
        fail(f"{why} ({name}: expected 1 of {needle!r}, found {n})")

need(
    idx,
    "index.ts",
    'import { handleConnect } from "./connect/handler.ts";',
    "current index.ts does not import handleConnect from ./connect/handler.ts",
)
need(
    idx,
    "index.ts",
    'url.pathname.startsWith("/connect/")',
    "current index.ts does not dispatch pathname /connect/",
)
need(
    idx,
    "index.ts",
    "return await handleConnect(env, request, store, nowSec());",
    "current index.ts /connect/ route does not call handleConnect",
)
need(
    handler,
    "connect/handler.ts",
    "export async function handleConnect(env: Env, request: Request, store: Store, nowSec: number): Promise<Response>",
    "current connect/handler.ts does not export the accepted async handleConnect signature",
)
for needle, route in (
    ("return await handleChallenge(env, request, store, nowSec);", "/connect/challenges"),
    ("return await handleDeployments(env, request, store, nowSec);", "/connect/deployments"),
    ("return await handleRefresh(env, request, store, nowSec);", "/connect/refresh"),
    ("return await handleRotate(env, request, store, nowSec, rotate[1]);", "rotate-key"),
    ("return await handleDelete(env, request, store, nowSec, del[1]);", "DELETE /connect/deployments/:id"),
):
    need(
        handler,
        "connect/handler.ts",
        needle,
        "current connect/handler.ts is a stub: handleConnect does not dispatch " + route,
    )
print("source-ok")
PY

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c03-16-connect-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c03-16-connect-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c03-16-connect-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("connect_landed") is not False:
    fail("connect_landed must stay false")
if data.get("remainder_applied") is not False:
    fail("remainder_applied must stay false")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)
print("json-ok")
PY

say "check-c03-16-connect-prep: CLEAN — the 2026-08-20 /connect HOLD record is intact; current source imports handleConnect, dispatches /connect/ to the non-stub handler; overlay remasure not in this gate."
exit 0
