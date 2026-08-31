#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# B8 unique leftover unique vs check-aws-estate.sh: Steps doc so
# CLOUD-ACC can be prepared without applying. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-aws-pasos-fran: FAIL — $*" >&2; exit 1; }
cannot() { say "check-aws-pasos-fran: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_AWSFRAN_JSON:-design/aws-pasos-fran-2026-08-20.json}"
DOC="${OLIVARES_AWSFRAN_DOC:-design/AWS-PASOS-FRAN.md}"
WF="${OLIVARES_AWSFRAN_WF:-.github/workflows/aws-terraform.yml}"
SEC="${OLIVARES_AWSFRAN_SEC:-deploy/aws/modules/secrets/main.tf}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$WF" ] || cannot "missing $WF"
[ -r "$SEC" ] || cannot "missing $SEC"
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-aws-estate.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs estate-shape check"
grep -q 'HOLD' "$DOC" || fail "prepare doc lost HOLD"
grep -q 'NEVER APPLIED' "$DOC" || fail "prepare doc lost NEVER APPLIED"
grep -q 'AL FINAL' "$DOC" || fail "prepare doc lost AL FINAL"
grep -q 'AWS_ROLE_ARN' "$DOC" || fail "prepare doc lost AWS_ROLE_ARN"
grep -q 'TF_BACKEND_BUCKET' "$DOC" || fail "prepare doc lost TF_BACKEND_BUCKET"
grep -q 'apply-sandbox-estate' "$DOC" || fail "prepare doc lost confirm token"
if grep -qiE 'tofu apply ran|estate applied|FIRMA A claimed' "$DOC"; then
  fail "prepare doc claims an apply this lote does not have"
fi

grep -q 'AWS_ROLE_ARN' "$WF" || fail "workflow lost AWS_ROLE_ARN"
grep -q 'TF_BACKEND_BUCKET' "$WF" || fail "workflow lost TF_BACKEND_BUCKET"
grep -q 'apply-sandbox-estate' "$WF" || fail "workflow lost confirm token"

python3 - "$JSON" "$SEC" <<'PY' || exit $?
import json, re, sys

def fail(msg):
    print(f"check-aws-pasos-fran: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-aws-pasos-fran: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
    sec = open(sys.argv[2], encoding="utf-8").read()
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "aws-pasos-fran/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("never_applied") is not True:
    fail("never_applied must stay true")
if data.get("deploy_at_end") is not True:
    fail("deploy_at_end must stay true")
if data.get("tofu_applied_in_this_lote") is not False:
    fail("tofu_applied_in_this_lote must stay false")
want_mods = ["compute", "data", "ingress", "network", "observability", "secrets"]
if data.get("modules") != want_mods:
    fail("modules drifted from the six ratified names")
if data.get("confirm_token") != "apply-sandbox-estate":
    fail("confirm_token drifted")
if data.get("github_secrets") != ["AWS_ROLE_ARN", "TF_BACKEND_BUCKET"]:
    fail("github_secrets drifted")
# Las OCHO ranuras ratificadas, cada una con la decisión que la puso ahí. Esta lista se
# escribe A MANO A PROPÓSITO: si se derivase del módulo, el control siempre diría que sí y
# dejaría de ser un control. Al añadir una ranura se toca ESTA línea, y ese es el punto.
#   dsn, license-signing, audit, tls .... 83cc76454, los seis módulos ratificados
#   cloud-cp-admin, cloud-cp-forward .... f029c76f9, las dos claves de API del cloud-cp (C04)
#   cloud-cp-databases, cloud-cp-runtime  0a13422ee, GAP §3.1 — la task definition inyecta
#                                         de verdad lo que prometía (URLs por rol / runtime)
want_slots = ["dsn", "license-signing", "audit", "tls", "cloud-cp-admin", "cloud-cp-forward",
              "cloud-cp-databases", "cloud-cp-runtime"]
if data.get("secret_slots") != want_slots:
    fail("secret_slots drifted")
n = data.get("aws_resource_blocks_remeasured")
if not isinstance(n, int) or n < 49:
    fail("aws_resource_blocks_remeasured is not a remasure >= 49")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)
m = re.search(r'slots\s*=\s*\[([^\]]+)\]', sec)
if not m:
    fail("secrets module lost slots list")
got = re.findall(r'"([^"]+)"', m.group(1))
if got != want_slots:
    fail("secrets module slots drifted: %r" % got)
print("json-ok")
PY

say "check-aws-pasos-fran: CLEAN — owner steps HOLD; NEVER APPLIED; deploy AL FINAL."
exit 0
