// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package gdrive is the Google Drive knowledge DATA connector: it ingests Drive
// document CONTENT (Docs/Sheets/Slides/files) for retrieval-augmented generation
// in module VIII, read-only, with each document's source permissions and
// provenance.
//
// It implements contentsource.Source (NOT sdk.SourceConnector): it is pull-driven
// by the knowledge module, emits no observations, and never writes back to Drive.
// Following the established connector pattern (s3-cloudtrail/pg-audit parse an
// exported native format with testdata fixtures), this connector parses the Drive
// Files API native shape (files.list + files.export, as exported to a JSON file
// or directory). The live OAuth/API transport is a documented follow-up behind the
// same Source interface; with no export configured it opens as an empty source.
//
// Minimal data / red line (docs/SECURITY-HARDENING.md-4): the source credential is configured BY
// REFERENCE to a secret-store (never inline); a document's ACL carries Drive
// permission references (group / domain / principal), never credential material;
// the document body is returned RAW for the knowledge module to redact (this
// connector never logs or pre-scrubs it). It imports only the SDK and the shared
// content helper, never /core.
package gdrive
