// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

// MCP protocol-revision awareness (AIP-01). The connector advertises the dated
// 2026-07-28 revision on the stateless path but tolerates a server that
// speaks an older (or unknown) one — introspection (the list methods) is stable
// across recent revisions. Rather than silently swallow the mismatch, it records
// the revision the server negotiated and surfaces it per server, so an operator
// can see which MCP servers still speak an older revision (a fleet-hygiene /
// supply-chain signal, not a fatal error).
//
// The 2026-07-28 revision was published on 2026-07-28 at
// https://modelcontextprotocol.io/specification/2026-07-28/. The wire strings
// the connector emits for it were checked on 2026-11-04 against that text and
// the published schema (upstream commit 271ecc9a, unchanged since 2026-07-28);
// docs/mcp-revisions.md records the check field by field.
//
// 2025-11-25 establishes JSON Schema 2020-12 as the default dialect for MCP schema
// definitions (SEP-1613) and adds, among others, sampling tool-calling (SEP-1577),
// URL-mode elicitation (SEP-1036), icons (SEP-973) and experimental Tasks
// (SEP-1686). Structured tool output (outputSchema/structuredContent) and
// elicitation themselves landed in the PRIOR revision, 2025-06-18 — see surface.go
// for the correct per-feature attribution.
//
// 2026-07-28 is the stateless core: no initialize handshake,
// per-request _meta, server/discover, L7 routing headers, and removal of
// tasks/list, tasks/result, resources/subscribe|unsubscribe, ping,
// logging/setLevel. It is the connector default.

const (
	revision20241105 = "2024-11-05" // initial spec
	revision20250326 = "2025-03-26" // Streamable HTTP supersedes HTTP+SSE; OAuth
	revision20250618 = "2025-06-18" // structured output, elicitation; MCP-Protocol-Version header MUST
	revision20251125 = "2025-11-25" // JSON Schema 2020-12 default (SEP-1613), sampling tools, icons, Tasks
)

// revision20260728 is the dated string of the published 2026-07-28 revision
// , the schema's LATEST_PROTOCOL_VERSION.
const revision20260728 = "2026-07-28"

// currentRevision is the revision this connector advertises and treats as the
// "up to date" baseline for the staleness flag.
const currentRevision = revision20260728

// LatestRevision and LegacyRevision are the two revisions every MCP path of the
// product speaks, declared here and nowhere else. LatestRevision is the newest
// published revision; LegacyRevision is the newest handshake-era revision, used
// with a server that has not moved and answered by a handshake-era server.
// docs/mcp-revisions.md lists which path tries which first.
const (
	LatestRevision = revision20260728
	LegacyRevision = revision20251125
)

// HandshakeRevision is the protocolVersion a handshake-era server answers an
// initialize with: the requested revision when it is a known handshake-era one,
// as the 2025-11-25 lifecycle requires, else LegacyRevision.
func HandshakeRevision(requested string) string {
	if i := revisionIndex(requested); i >= 0 && i <= revisionIndex(LegacyRevision) {
		return requested
	}
	return LegacyRevision
}

// revisionTimeline is the ordered set of dated revisions, oldest→newest.
// A server-negotiated revision is classified against it: current, known-but-older
// (stale), or unknown (newer than we advertise, or a non-standard string).
var revisionTimeline = []string{
	revision20241105,
	revision20250326,
	revision20250618,
	revision20251125,
	revision20260728,
}

// revisionStatus is the classification of a server-negotiated revision relative to
// the connector's current baseline.
type revisionStatus string

const (
	// revisionCurrent: the server negotiated the current baseline revision.
	revisionCurrent revisionStatus = "current"
	// revisionStale: a known, dated revision OLDER than current — a fleet-hygiene
	// signal (the server presents as an older MCP client/server).
	revisionStale revisionStatus = "stale"
	// revisionUnknown: a revision not on the known timeline — either newer than we
	// advertise or a non-standard/garbage string. Surfaced, never trusted as a
	// version claim.
	revisionUnknown revisionStatus = "unknown"
)

// revisionIndex returns the position of rev on the timeline, or -1 if not a known
// dated revision.
func revisionIndex(rev string) int {
	for i, r := range revisionTimeline {
		if r == rev {
			return i
		}
	}
	return -1
}

// classifyRevision grades a server-negotiated revision against the current
// baseline. An empty revision (the server omitted protocolVersion) is treated as
// unknown — the spec requires the field, so its absence is itself a signal.
func classifyRevision(rev string) revisionStatus {
	if rev == "" {
		return revisionUnknown
	}
	cur := revisionIndex(currentRevision)
	got := revisionIndex(rev)
	switch {
	case got < 0:
		return revisionUnknown
	case got < cur:
		return revisionStale
	default:
		// got == cur (newer dated revisions would be ahead of currentRevision on
		// the timeline, but currentRevision is the last entry, so got>cur cannot
		// occur here; a newer-than-current revision is not on the timeline and is
		// classified unknown above).
		return revisionCurrent
	}
}
