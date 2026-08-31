#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: RDS IAM database authentication is on; master secret stays.
# Estate unapplied. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-rds-iam-auth: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-rds-iam-auth: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04IAM_JSON:-design/c04-rds-iam-auth.json}"
DOC="${OLIVARES_C04IAM_DOC:-design/C04-RDS-IAM-AUTH-2026-08-20.md}"
TF="${OLIVARES_C04IAM_TF:-deploy/aws/modules/data/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$TF" ] || cannot "missing data terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$TF" || fail "data module lost NEVER APPLIED"
grep -q 'in addition' "$DOC" || fail "$DOC lost in-addition"
if grep -qiE 'estate applied|FIRMA A claimed|master secret dropped' "$DOC"; then
	fail "$DOC claims an apply or secret-drop this lote does not have"
fi

python3 - "$JSON" "$TF" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-rds-iam-auth/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("iam_database_authentication") is not True:
    raise SystemExit("iam_database_authentication must be true")
if data.get("replaces_master_secret") is not False:
    raise SystemExit("replaces_master_secret must stay false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

tf = open(sys.argv[2], encoding="utf-8").read()
if not re.search(
    r"iam_database_authentication_enabled\s*=\s*true", tf
):
    raise SystemExit("iam_database_authentication_enabled lost true")
if not re.search(
    r"manage_master_user_password\s*=\s*true", tf
):
    raise SystemExit("master secret flag lost true")
PY

say "check-c04-rds-iam-auth: CLEAN — IAM auth on; master secret stays; unapplied."
exit 0
