// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package icebergcatalog is the Olivares AI connector for the Apache Iceberg
// REST Catalog and Apache Polaris — the PERMITTED (declared-grant) side of the
// permitted-vs-observed access diff (ARCHITECTURE.md), NOT an observed-access feed.
//
// # The platform
//
// An Iceberg REST Catalog (Polaris is the reference implementation) governs which
// principals may read or write the data of each table, and vends short-lived
// storage credentials for that access. Polaris expresses access as table-level
// privileges granted to (catalog-role → principal-role) chains; the two data
// privileges this connector cares about are TABLE_READ_DATA (the catalog vends
// short-lived READ-ONLY storage credentials) and TABLE_WRITE_DATA (it vends
// read+write credentials). See the Iceberg REST catalog spec
// (https://iceberg.apache.org/rest-catalog-spec/) and Polaris access control
// (https://polaris.apache.org/releases/1.0.0/access-control/).
//
// # Ingest path (honest)
//
// The operator EXPORTS a single JSON snapshot object of the catalog's current
// grants and outstanding vended credentials, and the connector reads THAT FILE
// (os.ReadFile + json.Unmarshal — a single snapshot object, not a line-delimited
// log). The connector never opens a connection to the catalog, never calls the
// REST API, never vends or mutates anything: it is a read-only file reader.
//
// # Security posture (docs/SECURITY-HARDENING.md-3)
//
//   - Read-first: the only I/O is reading the operator-exported snapshot file.
//     The connector never connects to or mutates the catalog or its storage.
//   - Minimal-data: it emits only the access EDGE (principal, table, mode, source,
//     confidence, tool, timestamp). The record structs declare ONLY the fields read;
//     no storage credential, token, secret or SQL ever has a field, is read, or is
//     emitted. A vended credential contributes only its principal IDENTIFIER and the
//     privilege — never the credential material.
//   - Identities, not credentials: OriginKind is always "identity" (the catalog
//     attributes a grant to a principal/role, never to a resolved agent; the
//     identity↔agent bridge is module VI's job).
//
// # Verbatim R/RW mapping (never guessed)
//
//	TABLE_READ_DATA   => read   (catalog vends short-lived read-only credentials)
//	TABLE_WRITE_DATA  => write  (catalog vends short-lived read+write credentials)
//	any other privilege token => no data-access edge (it is a metadata/admin
//	                             privilege, not a data R/RW grant — not emitted)
//
// Every emitted edge carries Source=model.SignalPolicy: these are DECLARED grants
// (the permitted side), like the vault and managed-settings connectors emit —
// they are NOT observed accesses. Module III/VI diffs this permitted side against
// the observed side (pg-audit/CloudTrail/eBPF) to surface over-provisioned and
// shadow access. Confidence is attributed for a static grant (approximate if its
// principal is a declared shared account), and ALWAYS approximate for a vended
// credential: an ephemeral, vended principal is ambiguous attribution.
package icebergcatalog
