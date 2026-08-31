#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C02-14 unique leftover unique vs #896 (original OPEN product PR;
# no original check on origin/main). 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-14-audit-label-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-14-audit-label-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0214P_JSON:-design/c02-14-audit-label-prep-2026-08-20.json}"
DOC="${OLIVARES_C0214P_DOC:-design/C02-14-AUDIT-LABEL-PREP-2026-08-20.md}"
ART="${OLIVARES_C0214P_ART:-commercial/license-worker/src/download/artifacts.ts}"

for f in "$JSON" "$DOC" "$ART"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `#896`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #896"
grep -F -q 'Unique leftover unique vs `hub-comercio/c02-14-download-set`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original branch"
grep -F -q 'APPLIED (label only)' "$DOC" \
  || fail "prepare doc lost applied-label pin"
grep -F -q 'downloadAuditLabel landed' "$DOC" \
  || fail "prepare doc lost audit-label applied pin"
grep -F -q 'Does not add legacyMonolithKey' "$DOC" \
  || fail "prepare doc lost unscoped-key HOLD"
grep -F -q 'C02-02 filename stays N' "$DOC" \
  || fail "prepare doc lost C02-02 N HOLD"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|legacyMonolithKey landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

grep -q 'export function artifactKey(version: string, os: string, arch: string, set: string)' "$ART" \
  || fail "artifactKey is no longer 4-arg set-keyed"
grep -q 'export function downloadAuditLabel(version: string, set: string, os: string, arch: string)' "$ART" \
  || fail "downloadAuditLabel is not landed"
if grep -q 'legacyMonolithKey' "$ART"; then
  fail "legacyMonolithKey landed — this lote does not apply unscoped fallback"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c02-14-audit-label-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c02-14-audit-label-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c02-14-audit-label-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("artifact_key_set_dimension") is not True:
    fail("artifact_key_set_dimension must stay true")
if data.get("download_audit_label_landed") is not True:
    fail("download_audit_label_landed must stay true")
if data.get("legacy_monolith_key_landed") is not False:
    fail("legacy_monolith_key_landed must stay false")
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

say "check-c02-14-audit-label-prep: CLEAN — downloadAuditLabel landed; artifactKey 4-arg set-keyed; leftover vs #896; overlay remasure not in this gate."
exit 0
