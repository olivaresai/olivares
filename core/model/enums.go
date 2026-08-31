// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

// Severity is the qualitative severity of a Finding (ARCHITECTURE.md, module IX).
type Severity string

// The finding severities, low to critical.
const (
	// SeverityLow is an informational or low-impact finding.
	SeverityLow Severity = "low"
	// SeverityMedium is a moderate-impact finding.
	SeverityMedium Severity = "medium"
	// SeverityHigh is a high-impact finding.
	SeverityHigh Severity = "high"
	// SeverityCritical is a critical-impact finding.
	SeverityCritical Severity = "critical"
)

// FindingStatus is the triage state of a Finding.
type FindingStatus string

// The finding triage states.
const (
	// FindingOpen is a new, untriaged finding.
	FindingOpen FindingStatus = "open"
	// FindingTriaged is an acknowledged finding under investigation.
	FindingTriaged FindingStatus = "triaged"
	// FindingResolved is a fixed/closed finding.
	FindingResolved FindingStatus = "resolved"
	// FindingDismissed is a finding judged not actionable.
	FindingDismissed FindingStatus = "dismissed"
)

// HealthState is the current health of a monitored subject (ARCHITECTURE.md,
// module XXII).
type HealthState string

// The health states.
const (
	// HealthUnknown means health has not been determined.
	HealthUnknown HealthState = "unknown"
	// HealthHealthy means the subject is operating normally.
	HealthHealthy HealthState = "healthy"
	// HealthDegraded means the subject is impaired but functioning.
	HealthDegraded HealthState = "degraded"
	// HealthDown means the subject is not functioning.
	HealthDown HealthState = "down"
)

// LifecycleStatus is a generic active/inactive lifecycle for entities such as
// Agent, MCPServer, Model and Provider.
type LifecycleStatus string

// The lifecycle states.
const (
	// StatusActive means the entity is in service.
	StatusActive LifecycleStatus = "active"
	// StatusInactive means the entity is registered but not in service.
	StatusInactive LifecycleStatus = "inactive"
	// StatusError means the entity is in a fault state.
	StatusError LifecycleStatus = "error"
	// StatusSuspended means an ORG's service is withdrawn while its data is kept
	// intact. It is the intermediate state between "served" and "deleted"
	// that the cloud grace period needs: retiring ACCESS and destroying DATA are
	// two different decisions, and before the engine only had the second.
	//
	// It is org-only by construction. The tenant-settable lifecycles (workspace,
	// group — handlers_scoping.go) validate against active/inactive and therefore
	// reject it, which is correct: withdrawing service is an OPERATOR decision on
	// the System path, never something a tenant can set on itself. Nothing else
	// reads it, so adding it here changes no existing entity's behavior.
	StatusSuspended LifecycleStatus = "suspended"
)

// SessionState is the lifecycle of an agent Session (ARCHITECTURE.md, module II).
type SessionState string

// The session states.
const (
	// SessionRunning is an in-progress session.
	SessionRunning SessionState = "running"
	// SessionCompleted is a finished session.
	SessionCompleted SessionState = "completed"
	// SessionFailed is a session that ended in failure.
	SessionFailed SessionState = "failed"
)
