// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package snowflakeaudit is the snowflake-audit source connector. It captures
// column-level R/RW data access from Snowflake's native ACCESS_HISTORY audit and
// emits one minimal-data EdgeObservation per accessed object.
//
// # Security posture
//
//   - Read-first: the connector reads a FILE the operator exported from the
//     SNOWFLAKE.ACCOUNT_USAGE.ACCESS_HISTORY view (one JSON row per line, NDJSON).
//     It never opens a connection to the warehouse, never runs a query, and never
//     writes anywhere (docs/SECURITY-HARDENING.md). The honest ingest path is: the operator runs
//     a COPY INTO / SELECT … export from ACCESS_HISTORY to an NDJSON file (or a
//     scheduled task ships it) and points this connector at that file — there is
//     no live Snowflake driver here.
//   - Minimal-data: the emitted edge carries only the access edge (origin,
//     resource, mode, source, confidence, tool, timestamp). ACCESS_HISTORY rows
//     do NOT contain SQL text — only a QUERY_ID — so there is no statement to
//     parse or leak; the connector never reads QUERY_TEXT even if an export adds
//     it. The record struct declares only the fields read (docs/SECURITY-HARDENING.md).
//   - Identities, not credentials: OriginKind is always "identity" — the audit
//     attributes an access to USER_NAME (a Snowflake login), not to a resolved
//     agent. The identity↔agent bridge is module VI's job. The connector
//     emits the raw USER_NAME; it never emits a password, key or session token.
//
// # The platform
//
// Snowflake ACCESS_HISTORY (SNOWFLAKE.ACCOUNT_USAGE schema) records, per executed
// statement, the columns read and written. Each row has DIRECT_OBJECTS_ACCESSED
// and BASE_OBJECTS_ACCESSED (objects the statement read, directly and through
// views) and OBJECTS_MODIFIED (objects the statement wrote), each an array of
// { objectName, objectDomain, columns: [ { columnName } ] } entries.
//
// # R/RW mapping (verbatim from the source)
//
// The mode is taken verbatim from which bucket Snowflake placed the object in —
// it is never inferred from SQL (there is no SQL here):
//
//	DIRECT_OBJECTS_ACCESSED → Mode=read   ToolRef="direct_objects_accessed"
//	BASE_OBJECTS_ACCESSED   → Mode=read   ToolRef="base_objects_accessed"
//	OBJECTS_MODIFIED        → Mode=write  ToolRef="objects_modified"
//
// Granularity follows the row: when an object entry lists columns[], the
// connector emits one edge per column (ResourceKind "snowflake.column",
// ResourceRef "DB.SCHEMA.TABLE.COLUMN"); otherwise it emits a single
// table-grained edge (ResourceKind "snowflake.table", ResourceRef objectName).
package snowflakeaudit
