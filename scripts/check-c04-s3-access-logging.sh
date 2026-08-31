#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: S3 server-access logging to a sibling bucket.
# Unapplied. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-s3-access-logging: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-s3-access-logging: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04SL_JSON:-design/c04-s3-access-logging.json}"
DOC="${OLIVARES_C04SL_DOC:-design/C04-S3-ACCESS-LOGGING-2026-08-20.md}"
TF="${OLIVARES_C04SL_TF:-deploy/aws/modules/data/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$TF" ] || cannot "missing data terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$TF" || fail "data module lost NEVER APPLIED"
if grep -qiE 'estate applied|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

python3 - "$JSON" "$TF" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

def block_after(src, pat):
    m = re.search(pat, src)
    if not m:
        return None
    i = m.end()
    depth = 1
    while i < len(src) and depth:
        if src[i] == "{":
            depth += 1
        elif src[i] == "}":
            depth -= 1
        i += 1
    return src[m.start():i]

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-s3-access-logging/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("logging") is not True:
    raise SystemExit("logging must stay true")
if data.get("self_target") is not False:
    raise SystemExit("self_target must stay false")
if data.get("target_prefix") != "s3/":
    raise SystemExit("target_prefix drifted")
if data.get("delivery_principal") != "logging.s3.amazonaws.com":
    raise SystemExit("delivery_principal drifted")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

tf = open(sys.argv[2], encoding="utf-8").read()
logs = block_after(tf, r'resource\s+"aws_s3_bucket"\s+"plane_logs"\s*\{')
if not logs:
    raise SystemExit("aws_s3_bucket.plane_logs missing")

lg = block_after(tf, r'resource\s+"aws_s3_bucket_logging"\s+"plane"\s*\{')
if not lg:
    raise SystemExit("aws_s3_bucket_logging.plane missing")
if "aws_s3_bucket.plane_logs.id" not in lg:
    raise SystemExit("logging target is not plane_logs")
if re.search(r'target_bucket\s*=\s*aws_s3_bucket\.plane\.id', lg):
    raise SystemExit("logging target is the source bucket")
if not re.search(r'target_prefix\s*=\s*"s3/"', lg):
    raise SystemExit("target_prefix lost s3/")

pol = block_after(tf, r'resource\s+"aws_s3_bucket_policy"\s+"plane_logs"\s*\{')
if not pol:
    raise SystemExit("plane_logs bucket policy missing")
if "logging.s3.amazonaws.com" not in pol:
    raise SystemExit("delivery principal lost")
if "s3:PutObject" not in pol:
    raise SystemExit("PutObject lost from the log policy")
if "log-delivery-write" in pol or "acl" in pol.lower():
    raise SystemExit("retired ACL grant is back")
PY

say "check-c04-s3-access-logging: CLEAN — plane logs to sibling; estate unapplied."
exit 0
