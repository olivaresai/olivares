// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package debezium is the Olivares AI connector that turns a Debezium change-data
// capture (CDC) stream into minimal-data access edges. Debezium
// publishes a change event per row mutation to Kafka topics (one topic per captured
// table); this connector consumes those topics through the shared kafkawire client
// (the same wire as) and emits, for each event, a single CDC edge derived
// from the envelope's `op` and `source` — which capture pipeline touched which
// table, read or write. It NEVER decodes the before/after row images: those are the
// data, and a minimal-data observer reads only the operation and the table
// coordinates (docs/SECURITY-HARDENING.md).
//
// FRONTIER WITH (static datastore audit) — no double-ingest, no double-count.
// Already observes Postgres/MySQL access STATICALLY: it tails the database's own
// audit log (pgAudit, MySQL audit) and classifies statement-level R/RW, emitting
// edges under SignalSource pg_audit / mysql_audit with ResourceKind
// "postgres.table" / "mysql.table". This connector observes the SAME logical origin
// but via a DIFFERENT path and at a DIFFERENT layer: the real-time CDC stream
// (WAL/binlog → Debezium → Kafka), emitting under SignalSource "debezium" with
// ResourceKind "cdc.table". The two are disjoint by construction:
//
//   - different ingest path: an audit-log tail vs a Kafka CDC topic;
//   - different signal source: pg_audit/mysql_audit vs debezium;
//   - different resource kind: postgres.table/mysql.table vs cdc.table,
//     so a CDC edge and a static-audit edge for the same physical table never
//     collapse onto the same graph node — they are deliberately distinct entities
//     (the static-audit table and its change-stream), exactly as aws.api
//     control-plane edges stay disjoint from data-plane edges.
//
// So an operator who runs BOTH gets the static audit (who queried what) AND the live
// change stream (what is mutating, now) without one inflating the other. This is the
// same boundary discipline declared against.
//
// SECURITY (docs/SECURITY-HARDENING.md): the connector OBSERVES the CDC stream read-only; it does not
// run a Debezium connector, never writes to the source database, and never reads a
// row value. Kafka credentials (SASL/TLS) are held in memory only. It imports only
// the SDK and the shared Apache helpers — never the engine.
package debezium
