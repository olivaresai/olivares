#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C02 variant-filename unique leftover unique vs #1108 (original OPEN
# product PR; no original check on origin/main). 0 CLEAN · 1 finding
# · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-variant-filename-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-variant-filename-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C02VARP_JSON:-design/c02-variant-filename-prep-2026-08-20.json}"
DOC="${OLIVARES_C02VARP_DOC:-design/C02-VARIANT-FILENAME-PREP-2026-08-20.md}"
ART="${OLIVARES_C02VARP_ART:-commercial/license-worker/src/download/artifacts.ts}"

for f in "$JSON" "$DOC" "$ART"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `#1108`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1108"
grep -F -q 'Unique leftover unique vs `hub-comercio/c02-variant-filename`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original branch"
grep -F -q 'C02-02 filename stays N' "$DOC" \
  || fail "prepare doc lost C02-02 N HOLD"
grep -F -q 'Does not write core/release' "$DOC" \
  || fail "prepare doc lost core/release HOLD"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|variant landed in the worker filename' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

grep -q 'export function artifactKey(version: string, os: string, arch: string, set: string)' "$ART" \
  || fail "artifactKey is no longer 4-arg set-keyed"
grep -q 'isAllowedSetSlug(set)' "$ART" \
  || fail "artifactKey lost the set allowlist"
grep -q 'enterprise/${version}/${set}/' "$ART" \
  || fail "set dimension dropped from the R2 key"
grep -q 'export function artifactFilename(version: string, os: string, arch: string): string' "$ART" \
  || fail "artifactFilename 3-arg basename drifted"
if grep -q 'variant = ""' "$ART"; then
  fail "variant default landed — this HOLD lote does not apply #1108"
fi
if grep -q 'export function artifactFilename(version: string, os: string, arch: string, variant' "$ART"; then
  fail "variant in worker filename — this HOLD lote does not apply #1108"
fi
if grep -q 'from "core/release"' "$ART" || grep -q 'ExpectedArtifactName' "$ART"; then
  fail "worker artifacts.ts reached into core/release — C02-02 filename stays N's"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c02-variant-filename-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c02-variant-filename-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c02-variant-filename-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("artifact_key_set_dimension") is not True:
    fail("artifact_key_set_dimension must stay true")
if data.get("variant_in_worker_filename") is not False:
    fail("variant_in_worker_filename must stay false")
if data.get("core_release_written") is not False:
    fail("core_release_written must stay false")
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

say "check-c02-variant-filename-prep: CLEAN — artifactKey 4-arg set-keyed; worker filename stays three-part; C02-02 filename stays N's."
exit 0
