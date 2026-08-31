// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

import sdkmodel "github.com/olivaresai/olivares/sdk/model"

// AccessEdge is the differential entity: a recorded access from an origin
// (agent/identity/session) to a resource, with its read/write mode, the signal
// that produced it, the confidence, and — the killer feature — whether it is
// PERMITTED versus actually OBSERVED (ARCHITECTURE.md, §6). It is minimal-data: it
// stores the relationship, never the payload, SQL body, secret or PII that
// flowed across it (docs/SECURITY-HARDENING.md).
//
// The R/RW access map (module III) is a query over this entity, not a separate
// schema: least-privilege drift is simply the set of edges where Permitted and
// Observed disagree.
type AccessEdge struct {
	BaseFields
	// OriginKind is what acted: "agent", "identity" or "session".
	OriginKind string
	// OriginID is the acting entity.
	OriginID ID
	// ResourceID is the resource that was accessed.
	ResourceID ID
	// Mode is the read/write classification (shared wire vocabulary).
	Mode sdkmodel.AccessMode
	// SignalSource is the collector that produced the edge.
	SignalSource sdkmodel.SignalSource
	// Confidence is the trust in the attribution.
	Confidence sdkmodel.Confidence
	// Permitted reports whether a policy/grant permits this access. False means
	// "not known to be permitted", not "forbidden".
	Permitted bool
	// Observed reports whether the access was actually seen happening.
	Observed bool
	// ToolID is the tool/operation that performed the access (may be zero).
	ToolID ID
	// SessionID ties the edge to a session when known (may be zero).
	SessionID ID
	// FirstSeen and LastSeen bound the observation window.
	FirstSeen Timestamp
	LastSeen  Timestamp
	// OccurrenceCount counts how many times the edge was observed.
	OccurrenceCount int64
	// Metadata is free-form, non-sensitive context.
	Metadata map[string]any
}

// NodeRef references a node in the access graph (an origin or a resource).
type NodeRef struct {
	// Kind is the node kind (e.g. "agent", "resource").
	Kind string
	// ID is the node's entity id.
	ID ID
}

// Direction selects which edges Neighbors returns relative to a node.
type Direction string

// The graph traversal directions.
const (
	// Outgoing returns edges where the node is the origin.
	Outgoing Direction = "outgoing"
	// Incoming returns edges where the node is the resource.
	Incoming Direction = "incoming"
	// Both returns edges in either direction.
	Both Direction = "both"
)

// DriftKind classifies a least-privilege drift between permitted and observed.
type DriftKind string

// The drift kinds.
const (
	// DriftUnusedGrant is a permitted access never observed (over-provisioned).
	DriftUnusedGrant DriftKind = "unused_grant"
	// DriftViolation is an observed access that is not permitted.
	DriftViolation DriftKind = "violation"
)

// PrivilegeDrift is one least-privilege discrepancy: an edge whose Permitted and
// Observed flags disagree (ARCHITECTURE.md).
type PrivilegeDrift struct {
	// Edge is the offending access edge.
	Edge AccessEdge
	// Kind is the kind of drift.
	Kind DriftKind
}
