// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package deltasharing is the Olivares AI source connector that captures
// cross-org data egress from a Delta Sharing provider's audit trail
// (ARCHITECTURE.md, docs/contracts). Delta Sharing is the open protocol for
// sharing tables out to another organization: a recipient — an external org
// holding a recipient bearer token or OIDC credential — calls the sharing
// server's read RPCs (List Shares, Query Table, Get Table Version, …) to pull
// shared data. Every such recipient read is data LEAVING the provider org to a
// different org, which is the exfiltration-governed signal module IX
// evaluates against the cross-org boundary.
//
// The operator exports the Delta Sharing server's audit log to a file (one JSON
// object per line) and points this connector at it; the connector reads that
// file and emits one model.EdgeObservation per recipient access. It never opens
// a connection to the sharing server, the warehouse, or any catalog, and never
// writes anywhere — the only I/O is the read-only tail of the audit file
// (docs/SECURITY-HARDENING.md).
//
// Read/write is taken verbatim from the recipient action the export recorded, not
// guessed: a recipient querying or reading a shared table (queryTable,
// getTableData, getTableVersion, getMetadata, listShares, getShare, listSchemas,
// listTables, listAllTables) is egress => Mode=read. Delta Sharing is
// one-directional — a recipient cannot write back through it — so there is no
// write mode to fabricate; an action the export records that this connector does
// not recognize as a read yields Mode=unknown (explicit, never inferred).
//
// Action-token provenance (so no future reader treats these as native platform
// vocabulary): listShares/getShare/listSchemas/listTables/listAllTables/
// getTableVersion/getMetadata are the delta-io reference sharing server handler
// names (DeltaSharingService.scala); queryTable and getTableData are the
// Session brief's SYNTHETIC tokens for the POST .../query handler
// (reference handler listFiles). They are NOT platform strings — Databricks
// Unity-Catalog audits the recipient read as actionName deltaSharingQueriedTable
// (nested under request_params), and the reference server emits no action-keyed
// audit log. The flat recipient/share/schema/table/action wire shape is likewise
// the brief's operator-normalized export contract, not either platform's native
// audit schema. See parse.go for the full provenance note. The origin is the recipient
// identity the audit names (OriginKind "identity", OriginRef the recipient); a
// recipient declared shared/pooled in shared_accounts is marked approximate so
// module VI resolves the identity↔org attribution. The connector emits only
// the edge — recipient, share/schema/table, mode, action — never the rows, the
// query predicate, the pre-signed URL, or the bearer token (minimal data,
// docs/SECURITY-HARDENING.md).
//
// It imports only the SDK and connector-internal helpers, never the engine,
// keeping the Apache-2.0 boundary clean (LICENSING.md).
package deltasharing
