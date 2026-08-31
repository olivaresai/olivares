#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# CFG-03 remainder: a panel secret is a version nobody deploys.
# Does not run wrangler. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-cfg03-secret-deploy: FAIL — $*" >&2; exit 1; }
cannot() { say "check-cfg03-secret-deploy: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_CFG03S_JSON:-design/cfg-03-secret-deploy.json}"
DOC="${OLIVARES_CFG03S_DOC:-design/CFG-03-SECRET-DEPLOY-2026-08-20.md}"
WF="${OLIVARES_CFG03S_WF:-commercial/license-worker/wrangler.jsonc}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$WF" ] || cannot "missing wrangler config"

grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'NO IMPLEMENTADO' "$DOC" || fail "$DOC lost NO IMPLEMENTADO"
grep -q 'version nobody deploys' "$DOC" || fail "$DOC lost the panel-secret fact"
grep -q 'does not run' "$DOC" || fail "$DOC no longer refuses wrangler"
if grep -qiE 'C-16 complete|secret list ran|production deploy executed' "$DOC"; then
	fail "$DOC claims a live C-16 this lote does not have"
fi

python3 - "$JSON" "$WF" <<'PY' || fail "JSON flags or wrangler bindings drifted"
import json, re, sys

def fail(msg):
    print("check-cfg03-secret-deploy: FAIL — %s" % msg, file=sys.stderr)
    raise SystemExit(1)

def cannot(msg):
    print("check-cfg03-secret-deploy: COULD NOT LOOK — %s" % msg, file=sys.stderr)
    raise SystemExit(2)

def strip_jsonc(s):
    out = []
    i, n = 0, len(s)
    in_str = esc = False
    while i < n:
        c = s[i]
        if in_str:
            out.append(c)
            if esc:
                esc = False
            elif c == "\\":
                esc = True
            elif c == '"':
                in_str = False
            i += 1
            continue
        if c == '"':
            in_str = True
            out.append(c)
            i += 1
            continue
        if c == "/" and i + 1 < n and s[i + 1] == "/":
            while i < n and s[i] != "\n":
                i += 1
            continue
        out.append(c)
        i += 1
    return "".join(out)

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("live_secret_list") is not False:
    fail("live_secret_list must stay false")
if data.get("production_deployed") is not False:
    fail("production_deployed must stay false")
if data.get("c16_complete") is not False:
    fail("c16_complete must stay false")
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        fail("%s is not a 40-hex object id" % key)

raw = open(sys.argv[2], encoding="utf-8").read()
try:
    doc = json.loads(strip_jsonc(raw))
except Exception as e:
    cannot("wrangler config is not JSON after comment strip: %s" % e)

banned = ("PORTAL_SESSION_SECRET", "LICENSE_SIGNING_KEY", "DODO_WEBHOOK_SECRET",
          "DOWNLOAD_TOKEN_SECRET", "RESEND_API_KEY", "CLOUD_CP_API_KEY",
          "POLAR_WEBHOOK_SECRET")

def scan_vars(block, who):
    vars_ = (block or {}).get("vars") or {}
    for name in banned:
        if name in vars_:
            fail("%s vars declares %s — a var shadows the secret" % (who, name))

scan_vars(doc, "top-level")
envs = doc.get("env") or {}
for name, block in envs.items():
    scan_vars(block, "env.%s" % name)

prod = ((envs.get("production") or {}).get("vars") or {})
if not prod:
    fail("env.production.vars missing")
got = prod.get("FULFILLMENT_ENABLED")
if got != "false":
    fail("production FULFILLMENT_ENABLED is %r, want 'false'" % got)

need = (
    "LICENSE_SIGNING_KEY",
    "DODO_WEBHOOK_SECRET",
    "DOWNLOAD_TOKEN_SECRET",
    "PORTAL_SESSION_SECRET",
    "RESEND_API_KEY",
    "CLOUD_CP_API_KEY",
)
for name in need:
    if name not in raw:
        fail("wrangler comments lost named secret %s" % name)
PY

say "check-cfg03-secret-deploy: CLEAN — secrets stay off vars; C-16 live list unrun; production off."
exit 0
