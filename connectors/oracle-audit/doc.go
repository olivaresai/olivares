// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package oracleaudit is the Olivares AI source connector that captures
// read/write access from the Oracle Database Unified Audit Trail (ARCHITECTURE.md,
// docs/contracts). The operator unloads the UNIFIED_AUDIT_TRAIL view to a
// newline-delimited JSON (NDJSON) export file — one audit row per line — and the
// connector reads that file. It never connects to Oracle, never opens a database
// session and never writes anywhere; the I/O is a read-only tail of the export.
//
// Minimal data: the connector reads ONLY the columns it needs to build an access
// edge (DBUSERNAME, ACTION_NAME, OBJECT_SCHEMA, OBJECT_NAME, the UTC event
// timestamp). It never reads SQL_TEXT or any bind/parameter column; the SQL body
// never enters the process and is never emitted. It emits identities (the
// database user that acted), never credentials.
//
// The read/write mode is taken verbatim from Oracle's own ACTION_NAME, never
// inferred from the (unread) statement text:
//
//	SELECT                                          -> read
//	INSERT / UPDATE / DELETE                        -> write
//	TRUNCATE TABLE / CREATE* / ALTER* / DROP* (DDL) -> write
//	EXECUTE                                         -> unknown (a procedure call may read or write)
//	LOCK                                            -> unknown
//	any other action                                -> unknown (never guessed)
//
// A MERGE statement is not a UNIFIED_AUDIT_TRAIL action: Oracle's AUDIT_ACTIONS
// vocabulary has no MERGE row, and a MERGE is audited under its underlying
// INSERT/UPDATE row actions. MERGE is therefore not special-cased; an
// ACTION_NAME the trail does not define falls through to unknown, never guessed.
//
// OriginKind is always "identity": the audit attributes an access to a database
// user/credential, not to a resolved agent; the identity↔agent bridge is module
// VI's job. A user declared shared via shared_accounts is emitted with
// approximate confidence (the raw identity is still emitted; only trust drops).
//
// It imports only the SDK and connector-internal helpers, never the engine
// (LICENSING.md).
package oracleaudit
