// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package threatfeed is the OPEN (Apache-2.0) wire vocabulary the AI threat-intel
// add-on shares across the open-core license boundary: the status/summary
// DTOs the CLI and console render, the channel-name constants, and the
// Claude/Anthropic governance-crosswalk SHAPES (verbatim source pins → plane
// capability evidence).
//
// This package carries SHAPES, never the curated CONTENT. The curated, signed,
// refreshable feed content — agentic-attack signatures, MCP-server reputation, the
// model-deprecation calendar, control-mapping deltas and the crosswalk rows — is
// the COMMERCIAL product and lives in the closed enterprise/threatintel module
// (LicenseRef-Olivares-Commercial, //go:build enterprise). This is the Falco
// Feeds / CrowdSec split: the format is open so anyone can read a feed and the
// engine that consumes it can be open, but the live curated data and its
// delivery/refresh are the paid add-on. Because the add-on never shipped in OSS,
// it does not read as feature-capped.
//
// The DTOs here are minimal-data (docs/SECURITY-HARDENING.md): a FeedStatus carries versions,
// dates, counts and key fingerprints, never a signature value, a private key, an
// IOC value or any sensitive content. The crosswalk pins carry only public,
// verbatim policy text and citations.
//
// It depends only on the standard library, so it crosses the connector boundary
// and the package-main composition root without dragging in the engine or /core
// (scripts/check-boundary.sh: connectors must not import /core).
package threatfeed
