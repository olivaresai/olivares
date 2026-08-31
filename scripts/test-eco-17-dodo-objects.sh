#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-eco-17-dodo-objects.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-eco-17-dodo-objects.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/eco17.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

CANON_STUB=$(python3 - <<'PY'
keys = [
    "business-m", "business-y",
    "regulated-m", "regulated-y",
    "ai-runtime-security-m", "ai-runtime-security-y",
    "compliance-packs-m", "compliance-packs-y",
    "identity-scale-m", "identity-scale-y",
    "cloud-standard-m", "cloud-standard-y",
    "cloud-scale-m", "cloud-scale-y",
]
lines = ["  live_creation_state: forbidden-until-non-object-gates-and-approval",
         "  canonical_provider_object_ids: false"]
for k in keys:
    lines.append("    - { key: %s,            provider_object_id: UNKNOWN }" % k)
print("\n".join(lines))
PY
)

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design"
	cp "$CHECK" "$TMP/tree/scripts/check-eco-17-dodo-objects.sh"
	chmod +x "$TMP/tree/scripts/check-eco-17-dodo-objects.sh"
	cp "$ROOT/design/eco-17-dodo-objects.json" "$TMP/tree/design/"
	cat >"$TMP/tree/design/ECO-17-DODO-OBJECTS-HOLD-2026-08-19.md" <<'EOF'
NOT CREATED. Fourteen keys UNKNOWN. No adelante.
EOF
	printf '%s\n' "$CANON_STUB" >"$TMP/tree/design/PRICING-CANON.md"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-eco-17-dodo-objects.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: objects HOLD is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-17-dodo-objects.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["created"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: created true is FAIL"
else
	bad "created true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-17-dodo-objects.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["objects"][0]["provider_object_id"] = "pdt_invented"
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: invented id is FAIL"
else
	bad "invented id should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-17-dodo-objects.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["objects"] = d["objects"][:13]
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: drop one key is FAIL"
else
	bad "dropped key should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'objects created' >>"$TMP/tree/design/ECO-17-DODO-OBJECTS-HOLD-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming objects created is FAIL"
else
	bad "created claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/PRICING-CANON.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing canon is LOOK (2)"
else
	bad "missing canon should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-eco-17-dodo-objects: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
