// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"context"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

// onBusEvent dispatches the module's bus subscription by event type: the estate's
// finding stream feeds the anomaly reactor (onEvent), and the redacted observed-
// text stream feeds the guardrail detector chain (onGuardrailObserved).
// One subscription keeps the lifecycle simple (a single cancel).
func (m *Module) onBusEvent(ctx context.Context, e event.Event) error {
	switch e.Type {
	case event.TypeFindingReported:
		return m.onEvent(ctx, e)
	case event.TypeGuardrailObserved:
		return m.onGuardrailObserved(ctx, e)
	default:
		return nil
	}
}

// onGuardrailObserved is the hook: it runs the guardrail detector chain
// over a redacted excerpt of observed agent text that flowed on the bus (a
// TypeGuardrailObserved event), persisting and emitting a finding per detection —
// so the OWASP/ATLAS/ASI taxonomy fires on real estate traffic AUTOMATICALLY,
// without a caller POSTing to /guardrails/inspect. It is DETECTIVE ONLY: it never
// blocks. Inline enforcement stays the governed, opt-in /enforcement path
// (docs/SECURITY-HARDENING.md); routing observed text here changes detection coverage, never the
// posture. The producer guarantees the text is already redacted (docs/SECURITY-HARDENING.md); the
// handler clamps it defensively and each detector's excerpt is itself redacted, so
// no raw payload is persisted or re-emitted.
func (m *Module) onGuardrailObserved(ctx context.Context, e event.Event) error {
	if m.data == nil {
		return nil
	}
	ot, ok := event.ObservedTextOf(e)
	if !ok {
		return nil
	}
	surface := Surface(strings.TrimSpace(ot.Surface))
	if !validSurface(surface) || strings.TrimSpace(ot.Text) == "" {
		return nil // nothing inspectable; never a finding
	}
	tenant := model.TenantID(e.Tenant)
	if tenant.IsZero() || tenant.IsSystem() {
		return nil // guardrail findings are tenant-scoped; never the system partition
	}

	in := GuardrailInput{
		Surface:     surface,
		Text:        clamp(ot.Text, maxTextLen),
		AgentRef:    clamp(strings.TrimSpace(ot.AgentRef), maxRefLen),
		SessionRef:  clamp(strings.TrimSpace(ot.SessionRef), maxRefLen),
		ResourceRef: clamp(strings.TrimSpace(ot.ResourceRef), maxRefLen),
	}
	detections := m.inspect(ctx, in)
	if len(detections) == 0 {
		return nil
	}
	subjectKind, subjectRef := in.subject()

	// Persist a finding per detection (the module is the first producer of core
	// findings). Detective only: no enforcement policy is consulted and no
	// verdict is computed — the automatic path detects and records, never blocks.
	if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		for _, d := range detections {
			if _, ferr := m.persistFinding(ctx, sc, finding{
				kind:        findingKindGuardrail,
				severity:    d.Severity,
				source:      d.Class,
				subjectKind: subjectKind,
				subjectRef:  subjectRef,
				title:       d.Title,
				detail:      d.detail(),
				meta: aivssMeta(d, taxonomyMeta(d, map[string]any{
					"rule": d.Rule, "surface": string(surface),
					"origin": e.Source, "automatic": true,
				})),
			}); ferr != nil {
				return ferr
			}
		}
		return nil
	}); err != nil {
		return err // logged by the bus; writes are idempotent enough that redelivery is safe
	}

	// Mirror the findings onto the bus for real-time delivery and
	// cross-module correlation, exactly as the /guardrails/inspect path does. The
	// emitted FindingReport carries a one-way DetailHash, never the excerpt.
	for _, d := range detections {
		m.emitFinding(ctx, tenant, busGuardrail, d.Severity, subjectKind, subjectRef, d.Title, d.detail(), axesOf(d))
	}
	return nil
}
