// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package notion is the Notion knowledge DATA connector: it ingests Notion page
// CONTENT (title + blocks) for module VIII, read-only, with the parent
// database/workspace and shared groups as provenance and source permissions.
//
// It implements contentsource.Source (NOT sdk.SourceConnector); it parses the
// Notion API native shape (pages + blocks, exported to a JSON file/directory). The
// live API transport is a follow-up behind the same interface. Credentials are by
// secret-store reference only; the page body is returned raw for the knowledge
// module to redact (docs/SECURITY-HARDENING.md-4). Imports only the SDK + the shared content
// helper, never /core.
package notion
