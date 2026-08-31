// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package redshiftaudit is the redshift-audit SourceConnector: it captures the
// R/RW access map of an Amazon Redshift data warehouse from the warehouse's own
// native user-activity audit log, and emits one EdgeObservation per statement.
//
// # Security posture
//
//   - Read-first (docs/SECURITY-HARDENING.md): the connector READS a file — the Redshift
//     user-activity log the operator has already exported off the warehouse (an
//     S3 audit-log object the operator ships to the collector host, or a
//     CloudWatch-Logs export written to a local file). It NEVER opens a JDBC/ODBC
//     connection to Redshift, never runs a query, never connects to the platform,
//     and never writes anywhere. The only I/O is a read-only tail of the log file.
//   - Minimal-data (docs/SECURITY-HARDENING.md): the record struct declares ONLY the fields the
//     edge needs (the bracketed prefix: timestamp, db, user). The SQL statement
//     after `LOG:` is read into memory only far enough to read its leading verb,
//     is classified, and is then DISCARDED — it is never emitted, never logged,
//     never stored. The emitted edge carries identifiers and the access class,
//     never the SQL body, query arguments, or any credential value. A NoRawLeak
//     test proves no statement fragment from the fixture appears in any edge field.
//   - Identities, not credentials: OriginKind is always identity.OriginKind
//     ("identity") — the Redshift audit attributes a statement to a database
//     credential/role (the `user`), not to a resolved agent. The identity↔agent
//     bridge is module VI's job; this connector never invents an agent
//     attribution the audit does not give.
//
// # The platform
//
// Amazon Redshift logs each query before it runs in its USER ACTIVITY LOG (the
// `useractivitylog` audit file, enabled by the `enable_user_activity_logging`
// parameter). Each line is one statement, prefixed with the session context:
//
//	'2026-06-03T10:23:45Z UTC [ db=dev user=analyst pid=123 userid=100 xid=456 ]' LOG: SELECT ...
//
// The connector parses the leading timestamp and the bracketed `db=`/`user=`
// fields; the access mode is derived from the LEADING SQL verb only.
//
// # R/RW mapping (DEGRADED, by-verb — marked, not faked)
//
// Redshift's user-activity log records the statement text, not a read/write
// classification, so the connector classifies by the statement's first verb — the
// same degraded pattern as the MySQL general query log (docs/contracts).
// The resource granularity is the DATABASE (resource kind "redshift.database",
// ResourceRef = the `db` field), NOT the table: extracting table names by regex
// would be guessing, and correct-and-coarse beats fine-and-wrong (ARCHITECTURE.md).
//
//	SELECT / SHOW / UNLOAD              -> read
//	INSERT / UPDATE / DELETE / COPY     -> write
//	TRUNCATE                            -> write
//	CREATE / ALTER / DROP               -> write  (DDL — a schema write)
//	GRANT / REVOKE                      -> write  (DCL — a privilege write)
//	anything else                       -> unknown (explicit; never guessed)
//
// OriginRef = the `user` field; Confidence is approximate when that user is
// declared in `shared_accounts`, attributed otherwise (the raw identity is always
// emitted; only the trust drops — docs/contracts). ToolRef = the verb.
// ObservedAt = the line's leading timestamp, normalized to UTC.
//
// # Ingest path (honest)
//
// The operator exports the native Redshift user-activity audit log to the file
// this connector reads; there is no live driver — the connector tails an exported
// audit file, never a warehouse connection.
package redshiftaudit
