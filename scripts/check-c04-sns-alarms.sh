#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: CPU/memory alarms notify aws_sns_topic.alarms.
# Estate unapplied. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-sns-alarms: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-sns-alarms: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04SNS_JSON:-design/c04-sns-alarms.json}"
DOC="${OLIVARES_C04SNS_DOC:-design/C04-SNS-ALARMS-2026-08-20.md}"
TF="${OLIVARES_C04SNS_TF:-deploy/aws/modules/observability/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$TF" ] || cannot "missing observability terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$TF" || fail "observability module lost NEVER APPLIED"
grep -q 'email subscriber' "$DOC" || fail "$DOC lost no-subscriber"
if grep -qiE 'estate applied|FIRMA A claimed|email subscription landed' "$DOC"; then
	fail "$DOC claims an apply or mailbox this lote does not have"
fi

python3 - "$JSON" "$TF" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-sns-alarms/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("sns_topic") is not True:
    raise SystemExit("sns_topic must be true")
if data.get("cpu_alarm_actions") is not True:
    raise SystemExit("cpu_alarm_actions must be true")
if data.get("memory_alarm_actions") is not True:
    raise SystemExit("memory_alarm_actions must be true")
if data.get("email_subscription") is not False:
    raise SystemExit("email_subscription must stay false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

tf = open(sys.argv[2], encoding="utf-8").read()
if not re.search(r'resource\s+"aws_sns_topic"\s+"alarms"', tf):
    raise SystemExit("aws_sns_topic.alarms missing")
if re.search(r'resource\s+"aws_sns_topic_subscription"', tf):
    raise SystemExit("invented sns subscription")

def block(name):
    m = re.search(
        r'resource\s+"aws_cloudwatch_metric_alarm"\s+"%s"\s*\{' % name,
        tf,
    )
    if not m:
        raise SystemExit("alarm %s missing" % name)
    i = m.end()
    depth = 1
    while i < len(tf) and depth:
        if tf[i] == "{":
            depth += 1
        elif tf[i] == "}":
            depth -= 1
        i += 1
    return tf[m.start() : i]

cpu = block("cpu")
mem = block("memory")
needle = r"alarm_actions\s*=\s*\[aws_sns_topic\.alarms\.arn\]"
if not re.search(needle, cpu):
    raise SystemExit("cpu alarm_actions lost the topic")
if not re.search(needle, mem):
    raise SystemExit("memory alarm_actions lost the topic")
PY

say "check-c04-sns-alarms: CLEAN — alarms notify the topic; estate unapplied."
exit 0
