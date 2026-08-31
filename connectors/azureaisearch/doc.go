// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package azureaisearch is the Azure AI Search knowledge DATA connector: it
// ingests documents from Azure AI Search indices for governed knowledge,
// read-only, with security-filter-based ACL as provenance permissions.
//
// It implements contentsource.Source and contentsource.LiveSource. DeltaList
// queries the index with a configurable timestamp field filter for incremental
// sync. If the index has a security filter field, ACL entries are extracted from
// it. Authentication is via API key or Azure managed identity (IMDS token).
// Credentials are by secret-store reference only (docs/SECURITY-HARDENING.md). Uses pure HTTP
// REST API calls — no Azure SDK dependency. Imports only the SDK + the shared
// content helper, never /core.
package azureaisearch
