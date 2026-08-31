// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

// MCP protocol-revision awareness (AIP-01). The connector advertises the dated
// 2026-07-28 frozen-RC revision on the stateless path but tolerates a server that
// speaks an older (or unknown) one — introspection (the list methods) is stable
// across recent revisions. Rather than silently swallow the mismatch, it records
// the revision the server negotiated and surfaces it per server, so an operator
// can see which MCP servers still speak an older revision (a fleet-hygiene /
// supply-chain signal, not a fatal error).
//
// The 2026-07-28 revision is a frozen release candidate (frozen 2026-05-21),
// scheduled for FINAL publication on 2026-07-28. Until then its normative
// content is served under:
//
//	https://modelcontextprotocol.io/specification/draft/
//
// Wire strings here were verified against the frozen RC on 2026-07-03
// and MUST be re-verified against the published spec after 2026-07-28.
//
// 2025-11-25 establishes JSON Schema 2020-12 as the default dialect for MCP schema
// definitions (SEP-1613) and adds, among others, sampling tool-calling (SEP-1577),
// URL-mode elicitation (SEP-1036), icons (SEP-973) and experimental Tasks
// (SEP-1686). Structured tool output (outputSchema/structuredContent) and
// elicitation themselves landed in the PRIOR revision, 2025-06-18 — see surface.go
// for the correct per-feature attribution.
//
// 2026-07-28 is the frozen RC stateless core: no initialize
// handshake, per-request _meta, server/discover, L7 routing headers, and removal
// of tasks/list, tasks/result, resources/subscribe|unsubscribe, ping,
// logging/setLevel. It is the connector default but is not yet the published
// FINAL spec as of 2026-07-03.

const (
	revision20241105 = "2024-11-05" // initial spec
	revision20250326 = "2025-03-26" // Streamable HTTP supersedes HTTP+SSE; OAuth
	revision20250618 = "2025-06-18" // structured output, elicitation; MCP-Protocol-Version header MUST
	revision20251125 = "2025-11-25" // JSON Schema 2020-12 default (SEP-1613), sampling tools, icons, Tasks
)

// revision20260728 is the dated revision string defined by the 2026-07-28
// frozen RC, frozen 2026-05-21 and scheduled for FINAL publication
// on 2026-07-28. Verified against /specification/draft/ on 2026-07-03;
// re-verify against the published spec after 2026-07-28.
const revision20260728 = "2026-07-28"

// currentRevision is the dated frozen-RC revision this connector advertises and
// treats as the "up to date" baseline for the staleness flag. The string remains
// 2026-07-28 during the RC window; re-verify the wire contract after publication.
const currentRevision = revision20260728

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
