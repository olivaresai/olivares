// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package salesforce is the Salesforce REST API knowledge DATA connector: it
// ingests Salesforce object content (Account, Case, Knowledge__kav,
// ContentDocument, custom objects) for governed knowledge, read-only, with
// sharing-model ACL as provenance permissions.
//
// It implements contentsource.Source and contentsource.LiveSource. DeltaList
// uses SOQL WHERE SystemModstamp > :last_sync for incremental sync.
// Authentication is OAuth 2.0 JWT bearer flow for server-to-server access (no
// interactive login). Credentials are by secret-store reference only (docs/SECURITY-HARDENING.md
// §3). Imports only the SDK + the shared content helper, never /core.
package salesforce
