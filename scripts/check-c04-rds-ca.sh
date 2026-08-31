#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: RDS ca_cert_identifier is named. Unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-rds-ca: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-rds-ca: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04CA_JSON:-design/c04-rds-ca.json}"
DOC="${OLIVARES_C04CA_DOC:-design/C04-RDS-CA-2026-08-20.md}"
TF="${OLIVARES_C04CA_TF:-deploy/aws/modules/data/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$TF" ] || cannot "missing data terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$TF" || fail "data module lost NEVER APPLIED"
grep -q 'rds-ca-rsa2048-g1' "$DOC" || fail "$DOC lost the named CA"
if grep -qiE 'estate applied|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

python3 - "$JSON" "$TF" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-rds-ca/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("ca_cert_identifier") != "rds-ca-rsa2048-g1":
    raise SystemExit("ca_cert_identifier must stay rds-ca-rsa2048-g1")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

tf = open(sys.argv[2], encoding="utf-8").read()
if not re.search(r'ca_cert_identifier\s*=\s*"rds-ca-rsa2048-g1"', tf):
    raise SystemExit("ca_cert_identifier lost rds-ca-rsa2048-g1")
if not re.search(r"deletion_protection\s*=\s*true", tf):
    raise SystemExit("deletion_protection lost true")
if not re.search(r"kms_key_id\s*=\s*aws_kms_key\.rds\.arn", tf):
    raise SystemExit("RDS lost the storage CMK bind")
PY

say "check-c04-rds-ca: CLEAN — RDS CA named; estate unapplied."
exit 0
