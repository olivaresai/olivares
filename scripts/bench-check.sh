#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# bench-check.sh — anti-bit-rot smoke for the capacity/scale benchmarks. Runs each
# benchmark ONCE so a rename or a panic in the harness/seeding is caught, even
# though the raw throughput numbers are hardware-dependent (docs/SIZING-AND-CAPACITY.md
# owns the provenance-stamped figures). Compile-rot is already caught by `task test`
# in mainline-ci (it builds every *_test.go); this adds RUNTIME coverage.
#
# It is deliberately NOT a threshold gate: bench numbers vary by hardware, and the
# nightly CI runs on a different machine than the reference baseline, so a
# benchstat delta there would be hardware noise, not a regression. The gate is
# "the benches still build and run green". A benchstat delta vs the committed
# reference baseline is printed only as ADVISORY, and only when you have benchstat
# installed AND are on matching hardware.
#
# Usage:
#   scripts/bench-check.sh            # fast: write/storage/tenant + decision + DLP planes (~10s)
#   scripts/bench-check.sh --full     # also the RAG end-to-end corpus + 1M ranker curve (~70s)
#
# Env:
#   BENCHTIME   go test -benchtime (default 1x — one iteration, just prove it runs)
set -euo pipefail
cd "$(dirname "$0")/.."

BT="${BENCHTIME:-1x}"
FULL=0
[ "${1:-}" = "--full" ] && FULL=1
OUT="$(mktemp)"
trap 'rm -f "$OUT"' EXIT

echo "== bench smoke (build + run once, benchtime=$BT, full=$FULL) =="
# core/bench: write plane + storage-growth + per-tenant cost (all fast).
go test ./core/bench/ -run '^$' -bench . -benchmem -benchtime "$BT" | tee -a "$OUT"
# finops plane: budget reservation + check. Added 2026-08-01 — this script's own
# header promises each benchmark runs once, and modules/finops matched no selector
# here, so BenchmarkReserveBudget and BenchmarkCheckBudget were never smoke-run.
go test ./modules/finops/ -run '^$' -bench 'Budget' -benchmem -benchtime "$BT" | tee -a "$OUT"
# decision plane: in-memory algebra + end-to-end hook/proxy governed decision.
go test ./modules/inferenceproxy/ -run '^$' -bench 'DLPDecide' -benchmem -benchtime "$BT" | tee -a "$OUT"
go test ./cmd/olivares/ -run '^$' -bench 'HookDecide|ProxyAuthorizeEndToEnd' -benchmem -benchtime "$BT" | tee -a "$OUT"
if [ "$FULL" = 1 ]; then
  # retrieval plane: the 100k end-to-end point seeds a real corpus (~45s) and the
  # 1M ranker curve allocs ~24 MiB — nightly only.
  go test ./modules/knowledge/ -run '^$' -bench 'Retrieval|Cosine' -benchmem -benchtime "$BT" | tee -a "$OUT"
else
  # fast subset: the exact-cosine ranker at 10k proves the knowledge bench builds+runs;
  # the heavy end-to-end seed + 1M point are covered by the --full nightly.
  go test ./modules/knowledge/ -run '^$' -bench 'BenchmarkCosineIndexRankCurve/candidates=10000$' -benchmem -benchtime "$BT" | tee -a "$OUT"
fi
echo "== bench smoke OK (all benches built and ran) =="

BASELINE="core/bench/testdata/baseline.txt"
if command -v benchstat >/dev/null 2>&1 && [ -f "$BASELINE" ]; then
  echo "== advisory benchstat vs ${BASELINE} (informational; only meaningful on matching hardware) =="
  benchstat "$BASELINE" "$OUT" || true
else
  echo "== advisory benchstat skipped (need 'go install golang.org/x/perf/cmd/benchstat@latest' + matching-HW baseline) =="
fi
