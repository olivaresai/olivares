// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package mssqlaudit is the mssql-audit source connector: it captures the R/RW
// access map of a Microsoft SQL Server from the platform's own SQL Server Audit
// trail, read-only, and emits one minimal EdgeObservation per audited access.
//
// # Security posture
//
//   - Read-first / no platform connection. The connector NEVER opens a connection
//     to SQL Server. SQL Server Audit writes a binary .sqlaudit file set; the
//     operator extracts it with sys.fn_get_audit_file and ships the rows as
//     newline-delimited JSON (one audit record per line), and the connector reads
//     THAT exported file. There is no live ODBC/TDS driver here, by design — the
//     honest ingest path is a file the operator exports, not a warehouse session.
//   - Minimal data. The record structs declare ONLY the fields the connector
//     reads to build an edge (event_time, action_id/action_name, the principal
//     names, database/schema/object, class_type). The audit's `statement` column
//     (the raw T-SQL) is NEVER read, never parsed, never emitted — the connector
//     emits only the access edge (origin, resource, mode, source, confidence,
//     toolref, ts), never the SQL body (docs/SECURITY-HARDENING.md).
//   - Identities, not credentials. OriginRef is the SQL Server login
//     (server_principal_name, falling back to the database user
//     database_principal_name) — an identifier, never a credential value
//     (docs/SECURITY-HARDENING.md). OriginKind is always "identity": the audit attributes an
//     access to a login/role, not to a resolved agent; the identity↔agent bridge
//     is module VI's job, not this connector's (docs/contracts).
//
// # The platform
//
// Microsoft SQL Server (and Azure SQL Database / Managed Instance) SQL Server
// Audit, the engine's native auditing feature. A database audit specification
// records the database-level data actions SELECT/INSERT/UPDATE/DELETE/EXECUTE per
// object; sys.fn_get_audit_file surfaces them as rows whose columns are the field
// names this connector parses (Microsoft Learn: "sys.fn_get_audit_file" and "SQL
// Server Audit Action Groups and Actions").
//
// # R/RW mapping (verbatim by audited action)
//
// SQL Server Audit classifies each record by its action, exposed as a short
// action_id code (sys.fn_get_audit_file converts the internal int to a character
// code) and equivalently as an action_name. The connector maps the data actions
// VERBATIM — it never infers read/write from the statement text:
//
//	SELECT  (action_id "SL")  -> read
//	INSERT  (action_id "IN")  -> write
//	UPDATE  (action_id "UP")  -> write
//	DELETE  (action_id "DL")  -> write
//	EXECUTE (action_id "EX")  -> UNKNOWN — executing a procedure/function may read
//	                             or write; the audit does not say which, so the
//	                             connector does NOT fake it (ARCHITECTURE.md).
//	any other action          -> unknown
//
// ResourceKind is "mssql.table" when class_type is "U" (a user table) and
// "mssql.object" otherwise (view, procedure, function, …); ResourceRef is the
// qualified "database.schema.object". ObservedAt is the record's own event_time
// (already UTC in the audit), parsed and normalized — never time.Now() (it is the
// dedup natural key; docs/contracts). ToolRef is the action_name.
//
// # Ingest path (honest)
//
// The operator exports the native SQL Server Audit trail to the NDJSON file the
// connector reads — e.g. SELECT event_time, action_id, succeeded,
// server_principal_name, database_principal_name, database_name, schema_name,
// object_name, class_type FROM sys.fn_get_audit_file('…\*.sqlaudit', DEFAULT,
// DEFAULT) emitted as one JSON object per line; the connector tails that file.
package mssqlaudit
