#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c02-03-overlay-main-unscoped.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c02-03-overlay-main-unscoped.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0203m.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree" "$TMP/ent"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" "$TMP/ent"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c02-03-overlay-main-unscoped.sh"
	cp "$ROOT/design/c02-03-overlay-main-unscoped.json" "$TMP/tree/design/"
	cp "$ROOT/design/C02-03-OVERLAY-MAIN-UNSCOPED-2026-08-20.md" "$TMP/tree/design/"
	cat >"$TMP/ent/.goreleaser.yaml" <<'EOF'
blobs:
  - provider: s3
    directory: "enterprise/{{ .Version }}"
dockers:
  - image_templates:
      - "r2-registry.olivares.ai/olivares-enterprise:latest"
      - "r2-registry.olivares.ai/olivares-enterprise:latest-amd64"
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" OLIVARES_ENT_DIR="$TMP/ent" \
		bash "$TMP/tree/scripts/check-c02-03-overlay-main-unscoped.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live unscoped-producer pin is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/ent/.goreleaser.yaml" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
t = p.read_text()
old = 'directory: "enterprise/{{ .Version }}"'
new = 'directory: "enterprise/{{ .Version }}/{{ .Env.SET }}"'
if old not in t:
    raise SystemExit("unscoped directory not found")
p.write_text(t.replace(old, new, 1))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: set dimension in directory is FAIL"
else
	bad "set dimension should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/ent/.goreleaser.yaml" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
t = p.read_text().replace(":latest", ":{{ .Version }}")
p.write_text(t)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: dropped :latest is FAIL"
else
	bad "dropped :latest should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-03-overlay-main-unscoped.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["producer_on_main"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: producer_on_main true is FAIL"
else
	bad "producer flag should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-03-overlay-main-unscoped.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["land_key_before_producer"] = False
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: denying the half-stitch is FAIL"
else
	bad "land_key_before_producer false should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'producer landed on main' >>"$TMP/tree/design/C02-03-OVERLAY-MAIN-UNSCOPED-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims producer landed is FAIL"
else
	bad "landed claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/C02-03-OVERLAY-MAIN-UNSCOPED-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing unscoped doc is COULD NOT LOOK"
else
	bad "missing doc should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
OLIVARES_ROOT="$TMP/tree" OLIVARES_ENT_DIR="" \
	bash "$TMP/tree/scripts/check-c02-03-overlay-main-unscoped.sh" \
	>"$TMP/out" 2>"$TMP/err" || true
if grep -q 'COULD NOT LOOK' "$TMP/err"; then
	ok "unset overlay dir is COULD NOT LOOK"
else
	bad "unset overlay dir should LOOK 2 ($(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "check-c02-03-overlay-main-unscoped selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
