#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-eco-19-archive-encryption.sh — ECO-19 / TAR-31.
# Topology chosen (per-tenant CMK). Not applied in deploy/.
# Three answers: 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco-19-archive-encryption: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco-19-archive-encryption: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ECO19_JSON:-design/eco-19-archive-encryption.json}"
DOC="${OLIVARES_ECO19_DOC:-design/ECO-19-ARCHIVE-ENCRYPTION-2026-08-19.md}"
CANON="${OLIVARES_ECO19_CANON:-design/PRICING-CANON.md}"
DEPLOY="${OLIVARES_ECO19_DEPLOY:-deploy}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$CANON" ] || cannot "missing $CANON"
[ -d "$DEPLOY" ] || cannot "missing $DEPLOY"

command -v python3 >/dev/null 2>&1 || cannot "python3 missing"

python3 - "$JSON" <<'PY' || fail "JSON failed the ECO-19 contract"
import json, sys
path = sys.argv[1]
data = json.load(open(path, encoding="utf-8"))
want_opts = ["sse-s3", "cmk-shared-context", "cmk-per-tenant"]
if data.get("options") != want_opts:
    raise SystemExit("options %s, want %s" % (data.get("options"), want_opts))
if data.get("chosen") != "cmk-per-tenant":
    raise SystemExit("chosen %r, want cmk-per-tenant" % data.get("chosen"))
if data.get("applied") is not False:
    raise SystemExit("applied must be false (this lote does not implement SSE)")
for k in ("x_archive", "u_f", "u_d"):
    v = data.get(k)
    if v != "UNKNOWN":
        raise SystemExit("%s is %r, want UNKNOWN" % (k, v))
if data.get("exit_format") != "olivares.cloud-export.v1":
    raise SystemExit("exit_format %r" % data.get("exit_format"))
if data.get("aws_default_if_silent") != "sse-s3":
    raise SystemExit("aws_default_if_silent must remain sse-s3")
if data.get("deploy_has_archive_bucket") is not False:
    raise SystemExit("deploy_has_archive_bucket must stay false")
if data.get("plane_sse_is_not_archive") is not True:
    raise SystemExit("plane_sse_is_not_archive must stay true")
print("json-ok")
PY

grep -q 'ELEGIDO: per-tenant CMK' "$DOC" || fail "$DOC lost ELEGIDO: per-tenant CMK"
grep -q 'NO APLICADO' "$DOC" || fail "$DOC lost NO APLICADO"
grep -q 'SSE-S3' "$DOC" || fail "$DOC no longer names SSE-S3"
grep -q 'shared CMK' "$DOC" || fail "$DOC no longer names shared CMK"
grep -q 'per-tenant CMK' "$DOC" || fail "$DOC no longer names per-tenant CMK"
if grep -qiE 'aplicamos (la )?topolog|applied in terraform|APLICADO a deploy' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi
if grep -qE 'U_f[^A-Za-z0-9_].*[0-9]|u_f[^A-Za-z0-9_].*[0-9]' "$DOC"; then
	# Allow the string UNKNOWN next to U_f; fail a numeric fill.
	if grep -qE 'U_f[` ]*[:=][ `]*\$?[0-9]|`U_f`[ `]*[=:][ `]*[0-9]' "$DOC"; then
		fail "$DOC filled U_f — ECO-05's job, not this lote"
	fi
fi

grep -q 'olivares.cloud-export.v1' "$CANON" || fail "$CANON lost olivares.cloud-export.v1"

# Silent apply of the ARCHIVE plane only. C04 named SSE-S3 on the
# live-shard plane buckets (plane / plane_logs / alb_conn). That is
# ALC/C04 object-store encryption, not TAR-31. A grep of every
# sse_algorithm under deploy/ went red the day those buckets landed
# and certified the wrong plane.
python3 - "$DEPLOY" <<'PY' || fail "archive-plane SSE scan failed"
import os, re, sys
root = sys.argv[1]
archive_re = re.compile(r"archive|cloud-export|exit-bundle|tar-?31|export.v1", re.I)
sse_re = re.compile(r"x-amz-server-side|server_side_encryption|sse_algorithm", re.I)
hits = []
for dirpath, _, files in os.walk(root):
    for name in files:
        if not name.endswith((".tf", ".json", ".yml", ".yaml")):
            continue
        path = os.path.join(dirpath, name)
        rel = os.path.relpath(path, root)
        try:
            text = open(path, encoding="utf-8").read()
        except OSError as e:
            raise SystemExit("cannot read %s: %s" % (rel, e))
        path_is_archive = bool(archive_re.search(rel))
        if not sse_re.search(text) and not path_is_archive:
            continue
        if path_is_archive and sse_re.search(text):
            hits.append("%s: archive-named path carries SSE" % rel)
            continue
        for i, line in enumerate(text.splitlines(), 1):
            if archive_re.search(line) and sse_re.search(line):
                hits.append("%s:%d: %s" % (rel, i, line.strip()))
        # A resource whose NAME is archive* and whose block contains SSE.
        for m in re.finditer(r'resource\s+"[^"]+"\s+"(archive[^"]*|[^"]*cloud-export[^"]*|[^"]*exit-bundle[^"]*)"', text):
            start = m.start()
            brace = text.find("{", m.end())
            if brace < 0:
                continue
            depth = 1
            j = brace + 1
            while j < len(text) and depth:
                if text[j] == "{":
                    depth += 1
                elif text[j] == "}":
                    depth -= 1
                j += 1
            block = text[m.start():j]
            if sse_re.search(block):
                hits.append("%s: resource %s carries SSE" % (rel, m.group(0)))
if hits:
    raise SystemExit("archive plane gained SSE:\n" + "\n".join(hits))
print("archive-sse-absent")
PY

say "check-eco-19-archive-encryption: CLEAN — per-tenant CMK chosen, not applied; plane SSE is not TAR-31."
exit 0
