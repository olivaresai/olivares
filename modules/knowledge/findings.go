// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Finding kinds this module emits on the bus (for forensics / output).
const (
	// findingSecretRedacted is emitted ONCE per ingest when the module redacted one
	// or more secrets/PII from the ingested content before indexing it.
	findingSecretRedacted = "knowledge_secret_redacted"
	// findingResidencyViolation is emitted when a retrieval is denied because the
	// requesting identity's region does not match the knowledge base's residency.
	findingResidencyViolation = "knowledge_residency_violation"
	// findingEgressBlocked is emitted when ingest is refused because the KB's embed
	// policy forbids egress but the wired embedder would send content out (the red
	// line): the content never left.
	findingEgressBlocked = "knowledge_egress_blocked"
	// findingPIIDiscovered is emitted ONCE per discovery scan / ingest that found
	// PII in governed content: classes + counts in the hashed detail,
	// never a value.
	findingPIIDiscovered = "knowledge_pii_discovered"
	// findingDLPBlocked is emitted when the DLP egress policy acted: chunks
	// withheld from a retrieval (MEDIUM — the gate doing its job) or an ingest
	// refused before embed egress (HIGH — content was about to leave).
	findingDLPBlocked = "knowledge_dlp_blocked"
	// findingMemoryTampered is emitted when an agent-memory entry fails integrity
	// verification: the read-path self-check (per entry, entry withheld) or
	// the ledger-anchored verify (one summary per run). HIGH — at ≥HIGH the
	// security module persists it into the forensic view, where its ASI06 tag
	// correlates with the memory-poisoning prompt detectors.
	findingMemoryTampered = "knowledge_memory_tampered"
	// findingInjectionBlocked is emitted when the retrieval content scanner
	// withheld one or more chunks because high-severity injection markers were
	// found in the retrieved text. HIGH — the scanner doing its job at the
	// deny-closed return point; always a forensic event worth persisting.
	findingInjectionBlocked = "retrieval_injection_blocked"
	// findingContextDenied is emitted when a forbid context policy denies a
	// retrieval before embedding/ranking.
	findingContextDenied = "knowledge_context_denied"
	// findingContextTruncated is emitted when an effective context-token ceiling
	// truncated retrieval results.
	findingContextTruncated = "knowledge_context_truncated"
)

// asiMemoryPoisoning is the OWASP Agentic Top-10 id for Memory & Context
// Poisoning, the taxonomy axis a tamper finding carries — the same tag
// the CMA connector puts on its memory-store governance findings, so the two
// surfaces correlate in the security console.
const asiMemoryPoisoning = "ASI06"

// emitFinding publishes a minimal-data FindingReport on the bus. The detail is
// HASHED (DetailHash, a one-way reference for dedup/audit only — never reverse it);
// the raw detail is never transmitted or stored (docs/SECURITY-HARDENING.md). It is best-effort:
// the caller's primary outcome does not depend on delivery, but a failure is
// surfaced, never swallowed.
func (m *Module) emitFinding(ctx context.Context, tenant model.TenantID, kind string, sev sdkmodel.Severity, subjectKind, subjectRef, title, detail string) {
	if m.host == nil {
		return
	}
	report := sdkmodel.FindingReport{
		Kind:        kind,
		Severity:    sev,
		SubjectKind: subjectKind,
		SubjectRef:  subjectRef,
		Title:       title,
		DetailHash:  hashHex(detail),
		OccurredAt:  m.clock.Now().Time(),
	}
	if err := m.host.Publish(ctx, event.FromObservation(tenant.String(), Name, report)); err != nil {
		m.debugf("knowledge: publish finding failed", "kind", kind, "err", err)
	}
}

// emitMemoryTamperFinding publishes the integrity finding: HIGH, tagged
// ASI06. subjectKind/subjectRef name the tampered ENTRY with its own entity
// kind (knowledge.memory or knowledge.memory_scoped — the audit event and the
// finding must agree on which table holds the row), or the agent/tenant for a
// verify summary. Same minimal-data contract as emitFinding — the detail is
// hashed, never transmitted raw — and same best-effort posture: the tampered
// entry was already withheld; a lost finding is surfaced, never a re-allow.
func (m *Module) emitMemoryTamperFinding(ctx context.Context, tenant model.TenantID, subjectKind, subjectRef, detail string) {
	if m.host == nil {
		return
	}
	report := sdkmodel.FindingReport{
		Kind:        findingMemoryTampered,
		Severity:    sdkmodel.SeverityHigh,
		SubjectKind: subjectKind,
		SubjectRef:  subjectRef,
		Title:       "agent memory integrity violation (tamper detected)",
		DetailHash:  hashHex(detail),
		OWASPASI:    []string{asiMemoryPoisoning},
		OccurredAt:  m.clock.Now().Time(),
	}
	if err := m.host.Publish(ctx, event.FromObservation(tenant.String(), Name, report)); err != nil {
		m.debugf("knowledge: publish finding failed", "kind", findingMemoryTampered, "err", err)
	}
}
