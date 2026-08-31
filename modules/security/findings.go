// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// The persisted model.Finding.Kind values this module writes (the coarse
// classification on the core Finding entity, ARCHITECTURE.md).
const (
	findingKindGuardrail = "guardrail"
	findingKindAnomaly   = "anomaly"
	findingKindForensic  = "forensic"
	// findingKindSafetyPosture is the kind the provider safety-posture connectors
	// (OpenAI Moderation, AWS Bedrock Guardrails, Azure RAI —) emit on the bus
	// and that this module persists into the security view at ANY severity (deduped),
	// so the read-first safety posture is queryable via GET /findings?kind=… and the
	// aggregated GET /safety-posture view. The value agrees by VALUE with the Apache
	// connectors across the license boundary (no shared import).
	findingKindSafetyPosture = "safety_posture"
)

// The FindingReport.Kind routing keys this module emits on the bus. Consumers route
// on these to deliver to SIEM/Slack/PagerDuty; the anomaly reactor and compliance
// consume them. They are namespaced "security_*" so a consumer can
// tell a security finding from a knowledge_*/governance_*/finops_* one.
const (
	busGuardrail         = "security_guardrail"
	busAnomaly           = "security_anomaly"
	busEvasionCorrelated = "security_evasion_correlated"
	busExfilSuspected    = "security_exfil_suspected"
)

// finding is the internal, non-sensitive description of a finding to persist. The
// detail is the redacted fingerprint string that becomes DetailHash — the raw
// payload is never carried here (docs/SECURITY-HARDENING.md).
type finding struct {
	kind        string
	severity    sdkmodel.Severity
	source      string
	subjectKind string
	subjectRef  string
	title       string
	detail      string
	meta        map[string]any
}

// persistFinding writes a core model.Finding row (the module is the first producer
// of persisted findings). The sensitive detail is reduced to a one-way
// DetailHash; the string subject ref is kept in Metadata (it is a non-sensitive
// entity reference), and SubjectID is set only when the ref is a real engine id.
func (m *Module) persistFinding(ctx context.Context, sc store.Scope, f finding) (model.ID, error) {
	meta := map[string]any{}
	for k, v := range f.meta {
		meta[k] = v
	}
	if f.subjectRef != "" {
		meta["subject_ref"] = clamp(f.subjectRef, maxRefLen)
	}
	created, err := sc.Findings().Create(ctx, model.Finding{
		Kind:        f.kind,
		Severity:    sevToCore(f.severity),
		Status:      model.FindingOpen,
		Source:      f.source,
		SubjectKind: f.subjectKind,
		SubjectID:   parseIDOrZero(f.subjectRef),
		Title:       clamp(f.title, maxNameLen),
		DetailHash:  hashBytes(f.detail),
		OccurredAt:  m.clock.Now(),
		Metadata:    meta,
	})
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

// emitFinding publishes a minimal-data FindingReport on the bus for real-time
// delivery and correlation. The detail is HASHED (one-way); the raw
// detail is never transmitted (docs/SECURITY-HARDENING.md). Best-effort: a publish failure is
// surfaced, never swallowed, but the caller's primary outcome does not depend on it.
func (m *Module) emitFinding(ctx context.Context, tenant model.TenantID, kind string, sev sdkmodel.Severity, subjectKind, subjectRef, title, detail string, tax ...taxonomyAxes) {
	if m.host == nil {
		return
	}
	report := sdkmodel.FindingReport{
		Kind:        kind,
		Severity:    sev,
		SubjectKind: subjectKind,
		SubjectRef:  clamp(subjectRef, maxRefLen),
		Title:       clamp(title, maxNameLen),
		DetailHash:  hashHex(detail),
		OccurredAt:  m.clock.Now().Time(),
	}
	// Carry the finding's multi-taxonomy onto the bus so the notify bridge can project
	// it to SIEM. Variadic + optional: callers without a framework reference
	// (e.g. the evasion correlator) omit it and the report's axes stay nil.
	if len(tax) > 0 {
		report.OWASPLLM, report.OWASPASI, report.ATLAS = tax[0].llm, tax[0].asi, tax[0].atlas
	}
	if err := m.host.Publish(ctx, event.FromObservation(tenant.String(), Name, report)); err != nil {
		m.debugf("security: publish finding failed", "kind", kind, "err", err)
	}
}

// parseIDOrZero returns ref as a model.ID when it is a canonical UUID, else zero.
// Guardrail/anomaly subjects are often external string refs (an agent name, a
// session id) that are not engine ids; those live in Metadata["subject_ref"], and
// the typed SubjectID stays zero rather than holding a non-id.
func parseIDOrZero(ref string) model.ID {
	if id, err := model.ParseID(ref); err == nil {
		return id
	}
	return ""
}
