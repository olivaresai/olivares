#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: RDS uses a customer-managed KMS key; estate unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-rds-kms: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-rds-kms: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04KMS_JSON:-design/c04-rds-kms.json}"
DOC="${OLIVARES_C04KMS_DOC:-design/C04-RDS-KMS-2026-08-20.md}"
TF="${OLIVARES_C04KMS_TF:-deploy/aws/modules/data/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$TF" ] || cannot "missing data terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$TF" || fail "data module lost NEVER APPLIED"
if grep -qiE 'estate applied|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-rds-kms/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("rds_cmk") is not True:
    raise SystemExit("rds_cmk must be true")
if data.get("key_rotation") is not True:
    raise SystemExit("key_rotation must be true")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

grep -q 'resource "aws_kms_key" "rds"' "$TF" || fail "RDS KMS key resource missing"
grep -qE 'enable_key_rotation[[:space:]]*=[[:space:]]*true' "$TF" \
	|| fail "KMS key rotation is not true"
# ANCLADO a principio de atributo el 2026-08-20. Sin el `^[[:space:]]*` este
# patron casa tambien con `performance_insights_kms_key_id = aws_kms_key.rds.arn`,
# que es un atributo HERMANO y legitimo (#1205). Con el hermano presente, borrar
# el kms_key_id REAL dejaba este gate en verde: pinaba una cadena, no el atributo.
grep -qE '^[[:space:]]*kms_key_id[[:space:]]*=[[:space:]]*aws_kms_key\.rds\.arn' "$TF" \
	|| fail "RDS instance is not bound to the CMK"
grep -qE 'storage_encrypted[[:space:]]*=[[:space:]]*true' "$TF" \
	|| fail "RDS storage_encrypted lost true"
grep -qE 'skip_final_snapshot[[:space:]]*=[[:space:]]*false' "$TF" \
	|| fail "RDS skip_final_snapshot lost false"

say "check-c04-rds-kms: CLEAN — RDS CMK present with rotation; estate unapplied."
exit 0
