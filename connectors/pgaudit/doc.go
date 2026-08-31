// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package pgaudit is the Olivares AI source connector that captures read/write
// access to PostgreSQL from the database's native pgAudit trail (ARCHITECTURE.md,
// docs/contracts). It tails a structured PostgreSQL log (csvlog or jsonlog)
// carrying pgAudit "AUDIT: …" messages, and emits one model.EdgeObservation per
// audited data access.
//
// The read/write mode is taken verbatim from pgAudit's own CLASS field — READ,
// WRITE or DDL — never inferred from the SQL text. The identity is the role or
// application_name the log attributes the access to; the SQL body is parsed only
// to the extent pgAudit already classified it and is never captured or emitted
// (minimal data, docs/SECURITY-HARDENING.md). The connector is read-only: it reads the log file
// and never connects to or writes to the database.
//
// Two operational notes: jsonlog is line-delimited and supports continuous
// follow; csvlog (whose records may span newlines) is always read as a batch,
// so the `follow` setting applies only to jsonlog. And the PostgreSQL server
// must log in UTC (log_timezone = 'UTC'): PostgreSQL writes the time as a zone
// abbreviation, and a non-UTC abbreviation cannot be resolved to an offset, so
// such records are skipped rather than given a wrong timestamp.
//
// It imports only the SDK (and connector-internal helpers), never the engine,
// keeping the Apache-2.0 boundary clean (LICENSING.md).
package pgaudit
