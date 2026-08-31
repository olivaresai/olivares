// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// MemoryStore is a CMA memory store (memstore_...): a workspace-scoped collection of text
// documents an agent reads/writes across sessions. The connector inventories the store
// REFERENCE + metadata; it never reads a memory's content (docs/SECURITY-HARDENING.md).
type MemoryStore struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ArchivedAt string `json:"archived_at"`
	CreatedAt  string `json:"created_at"`
}

// MemoryVersion is one immutable version in a store's audit trail (memver_...). Every
// mutation creates one; they belong to the STORE and survive the memory's deletion, so the
// audit stays complete. The connector reads the audit METADATA — the operation, the actor
// attribution, and whether the version was redacted — never the content body.
type MemoryVersion struct {
	ID         string `json:"id"`
	MemoryID   string `json:"memory_id"`
	Operation  string `json:"operation"` // create | update | delete
	Actor      string `json:"actor"`     // best-effort attribution (writes are attributed to the session)
	SessionID  string `json:"session_id"`
	RedactedAt string `json:"redacted_at"`
	Redacted   bool   `json:"redacted"`
	CreatedAt  string `json:"created_at"`
}

// redactedState reports whether this version has been scrubbed via the redact endpoint
// (the evidence-of-erasure signal).
func (v MemoryVersion) redactedState() bool { return v.Redacted || v.RedactedAt != "" }

// actorRef returns the best-effort, scrubbed actor attribution for the audit detail.
func (v MemoryVersion) actorRef() string {
	if a := redact.Clean(v.Actor); a != "" {
		return a
	}
	if s := redact.Clean(v.SessionID); s != "" {
		return "session:" + s
	}
	return "unknown"
}

type memoryStorePage struct {
	Data    []MemoryStore `json:"data"`
	HasMore bool          `json:"has_more"`
	LastID  string        `json:"last_id"`
}

type memoryVersionPage struct {
	Data    []MemoryVersion `json:"data"`
	HasMore bool            `json:"has_more"`
	LastID  string          `json:"last_id"`
}

// fetchMemoryStores lists the workspace's (non-archived) memory stores.
func (c *client) fetchMemoryStores(ctx context.Context) ([]MemoryStore, error) {
	var out []MemoryStore
	after := ""
	for i := 0; i < c.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		var page memoryStorePage
		if err := c.getJSON(ctx, "/v1/memory_stores", listQuery("after_id", after), &page); err != nil {
			return out, err
		}
		out = append(out, page.Data...)
		if !page.HasMore || page.LastID == "" {
			break
		}
		after = page.LastID
	}
	return out, nil
}

// fetchMemoryVersions lists a store's most-recent version history (newest first, a single
// bounded page). It is the immutable audit trail; the connector scans it for redaction /
// deletion evidence. It deliberately does NOT walk the full history every poll — the recent
// page surfaces new erasure events; older events were already emitted with their stable
// (created_at) ObservedAt and de-dup on re-emission.
func (c *client) fetchMemoryVersions(ctx context.Context, storeID string) ([]MemoryVersion, error) {
	var page memoryVersionPage
	path := "/v1/memory_stores/" + storeID + "/memory_versions"
	if err := c.getJSON(ctx, path, listQuery("", ""), &page); err != nil {
		return nil, err
	}
	return page.Data, nil
}

// memoryStoreEdge places a memory store under its workspace in the access map.
func memoryStoreEdge(s MemoryStore, workspaceRef string, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   originWorkspace,
		OriginRef:    labelRef(workspaceRef, "workspace"),
		ResourceKind: kindMemoryStore,
		ResourceRef:  redact.Clean(s.ID),
		Mode:         model.ModeRead,
		Source:       model.SignalCMA,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   at,
	}
}

// memoryVersionFinding maps an erasure-relevant immutable memory version to a governance
// finding — the audit fact the subscribing module records to the append-only ledger as
// EVIDENCE-of-erasure (the control plane has no cryptographic-shred primitive today; true
// RTBF reconciliation is the future session). A redacted version is the primary
// signal; a delete-operation version is the audit of a removed memory (versions outlive
// their parent). A normal create/update returns ok=false (no finding — not erasure).
// ObservedAt is the version's own created_at so re-emission de-dups (the version is
// immutable; the same finding must not multiply across polls).
func memoryVersionFinding(storeID string, v MemoryVersion, fallbackAt time.Time) (model.FindingReport, bool) {
	at := parseTime(v.CreatedAt)
	if at.IsZero() {
		at = fallbackAt
	}
	switch {
	case v.redactedState():
		return model.FindingReport{
			Kind:        findingGovernance,
			Severity:    model.SeverityInfo,
			SubjectKind: kindMemoryVersion,
			SubjectRef:  redact.Clean(v.ID),
			Title:       "CMA memory version redacted (evidence-of-erasure)",
			DetailHash:  redact.Hash("memory_version redact store=" + storeID + " version=" + v.ID + " memory=" + v.MemoryID + " actor=" + v.actorRef() + " op=" + v.Operation + "; content scrubbed, immutable audit retained (CMA memory; RTBF reconciliation)"),
			OWASPASI:    []string{asiMemoryPoison},
			OccurredAt:  at,
		}, true
	case v.Operation == "delete":
		return model.FindingReport{
			Kind:        findingGovernance,
			Severity:    model.SeverityInfo,
			SubjectKind: kindMemoryVersion,
			SubjectRef:  redact.Clean(v.ID),
			Title:       "CMA memory deleted; immutable version retained as audit",
			DetailHash:  redact.Hash("memory_version delete store=" + storeID + " version=" + v.ID + " memory=" + v.MemoryID + " actor=" + v.actorRef() + "; version outlives its parent for a complete audit trail (CMA memory)"),
			OWASPASI:    []string{asiMemoryPoison},
			OccurredAt:  at,
		}, true
	default:
		return model.FindingReport{}, false
	}
}
