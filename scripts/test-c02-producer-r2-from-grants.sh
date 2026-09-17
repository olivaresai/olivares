#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c02-producer-r2-from-grants.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c02-producer-r2-from-grants.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c02pk.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
# export-closure: hub-only scripts/publish-enterprise-artifacts.sh — this bank
# exercises the private producer and its Worker module graph. The public export
# cannot run that fixture; report the omitted check before trying to stage it.
if [ ! -r "$ROOT/scripts/publish-enterprise-artifacts.sh" ]; then
	printf 'SKIP %s: scripts/publish-enterprise-artifacts.sh is hub-only and absent from this tree\n' \
		"$(basename "${BASH_SOURCE[0]}")"
	exit 0
fi
. "$ROOT/scripts/lib/c02-download-contract-tests.sh"
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/commercial/license-worker/src/download" \
		"$TMP/tree/commercial/license-worker/test"
	cp "$CHECK" "$TMP/tree/scripts/check-c02-producer-r2-from-grants.sh"
	chmod +x "$TMP/tree/scripts/check-c02-producer-r2-from-grants.sh"
	cp "$ROOT/design/c02-producer-r2-from-grants.json" "$TMP/tree/design/"
	cp "$ROOT/design/C02-PRODUCER-R2-FROM-GRANTS-2026-08-19.md" "$TMP/tree/design/"
	cp -R "$ROOT/commercial/license-worker/src" "$TMP/tree/commercial/license-worker/"
	cp "$ROOT/commercial/license-worker/package.json" "$TMP/tree/commercial/license-worker/"
	cp "$ROOT/commercial/license-worker/test/download.test.ts" "$TMP/tree/commercial/license-worker/test/"
	mkdir -p "$TMP/tree/commercial/license-worker/contracts" "$TMP/tree/scripts/lib"
	cp "$ROOT/commercial/license-worker/contracts/publisher.gen.json" "$TMP/tree/commercial/license-worker/contracts/"
	cp "$ROOT/scripts/lib/c02-download-contract.mjs" "$TMP/tree/scripts/lib/"
	# export-closure: hub-only scripts/publish-enterprise-artifacts.sh — private producer fixture.
	if [ -f "$ROOT/scripts/publish-enterprise-artifacts.sh" ]; then
		cp "$ROOT/scripts/publish-enterprise-artifacts.sh" "$TMP/tree/scripts/"
	fi
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c02-producer-r2-from-grants.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: pinned producer-key is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-producer-r2-from-grants.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["binary_key_includes_set"] = False
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: dropping set from the key is FAIL"
else
	bad "no-set key should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/scripts/publish-enterprise-artifacts.sh" <<'PY'
import sys
open(sys.argv[1], "w", encoding="utf-8").write(
    'plan+=("enterprise/${VERSION}/$(basename "$a")|$a")\n'
)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: unscoped publisher binary key is FAIL"
else
	bad "unscoped publisher should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/download/artifacts.ts" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
s = p.read_text()
old = 'export function artifactKey(version: string, os: string, arch: string, set: string): string {'
assert s.count(old) == 1
p.write_text(s.replace(old, 'export function artifactKey(version: string, os: string, arch: string): string {'))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -Fq "artifactKey arity is 3, want 4" "$TMP/err"; then
	ok "firing: three-arg monolith key is FAIL"
else
	bad "three-arg key should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-producer-r2-from-grants.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["monolith_fallback"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: monolith fallback claim is FAIL"
else
	bad "monolith fallback should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-producer-r2-from-grants.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["delivery_404_closed"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: claiming delivery closed is FAIL"
else
	bad "delivery closed should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/c02-producer-r2-from-grants.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing JSON is LOOK (2)"
else
	bad "missing JSON should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

c02_download_contract_mutants

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
c02_download_contract_overrides \
  OLIVARES_C02PK_JSON=design/c02-producer-r2-from-grants.json \
  OLIVARES_C02PK_DOC=design/C02-PRODUCER-R2-FROM-GRANTS-2026-08-19.md \
  OLIVARES_C02PK_ART=commercial/license-worker/src/download/artifacts.ts \
  OLIVARES_C02PK_SETS=commercial/license-worker/src/download/sets.ts \
  OLIVARES_C02PK_GATE=commercial/license-worker/src/download/gate.ts \
  OLIVARES_C02PK_PUB=scripts/publish-enterprise-artifacts.sh \
  OLIVARES_C02_CONTRATO=commercial/license-worker/contracts/publisher.gen.json \
  OLIVARES_C02PK_TEST=commercial/license-worker/test/download.test.ts

echo "test-c02-producer-r2-from-grants: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
