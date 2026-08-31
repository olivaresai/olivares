#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: versioned S3 + ACM named; estate unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-s3-acm: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-s3-acm: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04SA_JSON:-design/c04-s3-acm.json}"
DOC="${OLIVARES_C04SA_DOC:-design/C04-S3-ACM-2026-08-20.md}"
DATA="${OLIVARES_C04SA_DATA:-deploy/aws/modules/data/main.tf}"
ING="${OLIVARES_C04SA_ING:-deploy/aws/modules/ingress/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$DATA" ] || cannot "missing data terraform"
[ -f "$ING" ] || cannot "missing ingress terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$DATA" || fail "data module lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$ING" || fail "ingress module lost NEVER APPLIED"
if grep -qiE 'estate applied|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-s3-acm/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("s3_versioned") is not True:
    raise SystemExit("s3_versioned must be true")
if data.get("acm_named") is not True:
    raise SystemExit("acm_named must be true")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

grep -q 'resource "aws_s3_bucket" "plane"' "$DATA" || fail "plane S3 bucket missing"
# ── ACOTADO al cubo `plane`, que es el sujeto de este gate (2026-08-20) ──────
# Estas cuatro comprobaciones hacian `grep` sobre el FICHERO ENTERO. Con un solo
# cubo daba igual; hoy `modules/data/main.tf` declara `plane`, `plane_logs` y
# `alb_conn`, asi que bastaba con que CUALQUIERA de los tres tuviera la
# proteccion para que el gate declarase limpio el `plane`. Es un falso verde:
# suspender el versionado del cubo del plano de control seguia dando CLEAN
# porque los otros dos seguian Enabled. Medido: `status = "Enabled"` aparece 2
# veces y `block_public_acls` 3.
bloque_de() { # <regex-del-recurso> -> imprime su bloque
	awk -v re="$1" '
		index($0, "resource") == 1 && $0 ~ re { d = 0; on = 1 }
		on {
			print
			n = gsub(/\{/, "{"); m = gsub(/\}/, "}")
			d += n - m
			if (d == 0 && n + m > 0) { on = 0 }
		}' "$DATA"
}

ver="$(bloque_de "aws_s3_bucket_versioning\" \"plane\"")"
[ -n "$ver" ] || fail "aws_s3_bucket_versioning.plane missing"
grep -qE 'status[[:space:]]*=[[:space:]]*"Enabled"' <<<"$ver" \
	|| fail "S3 versioning is not Enabled on the plane bucket"

pab="$(bloque_de "aws_s3_bucket_public_access_block\" \"plane\"")"
[ -n "$pab" ] || fail "aws_s3_bucket_public_access_block.plane missing"
grep -qE 'block_public_acls[[:space:]]*=[[:space:]]*true' <<<"$pab" \
	|| fail "S3 public ACLs are not blocked on the plane bucket"
grep -qE 'restrict_public_buckets[[:space:]]*=[[:space:]]*true' <<<"$pab" \
	|| fail "S3 public buckets are not restricted on the plane bucket"

sse="$(bloque_de "aws_s3_bucket_server_side_encryption_configuration\" \"plane\"")"
[ -n "$sse" ] || fail "aws_s3_bucket_server_side_encryption_configuration.plane missing"
grep -qE 'sse_algorithm[[:space:]]*=[[:space:]]*"AES256"' <<<"$sse" \
	|| fail "S3 encryption is not AES256 on the plane bucket"
grep -q 'resource "aws_kms_key" "rds"' "$DATA" || fail "RDS CMK lost"

grep -q 'resource "aws_acm_certificate" "alb"' "$ING" \
	|| fail "ACM certificate resource missing"
grep -qE 'validation_method[[:space:]]*=[[:space:]]*"DNS"' "$ING" \
	|| fail "ACM validation is not DNS"
grep -q 'local.cert_arn' "$ING" || fail "HTTPS listener is not bound to local.cert_arn"

say "check-c04-s3-acm: CLEAN — versioned S3 and ACM named; estate unapplied."
exit 0
