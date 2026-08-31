#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: compute task log group uses aws_kms_key.tasks.
# Estate unapplied. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-tasks-logs-kms: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-tasks-logs-kms: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04TL_JSON:-design/c04-tasks-logs-kms.json}"
DOC="${OLIVARES_C04TL_DOC:-design/C04-TASKS-LOGS-KMS-2026-08-20.md}"
TF="${OLIVARES_C04TL_TF:-deploy/aws/modules/compute/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$TF" ] || cannot "missing compute terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$TF" || fail "compute module lost NEVER APPLIED"
if grep -qiE 'estate applied|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

python3 - "$JSON" "$TF" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-tasks-logs-kms/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("tasks_cmk") is not True:
    raise SystemExit("tasks_cmk must be true")
if data.get("key_rotation") is not True:
    raise SystemExit("key_rotation must be true")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

tf = open(sys.argv[2], encoding="utf-8").read()
keys = re.findall(r'resource\s+"aws_kms_key"\s+"([^"]+)"', tf)
if keys != ["tasks"]:
    raise SystemExit("expected exactly one aws_kms_key named tasks, got %r" % keys)
if not re.search(r"enable_key_rotation\s*=\s*true", tf):
    raise SystemExit("key rotation lost true")
if not re.search(r"kms_key_id\s*=\s*aws_kms_key\.tasks\.arn", tf):
    raise SystemExit("tasks log group not bound to the tasks CMK")
if "logs." not in tf or "amazonaws.com" not in tf:
    raise SystemExit("Logs service principal lost from the key policy")
if not re.search(r'resource\s+"aws_cloudwatch_log_group"\s+"tasks"', tf):
    raise SystemExit("tasks log group missing")
PY

say "check-c04-tasks-logs-kms: CLEAN — task logs on rotating CMK; estate unapplied."
exit 0
