// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// The persisted model.Finding.Kind this module writes (coarse classification on
// the core Finding entity, ARCHITECTURE.md).
const findingKindOrchestration = "orchestration"

// The FindingReport.Kind routing keys this module emits on the bus. Route
// on these to deliver to SIEM/Slack/PagerDuty (compliance) consumes them.
// They are namespaced "orchestration_*" so a consumer can tell them from a
// security_*/voice_*/finops_* finding.
const (
	busCadenceMiss    = "orchestration_cadence_miss"
	busUngovernedFire = "orchestration_ungoverned_fire"
)

// finding is the internal, non-sensitive description of a finding to record. The
// detail is the redaction-safe fingerprint string that becomes DetailHash — no raw
// payload is ever carried here (docs/SECURITY-HARDENING.md).
type finding struct {
	kind        string
	severity    sdkmodel.Severity
	subjectKind string
	subjectRef  string
	title       string
	detail      string
	meta        map[string]any
}

// persistFinding writes a core model.Finding row in the caller's transaction. The
// sensitive detail is reduced to a one-way DetailHash; the string subject ref is
// kept (non-sensitive) in Metadata, and SubjectID is set only when the ref is a
// real engine id.
func (m *Module) persistFinding(ctx context.Context, sc store.Scope, f finding) error {
	meta := map[string]any{}
	for k, v := range f.meta {
		meta[k] = v
	}
	if f.subjectRef != "" {
		meta["subject_ref"] = clamp(f.subjectRef, maxRefLen)
	}
	_, err := sc.Findings().Create(ctx, model.Finding{
		Kind:        findingKindOrchestration,
		Severity:    sevToCore(f.severity),
		Status:      model.FindingOpen,
		Source:      Name,
		SubjectKind: f.subjectKind,
		SubjectID:   parseIDOrZero(f.subjectRef),
		Title:       clamp(f.title, maxNameLen),
		DetailHash:  hashBytes(f.detail),
		OccurredAt:  m.clock.Now(),
		Metadata:    meta,
	})
	return err
}

// emitFinding publishes a minimal-data FindingReport on the bus for real-time
// delivery and correlation. The detail is HASHED (one-way); the raw
// detail is never transmitted (docs/SECURITY-HARDENING.md). Best-effort: a publish failure is
// surfaced, never swallowed, but the caller's primary outcome does not depend on it.
func (m *Module) emitFinding(ctx context.Context, tenant model.TenantID, busKind string, sev sdkmodel.Severity, subjectKind, subjectRef, title, detail string) {
	if m.host == nil {
		return
	}
	report := sdkmodel.FindingReport{
		Kind:        busKind,
		Severity:    sev,
		SubjectKind: subjectKind,
		SubjectRef:  clamp(subjectRef, maxRefLen),
		Title:       clamp(title, maxNameLen),
		DetailHash:  hashHex(detail),
		OccurredAt:  m.clock.Now().Time(),
	}
	if err := m.host.Publish(ctx, event.FromObservation(tenant.String(), Name, report)); err != nil {
		m.debugf("orchestration: publish finding failed", "kind", busKind, "err", err)
	}
}
