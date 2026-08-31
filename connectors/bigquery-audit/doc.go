// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package bigqueryaudit is the Olivares AI source connector that captures
// read/write access to Google BigQuery from the platform's native
// BigQueryAuditMetadata trail (ARCHITECTURE.md, docs/contracts). It reads
// Cloud Logging audit entries as NDJSON — one JSON LogEntry per line — whose
// protoPayload is a Cloud Audit Logs AuditLog carrying a BigQueryAuditMetadata
// payload in its metadata field, and emits one model.EdgeObservation per table
// data access.
//
// The read/write mode is taken verbatim from BigQueryAuditMetadata's own event
// oneof, never inferred: a tableDataRead event is a read, a tableDataChange
// event is a write. An entry whose metadata carries neither (e.g. a job- or
// dataset-level event with no table data access) is not an emittable data-access
// edge and is skipped; the read/write nature is never guessed (ARCHITECTURE.md).
// The identity is the principalEmail the audit attributes the access to (a user
// or service account), emitted as the raw OriginKind="identity" reference — the
// audit attributes to a credential, not an agent, and the identity↔agent bridge
// is module VI's job. A principalEmail declared shared/pooled by the
// operator drops the attribution to approximate; the raw identity is always
// emitted, only the trust drops.
//
// The connector reads only the audit EXPORT the operator ships to the configured
// path — a Cloud Logging sink writes the BigQuery data-access logs to a file (or
// directory of files) the connector tails — and never opens a BigQuery
// connection, queries the warehouse, or writes anywhere. It emits only the
// access edge (origin, resource, mode, source, confidence, toolRef, timestamp);
// the SQL/query body never travels (minimal data, docs/SECURITY-HARDENING.md) and is never even
// read — the connector parses only the fields it maps.
//
// It imports only the SDK (and connector-internal helpers), never the engine,
// keeping the Apache-2.0 boundary clean (LICENSING.md).
package bigqueryaudit
