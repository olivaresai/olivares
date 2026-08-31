// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package cfmcpportals is a read-only SourceConnector that polls the Cloudflare
// One (Zero Trust) MCP servers and portals API and emits inventory edges for
// every discovered MCP server and portal. It also performs self-contained shadow
// MCP detection: when a configured approved-servers list is provided, it diffs
// the discovered servers against that list and emits high-severity findings for
// servers present in Cloudflare One but absent from the approved list.
//
// # What it observes (read-first, docs/SECURITY-HARDENING.md)
//
// Cloudflare One's AI Controls feature allows admins to centralize MCP servers
// under Zero Trust access policies. This connector polls the management API to
// discover:
//   - MCP servers registered in CF One (name, URL, status, tool count)
//   - MCP server portals (name, hostname)
//
// It is a batch poller: Gather lists servers, emits inventory edges, runs the
// shadow diff, lists portals, emits portal edges, and returns nil at EOF.
//
// # What it emits
//
//   - EdgeObservation per MCP server: cf.account → cf.mcp_server
//   - EdgeObservation per MCP portal: cf.account → cf.mcp_portal
//   - FindingReport (shadow_mcp, severity=High) per unmanaged server
//   - FindingReport (health, severity=Medium) on API failure
//
// # Minimal data (docs/SECURITY-HARDENING.md-3)
//
// Only structural inventory metadata is read: server names (public
// identifiers), URLs (sanitized via redact.SanitizeURL), status, tool/prompt
// counts. No tool arguments, resource contents, prompt text, or policy details
// are read or emitted. Server URLs may embed credentials (e.g. an OAuth
// callback with a token), so every URL is sanitized before it becomes an edge
// ref.
//
// It imports only the SDK and the standard library — never the engine (/core).
package cfmcpportals
