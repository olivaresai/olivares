// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package pgcontent is the Olivares AI knowledge DATA connector that ingests rows
// from a PostgreSQL database as governed knowledge Documents (contentsource.Source).
// It is the operational-database counterpart to the SaaS/warehouse content sources
// (gdrive/confluence/notion/s3content/snowflake/…): a self-hosted operator points it
// at a Postgres schema/table (or a declared read-only SELECT), declares how a row
// materializes into a Document (a stable key, body column(s), a title, ACL columns,
// classification), and those rows flow through the SAME governed knowledge pipeline
// (redact → classify → chunk → embed → index → MCP serving) as every other source.
//
// READ-ONLY BY CONSTRUCTION (three independent layers, docs/SECURITY-HARDENING.md):
//  1. The connector only ever BUILDS SELECT statements (readonly.go query builders),
//     and any operator-supplied `query` is validated SELECT-only before use.
//  2. Every statement runs in a READ ONLY transaction on a session opened with
//     default_transaction_read_only=on, so PostgreSQL itself refuses a write.
//  3. The documented least-privilege role has SELECT and nothing else.
//
// A test proves the guard rejects every write form; the wire-proof E2E (CI, real
// Postgres) proves the session rejects a write it somehow reached.
//
// It imports only the SDK, the contentsource contract, the connector-internal
// content helpers and the pure pgx driver — never the engine — so the Apache license
// boundary stays clean and the connector never becomes a core data path. The module
// (VIII) — not the connector — redacts the body before persisting; the connector
// carries provenance (ACL / classification / external labels) so retrieval governs
// per chunk. Honest ACL: it maps only what the row expresses (declared ACL columns);
// it never invents a per-row ACL the source does not carry, and RLS is respected
// implicitly (the connector sees exactly what its role sees).
//
// Modes (live-vs-export, signaled by Mode()): "export" parses a static row dump
// (a snapshot, never presented as live); "live" reads the database through a pooled
// read-only pgx connection and supports incremental delta sync by an updated-at/PK
// cursor (contentsource.LiveSource), with full-list reconciliation as the fallback.
package pgcontent
