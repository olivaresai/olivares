// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// The persisted model.Finding.Kind this module writes (coarse classification).
const findingKindVoice = "voice"

// The FindingReport.Kind routing keys this module emits on the bus, namespaced
// "voice_*" so a consumer can tell them from other modules' findings. The one
// exception is realtime_session_ungoverned, whose cross-surface name is fixed
// by the voice/PCI brief.
const (
	busPolicyViolation        = "voice_policy_violation"
	busLatencyDegraded        = "voice_latency_degraded"
	busUngovernedOpen         = "voice_ungoverned_open"
	busRecordingSADRisk       = "voice_recording_sad_risk"
	busTranscriptUnclassified = "voice_transcript_unclassified"
	busRealtimeUngoverned     = "realtime_session_ungoverned"
)

// finding is the internal, non-sensitive description of a finding to record.
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
// sensitive detail is reduced to a one-way DetailHash.
func (m *Module) persistFinding(ctx context.Context, sc store.Scope, f finding) error {
	meta := map[string]any{}
	for k, v := range f.meta {
		meta[k] = v
	}
	if f.subjectRef != "" {
		meta["subject_ref"] = clamp(f.subjectRef, maxRefLen)
	}
	_, err := sc.Findings().Create(ctx, model.Finding{
		Kind:        findingKindVoice,
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

// emitFinding publishes a minimal-data FindingReport on the bus. The detail
// is HASHED; the raw detail is never transmitted (docs/SECURITY-HARDENING.md). Best-effort.
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
		m.debugf("voice: publish finding failed", "kind", busKind, "err", err)
	}
}
