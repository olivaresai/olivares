#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-eco-19-archive-encryption.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-eco-19-archive-encryption.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/eco19.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

good_json() {
	cat >"$TMP/tree/design/eco-19-archive-encryption.json" <<'EOF'
{
  "lote": "ECO-19",
  "chosen": "cmk-per-tenant",
  "applied": false,
  "options": ["sse-s3", "cmk-shared-context", "cmk-per-tenant"],
  "x_archive": "UNKNOWN",
  "u_f": "UNKNOWN",
  "u_d": "UNKNOWN",
  "exit_format": "olivares.cloud-export.v1",
  "aws_default_if_silent": "sse-s3",
  "deploy_has_archive_bucket": false,
  "plane_sse_is_not_archive": true
}
EOF
}

good_doc() {
	cat >"$TMP/tree/design/ECO-19-ARCHIVE-ENCRYPTION-2026-08-19.md" <<'EOF'
ELEGIDO: per-tenant CMK. NO APLICADO.
Options: SSE-S3, shared CMK + encryption context, per-tenant CMK.
X_archive stays UNKNOWN. U_f stays UNKNOWN.
EOF
}

good_canon() {
	cat >"$TMP/tree/design/PRICING-CANON.md" <<'EOF'
  exit:
    format: olivares.cloud-export.v1
EOF
}

good_deploy() {
	mkdir -p "$TMP/tree/deploy/aws/modules/data"
	cat >"$TMP/tree/deploy/aws/main.tf" <<'EOF'
# RDS lives here. storage_encrypted is not SSE-S3.
resource "aws_db_instance" "this" {
  storage_encrypted = true
}
EOF
	# C04 plane SSE is the live shard, not TAR-31. Must stay CLEAN.
	cat >"$TMP/tree/deploy/aws/modules/data/main.tf" <<'EOF'
resource "aws_s3_bucket_server_side_encryption_configuration" "plane" {
  bucket = "plane"
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}
EOF
}

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/design" "$TMP/tree/scripts" "$TMP/tree/deploy/aws"
	cp "$CHECK" "$TMP/tree/scripts/check-eco-19-archive-encryption.sh"
	chmod +x "$TMP/tree/scripts/check-eco-19-archive-encryption.sh"
	good_json
	good_doc
	good_canon
	good_deploy
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-eco-19-archive-encryption.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

# 1. no-fire
stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: chosen, not applied, plane SSE is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# 2. firing: silent apply on an ARCHIVE-named resource
stage
printf '\nresource "aws_s3_bucket_server_side_encryption_configuration" "archive" {\n  rule {\n    apply_server_side_encryption_by_default {\n      sse_algorithm = "AES256"\n    }\n  }\n}\n' >>"$TMP/tree/deploy/aws/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'archive plane gained SSE' "$TMP/err"; then
	ok "firing: archive-named SSE is FAIL"
else
	bad "archive SSE should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# 3. firing: JSON chooses SSE-S3
stage
python3 - "$TMP/tree/design/eco-19-archive-encryption.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["chosen"] = "sse-s3"
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'JSON failed' "$TMP/err"; then
	ok "firing: choosing SSE-S3 in JSON is FAIL"
else
	bad "chosen sse-s3 should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# 4. firing: JSON applied true
stage
python3 - "$TMP/tree/design/eco-19-archive-encryption.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["applied"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'JSON failed' "$TMP/err"; then
	ok "firing: applied true is FAIL"
else
	bad "applied true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# 5. firing: fill U_f
stage
python3 - "$TMP/tree/design/eco-19-archive-encryption.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["u_f"] = 0.15
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'JSON failed' "$TMP/err"; then
	ok "firing: numeric U_f is FAIL"
else
	bad "numeric u_f should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# 6. firing: canon loses the exit format
stage
echo '  exit: { format: something-else }' >"$TMP/tree/design/PRICING-CANON.md"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'cloud-export.v1' "$TMP/err"; then
	ok "firing: losing olivares.cloud-export.v1 is FAIL"
else
	bad "lost exit format should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# 7. firing: doc claims apply
stage
echo 'applied in terraform' >>"$TMP/tree/design/ECO-19-ARCHIVE-ENCRYPTION-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'claims an apply' "$TMP/err"; then
	ok "firing: doc claiming apply is FAIL"
else
	bad "apply claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# 8. LOOK: missing ADR
stage
rm -f "$TMP/tree/design/ECO-19-ARCHIVE-ENCRYPTION-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing ADR is LOOK (2)"
else
	bad "missing ADR should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# 9. no-fire after firing
stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire after a firing case still CLEAN"
else
	bad "second untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# 10. firing: collapsing the two planes in JSON
stage
python3 - "$TMP/tree/design/eco-19-archive-encryption.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["plane_sse_is_not_archive"] = False
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -q 'JSON failed' "$TMP/err"; then
	ok "firing: plane_sse_is_not_archive false is FAIL"
else
	bad "collapsed planes should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-eco-19-archive-encryption: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
