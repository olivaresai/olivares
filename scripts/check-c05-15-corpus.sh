#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c05-15-corpus.sh — C05-15. The SAME captured Dodo body must drive
# classifyDodo (Worker) and EventDataFromDodo (Go). Three answers.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-15-corpus: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-15-corpus: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

# export-closure: hub-only cloud/control-plane/internal/billing/c05_15_corpus_test.go — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/control-plane/internal/billing/dodoenvelope.go — el modulo cloud/ no viaja al export
# Sin el modulo no hay sujeto que comprobar. La respuesta correcta es la TERCERA del
# canon —«no he podido mirar»—, no un verde y no el error crudo con el que muere hoy.
if [ ! -d cloud/control-plane ]; then
	printf '%s\n' "check-c05-15-corpus: COULD NOT LOOK — cloud/control-plane is not in this tree" >&2
	exit 2
fi

EVIDENCE=commercial/dodo-sandbox/evidence/dodo-10/wh-deliveries/delivery-0010.json
GO=cloud/control-plane/internal/billing/c05_15_corpus_test.go
TS=commercial/license-worker/test/c05-15-corpus-contract.test.ts
CLASSIFIER=commercial/license-worker/src/dodo/events.ts
PARSER=cloud/control-plane/internal/billing/dodoenvelope.go

for f in "$EVIDENCE" "$GO" "$TS" "$CLASSIFIER" "$PARSER"; do
	[ -r "$f" ] || cannot "missing $f"
done

grep -q '"raw_body"' "$EVIDENCE" || fail "evidence lost raw_body"
grep -q 'delivery-0010.json' "$GO" || fail "Go contract does not load delivery-0010.json"
grep -q 'EventDataFromDodo' "$GO" || fail "Go contract does not call EventDataFromDodo"
grep -q 'classifyDodo' "$TS" || fail "Worker contract does not call classifyDodo"
grep -q 'delivery-0010.json' "$TS" || fail "Worker contract does not load delivery-0010.json"
grep -q 'INVALID_PAYLOAD' "$TS" || fail "Worker contract lost the reduced-body INVALID_PAYLOAD control"
grep -q 'export function classifyDodo' "$CLASSIFIER" || fail "classifyDodo is no longer exported"
grep -q 'func EventDataFromDodo' "$PARSER" || fail "EventDataFromDodo is gone"

say "check-c05-15-corpus: CLEAN — both halves name delivery-0010 and call the shipped classifiers."
exit 0
