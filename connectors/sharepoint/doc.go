// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package sharepoint is the Microsoft SharePoint / OneDrive knowledge DATA
// connector: it ingests document CONTENT (driveItems) for module VIII,
// read-only, with the site, granted groups and sensitivity label as provenance and
// source permissions.
//
// It implements contentsource.Source (NOT sdk.SourceConnector); it parses the
// Microsoft Graph driveItem native shape (exported to a JSON file/directory). The
// live Graph API transport is a follow-up behind the same interface. Credentials
// are by secret-store reference only; the document body is returned raw for the
// knowledge module to redact (docs/SECURITY-HARDENING.md-4). Imports only the SDK + the shared
// content helper, never /core.
package sharepoint
