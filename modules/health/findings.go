// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// emitFinding publishes a minimal-data FindingReport on the bus for real-time
// delivery (module XV routes health_* findings to Slack/PagerDuty/SIEM) and
// correlation. The detail is HASHED (one-way); the raw detail is never
// transmitted (docs/SECURITY-HARDENING.md). Best-effort: a publish failure is surfaced at debug,
// never swallowed, but the caller's primary outcome does not depend on it.
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
		m.debugf("health: publish finding failed", "kind", busKind, "err", err)
	}
}
