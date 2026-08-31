// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package bench holds the reproducible capacity benchmarks for the control plane
// (OPS-6 /). They measure the per-node durable-write ceiling
// that bounds ingest throughput, the write latency distribution (p50/p95/p99),
// and the SQLite single-writer saturation that triggers the move to Postgres.
//
// They are plain Go benchmarks — no fixtures, no fabricated numbers — and are run
// with `task bench`, or directly:
//
//	go test ./core/bench/ -bench . -benchmem -run '^$' -benchtime 3s
//
// To measure the Postgres backend alongside SQLite, set OLIVARES_TEST_POSTGRES_DSN
// to an application-role DSN (a NOSUPERUSER NOBYPASSRLS role — see
// deploy/postgres/01-app-role.sql); a per-backend sub-benchmark then runs. The
// container that produced the published baseline has no Postgres, so the baseline
// is SQLite-only and the Postgres column in the sizing guide is to be filled by
// the operator on their target. See docs/SIZING-AND-CAPACITY.md for the method,
// the reference-hardware numbers, and how to interpret them.
package bench
