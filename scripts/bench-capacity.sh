#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# bench-capacity.sh — runs the reproducible control-plane capacity benchmarks
# (core/bench / OPS-6) and stamps the result with the hardware/storage
# provenance that makes a published number meaningful. The numbers in
# docs/SIZING-AND-CAPACITY.md were produced by this exact command; re-run it on
# YOUR target host + storage class before sizing a deployment — write throughput
# is bounded by commit fsync, so it is storage-dependent.
#
# Usage:
#   bash scripts/bench-capacity.sh                 # SQLite only
#   OLIVARES_TEST_POSTGRES_DSN=postgres://... \    # also measure Postgres
#     bash scripts/bench-capacity.sh
#
# Env:
#   BENCHTIME   go test -benchtime (default 2s)
#   BENCH       -bench selector (default . = all)
#   OUT         write the raw benchmark output to this file too

set -euo pipefail
cd "$(dirname "$0")/.."

BENCHTIME="${BENCHTIME:-2s}"
BENCH="${BENCH:-.}"
TMP="${TMPDIR:-/tmp}"

echo "=== Olivares control-plane capacity benchmark — provenance ==="
echo "date(UTC) : $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "commit    : $(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
echo "go        : $(go version)"
echo "GOMAXPROCS: $(go env GOMAXPROCS)   nproc: $(nproc 2>/dev/null || echo '?')"
echo "cpu       : $(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2- | sed 's/^ //' || echo '?')"
echo "mem       : $(grep MemTotal /proc/meminfo 2>/dev/null || echo '?')"
echo -n "TMPDIR fs : "; df -hT "$TMP" 2>/dev/null | awk 'NR==2{print $1" ("$2")  "$3" total"}' || echo '?'
echo "           NOTE: if TMPDIR is tmpfs (RAM), commit fsync is ~free — the write"
echo "           throughput here is an UPPER bound vs durable disk. Re-run on the"
echo "           target storage class for production sizing."
if [ -n "${OLIVARES_TEST_POSTGRES_DSN:-}" ]; then
  echo "postgres  : measuring (OLIVARES_TEST_POSTGRES_DSN set)"
else
  echo "postgres  : SKIPPED (set OLIVARES_TEST_POSTGRES_DSN to include it)"
fi
echo "==============================================================="

run() {
  # Write/storage plane (audit append, edge upsert, write-scaling, bus, rate-limit,
  # storage-growth, per-tenant cost) — all in core/bench.
  go test ./core/bench/ -bench "$BENCH" -benchmem -run '^$' -benchtime "$BENCHTIME"
  # Decision plane: in-memory algebra + end-to-end governed decision (auth + policy
  # read + kill-switch [+ signed audit append on the proxy path]) — decisions/sec + p95/p99.
  go test ./cmd/olivares/ -run '^$' -bench 'HookDecide|ProxyAuthorizeEndToEnd' -benchmem -benchtime "$BENCHTIME"
  go test ./modules/inferenceproxy/ -run '^$' -bench 'DLPDecide' -benchmem -benchtime "$BENCHTIME"
  # Retrieval plane (F-12): end-to-end Query at corpus sizes + the exact-cosine ranker
  # curve. The 100k end-to-end point seeds a real corpus and is slow (~45s) by design.
  go test ./modules/knowledge/ -run '^$' -bench 'Retrieval|Cosine' -benchmem -benchtime "$BENCHTIME"
}

if [ -n "${OUT:-}" ]; then
  run | tee "$OUT"
  echo "raw output written to $OUT"
else
  run
fi
