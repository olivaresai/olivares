#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: plane S3 owner-enforced + abort incomplete MPU.
# Estate unapplied. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-s3-mpu: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-s3-mpu: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04MPU_JSON:-design/c04-s3-mpu.json}"
DOC="${OLIVARES_C04MPU_DOC:-design/C04-S3-MPU-2026-08-20.md}"
TF="${OLIVARES_C04MPU_TF:-deploy/aws/modules/data/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$TF" ] || cannot "missing data terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$TF" || fail "data module lost NEVER APPLIED"
grep -q 'abort incomplete MPU' "$DOC" || fail "$DOC lost abort-MPU"
if grep -qiE 'estate applied|FIRMA A claimed|current objects expire' "$DOC"; then
	fail "$DOC claims an apply or expiry this lote does not have"
fi

python3 - "$JSON" "$TF" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-s3-mpu/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("bucket_owner_enforced") is not True:
    raise SystemExit("bucket_owner_enforced must be true")
if data.get("abort_incomplete_days") != 7:
    raise SystemExit("abort_incomplete_days must stay 7")
if data.get("expires_current_objects") is not False:
    raise SystemExit("expires_current_objects must stay false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

tf = open(sys.argv[2], encoding="utf-8").read()
if not re.search(r'resource\s+"aws_s3_bucket_ownership_controls"\s+"plane"', tf):
    raise SystemExit("ownership controls missing")
if not re.search(r'object_ownership\s*=\s*"BucketOwnerEnforced"', tf):
    raise SystemExit("object_ownership lost BucketOwnerEnforced")
if not re.search(r'resource\s+"aws_s3_bucket_lifecycle_configuration"\s+"plane"', tf):
    raise SystemExit("lifecycle configuration missing")
if not re.search(r"abort_incomplete_multipart_upload", tf):
    raise SystemExit("abort_incomplete_multipart_upload missing")
if not re.search(r"days_after_initiation\s*=\s*7\b", tf):
    raise SystemExit("days_after_initiation lost 7")
if re.search(r"expiration\s*\{", tf):
    raise SystemExit("current-object expiration is not this lote")
PY

say "check-c04-s3-mpu: CLEAN — owner-enforced + abort MPU 7d; estate unapplied."
exit 0
