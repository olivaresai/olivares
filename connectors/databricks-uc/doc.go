// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package databricksuc is the Olivares AI source connector that captures
// read/write access to Databricks Unity Catalog tables and columns from the
// platform's native lineage system tables (ARCHITECTURE.md, docs/contracts). On
// Databricks both the audit and the data-flow signal live in one platform; the
// VERBATIM R/RW source here is the lineage system tables —
// system.access.table_lineage and system.access.column_lineage — which Unity
// Catalog populates with a record per read or write event on a table or path.
//
// The operator exports those two system tables to a newline-delimited JSON file
// (one lineage row per line) that this connector reads; the connector never
// opens a connection to the warehouse, never runs SQL, and never writes
// anywhere. The R/RW mode is taken verbatim from the lineage row's STRUCTURE,
// not guessed from any statement text: a row with a non-empty source means
// created_by READ that source, and a row with a non-empty target means
// created_by WROTE that target. A single lineage row therefore yields up to two
// edges (a read of the source and a write of the target). For table_lineage the
// resource is the table (kind "databricks.table", ref the three-part
// catalog.schema.table full name); for column_lineage it is the column (kind
// "databricks.column", ref catalog.schema.table.column). The connector emits
// only the access edge — origin, resource, mode, source, confidence, timestamp —
// never the SQL/statement body, query args, or any payload (minimal data,
// docs/SECURITY-HARDENING.md). The record structs declare only the lineage fields read.
//
// The identity is the created_by principal the lineage attributes the access to
// (a user email or a service principal); it is always emitted as OriginKind
// "identity" (the audit attributes to a credential/principal, not a resolved
// agent — the identity↔agent bridge is module VI's job). When created_by is
// declared a shared/pooled account via the shared_accounts setting, the raw
// identity is still emitted but the attribution confidence drops to approximate.
//
// ObservedAt always comes from the lineage row's own event_time (UTC, the
// platform records the +00:00 offset), parsed and normalized to UTC — never the
// wall clock — so an edge re-emitted after a connector restart collapses to the
// same dedup key (docs/contracts). ToolRef is empty: lineage names no
// tool/operation.
//
// It imports only the SDK (and connector-internal helpers), never the engine,
// keeping the Apache-2.0 boundary clean (LICENSING.md).
package databricksuc
