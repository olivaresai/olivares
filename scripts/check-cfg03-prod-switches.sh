#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-cfg03-prod-switches.sh — CFG-03. Production sale switches stay
# off. This lote inventories; it does not flip. Three answers.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-cfg03-prod-switches: FAIL — $*" >&2; exit 1; }
cannot() { say "check-cfg03-prod-switches: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

DOC=design/CFG-03-PRODUCTION-SWITCHES-2026-08-18.md
WF=commercial/license-worker/wrangler.jsonc
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$WF" ]  || cannot "missing $WF"

grep -q 'does not authorize' "$DOC" \
  || fail "inventory no longer says it does not flip production"
grep -q 'FULFILLMENT_ENABLED' "$DOC" \
  || fail "inventory lost the go-live switch"

python3 - "$WF" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-cfg03-prod-switches: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)
def cannot(msg):
    print(f"check-cfg03-prod-switches: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

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

raw = open(sys.argv[1], encoding="utf-8").read()
try:
    doc = json.loads(strip_jsonc(raw))
except Exception as e:
    cannot(f"wrangler.jsonc is not JSON after comment strip: {e}")

envs = (doc.get("env") or {})
prod = ((envs.get("production") or {}).get("vars") or {})
sand = ((envs.get("sandbox") or {}).get("vars") or {})
if not prod:
    fail("env.production.vars missing")

def want(block, key, value, who):
    got = block.get(key)
    if got != value:
        fail(f"{who} {key} is {got!r}, want {value!r}")

want(prod, "FULFILLMENT_ENABLED", "false", "production")
want(prod, "CLOUD_FORWARD_ENABLED", "false", "production")
want(prod, "ENTERPRISE_VERSION", "0.0.0", "production")
want(prod, "EXPIRED_SECURITY_PATCH_ACCESS", "false", "production")
# Polar is plan B, not live. A silent provider swap would issue
# against the residual MoR. ISSUER_PURPOSE is the other fence the
# inventory named and the previous check did not pin.
want(prod, "COMMERCE_PROVIDER", "dodo", "production")
want(prod, "ISSUER_PURPOSE", "production", "production")
# Sandbox may be on; this lote does not touch it.
if sand.get("FULFILLMENT_ENABLED") not in ("true", "false"):
    fail(f"sandbox FULFILLMENT_ENABLED is {sand.get('FULFILLMENT_ENABLED')!r}")
print("check-cfg03-prod-switches: CLEAN — production sale switches off; inventory does not flip.")
PY
