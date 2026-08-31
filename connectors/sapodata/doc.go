// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package sapodata is the SAP OData v4 knowledge DATA connector: it ingests SAP
// entity content (materials, orders, document attachments) for governed knowledge,
// read-only, with PFCG role-based ACL as provenance permissions.
//
// It implements contentsource.Source and contentsource.LiveSource. DeltaList uses
// OData v4 $deltatoken change tracking for incremental sync; without server-side
// change tracking the connector falls back to full entity-set enumeration.
// Credentials are by secret-store reference only (docs/SECURITY-HARDENING.md). Supports two auth
// schemes: Basic Auth (on-prem SAP) and OAuth 2.0 client-credentials (SAP BTP
// XSUAA). Imports only the SDK + the shared content helper, never /core.
package sapodata
