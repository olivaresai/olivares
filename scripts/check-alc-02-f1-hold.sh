#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ALC-02-F1: backup/restore of the plane does not start. C04 unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-alc-02-f1-hold: FAIL — $*" >&2; exit 1; }
cannot() { say "check-alc-02-f1-hold: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ALC02F1_JSON:-design/alc-02-f1-hold.json}"
DOC="${OLIVARES_ALC02F1_DOC:-design/ALC-02-F1-HOLD-2026-08-20.md}"
AWS="${OLIVARES_ALC02F1_AWS:-deploy/aws}"
README="${OLIVARES_ALC02F1_README:-$AWS/README.md}"
VERS="${OLIVARES_ALC02F1_VERS:-$AWS/versions.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -d "$AWS" ] || cannot "missing AWS estate directory"
[ -f "$README" ] || cannot "missing AWS README"
[ -f "$VERS" ] || cannot "missing AWS versions.tf"

grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'NO APLICADO' "$DOC" || fail "$DOC lost NO APLICADO"
if grep -qiE 'tofu apply ran|applied the estate|F1 started|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

grep -q 'NEVER APPLIED' "$README" || fail "$README lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$VERS" || fail "$VERS lost NEVER APPLIED"
grep -q 'NeverApplied' "$VERS" || fail "$VERS lost NeverApplied tag"

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "alc-02-f1-hold/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("c04_applied") is not False:
    raise SystemExit("c04_applied must stay false")
if data.get("f1_started") is not False:
    raise SystemExit("f1_started must stay false")
if data.get("backend_present") is not False:
    raise SystemExit("backend_present must stay false")
if data.get("backend_partial") is not True:
    raise SystemExit("backend_partial must stay true")
if data.get("tfstate_present") is not False:
    raise SystemExit("tfstate_present must stay false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

# Partial `backend "s3" {}` is C04-02 scaffolding (`tofu init -backend=false`).
# A filled bucket/key/region is a remote that can receive state.
python3 - "$AWS" <<'PY' || fail "configured terraform backend while C04 is unapplied"
import os, re, sys
root = sys.argv[1]
configured = []
pat = re.compile(r'backend\s+"([^"]+)"\s*\{', re.M)
assign = re.compile(r'\b(bucket|key|region|dynamodb_table)\s*=')
for dirpath, _, files in os.walk(root):
    for name in files:
        if not name.endswith(".tf"):
            continue
        path = os.path.join(dirpath, name)
        text = open(path, encoding="utf-8").read()
        for m in pat.finditer(text):
            i = m.end()
            depth = 1
            j = i
            while j < len(text) and depth:
                if text[j] == "{":
                    depth += 1
                elif text[j] == "}":
                    depth -= 1
                j += 1
            body = text[i:j]
            if assign.search(body):
                configured.append("%s: backend %r is filled" % (os.path.relpath(path, root), m.group(1)))
if configured:
    raise SystemExit("\n".join(configured))
print("backend-partial-ok")
PY

tfstates="$(find "$AWS" -name '*.tfstate' -print 2>/dev/null || true)"
if [ -n "$tfstates" ]; then
	fail "tfstate present while C04 is unapplied"
fi

say "check-alc-02-f1-hold: CLEAN — C04 unapplied; F1 backup/restore does not start."
exit 0
