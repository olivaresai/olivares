// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package confluence is the Atlassian Confluence knowledge DATA connector: it
// ingests Confluence page CONTENT for module VIII, read-only, with each
// page's space, read-restrictions and labels as provenance and source permissions.
//
// It implements contentsource.Source (NOT sdk.SourceConnector); it parses the
// Confluence Content REST native shape (an exported JSON file/directory). The live
// API transport is a follow-up behind the same interface. Credentials are by
// secret-store reference only; the page body is returned raw for the knowledge
// module to redact (docs/SECURITY-HARDENING.md-4). Imports only the SDK + the shared content
// helper, never /core.
package confluence
