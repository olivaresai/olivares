#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# export-closure: hub-only cloud/control-plane/internal/billing/c05_15_corpus_test.go — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/control-plane/internal/billing/dodoenvelope.go — el modulo cloud/ no viaja al export
# Sin el modulo no hay sujeto que comprobar. La respuesta correcta es la TERCERA del
# canon —«no he podido mirar»—, no un verde y no el error crudo con el que muere hoy.
if [ ! -f "$ROOT"/cloud/control-plane/internal/billing/c05_15_corpus_test.go ]; then
	printf '%s\n' "test-c05-15-corpus: COULD NOT LOOK — cloud/control-plane/internal/billing/c05_15_corpus_test.go is not in this tree" >&2
	exit 2
fi
if [ ! -f "$ROOT"/cloud/control-plane/internal/billing/dodoenvelope.go ]; then
	printf '%s\n' "test-c05-15-corpus: COULD NOT LOOK — cloud/control-plane/internal/billing/dodoenvelope.go is not in this tree" >&2
	exit 2
fi
CHECK="$ROOT/scripts/check-c05-15-corpus.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0515.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" \
		"$TMP/tree/commercial/dodo-sandbox/evidence/dodo-10/wh-deliveries" \
		"$TMP/tree/cloud/control-plane/internal/billing" \
		"$TMP/tree/commercial/license-worker/src/dodo" \
		"$TMP/tree/commercial/license-worker/test"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c05-15-corpus.sh"
	cp "$ROOT/commercial/dodo-sandbox/evidence/dodo-10/wh-deliveries/delivery-0010.json" \
		"$TMP/tree/commercial/dodo-sandbox/evidence/dodo-10/wh-deliveries/"
	cp "$ROOT/cloud/control-plane/internal/billing/c05_15_corpus_test.go" \
		"$TMP/tree/cloud/control-plane/internal/billing/"
	cp "$ROOT/cloud/control-plane/internal/billing/dodoenvelope.go" \
		"$TMP/tree/cloud/control-plane/internal/billing/"
	cp "$ROOT/commercial/license-worker/test/c05-15-corpus-contract.test.ts" \
		"$TMP/tree/commercial/license-worker/test/"
	cp "$ROOT/commercial/license-worker/src/dodo/events.ts" \
		"$TMP/tree/commercial/license-worker/src/dodo/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c05-15-corpus.sh" \
		>/dev/null 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return "$rc"
}

stage
if run; then ok "live contract files are CLEAN"
else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
sed -i '/classifyDodo/d' "$TMP/tree/commercial/license-worker/test/c05-15-corpus-contract.test.ts"
if run; then bad "Worker test without classifyDodo stayed CLEAN"
else ok "mutant (Worker drops classifyDodo) is killed"; fi

stage
sed -i '/EventDataFromDodo/d' "$TMP/tree/cloud/control-plane/internal/billing/c05_15_corpus_test.go"
if run; then bad "Go test without EventDataFromDodo stayed CLEAN"
else ok "mutant (Go drops EventDataFromDodo) is killed"; fi

stage
sed -i '/delivery-0010.json/d' "$TMP/tree/cloud/control-plane/internal/billing/c05_15_corpus_test.go"
if run; then bad "Go test without the corpus file stayed CLEAN"
else ok "mutant (Go drops delivery-0010) is killed"; fi

stage
rm -f "$TMP/tree/commercial/dodo-sandbox/evidence/dodo-10/wh-deliveries/delivery-0010.json"
if run; then bad "missing evidence stayed CLEAN"
else
	if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing evidence is COULD NOT LOOK"
	else bad "missing evidence should be 2 ($(cat "$TMP/err"))"; fi
fi

stage
if run; then ok "no-fire: live contract stays CLEAN"
else bad "restored live should be CLEAN ($(cat "$TMP/err"))"; fi

printf 'check-c05-15-corpus selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
