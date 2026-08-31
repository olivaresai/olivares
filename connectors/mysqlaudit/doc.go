// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package mysqlaudit is the Olivares AI source connector that captures read/write
// access to MySQL/MariaDB from a native audit log (ARCHITECTURE.md,
// docs/contracts). It supports two formats:
//
//   - mariadb_audit — the MariaDB Audit Plugin / MySQL Enterprise Audit log. Its
//     TABLE events carry a per-table operation (READ/WRITE/CREATE/…) that maps to
//     a high-fidelity, per-table edge; its QUERY events are classified by the
//     statement's leading verb at database granularity.
//   - general_log — the MySQL general query log (the degraded fallback): a
//     connection state machine attributes each statement to its user@host and
//     current database, classifying the access by the statement verb.
//
// The connector is read-only over the log file and never connects to the
// database. The SQL body is read only far enough to read the leading verb and is
// never captured or emitted (minimal data, docs/SECURITY-HARDENING.md). It imports only the SDK
// and connector-internal helpers, never the engine (LICENSING.md).
package mysqlaudit
