// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// findingKindRedteam is the persisted Finding.Kind for a red-team result (ARCHITECTURE.md
// §3). busRedteamFailure is the FindingReport.Kind routing key deliver and
// (compliance) consumes.
const (
	findingKindRedteam = "redteam"
	busRedteamFailure  = "redteam_failure"
)

// persistFailure writes a core Finding for a FAILED probe (the agent complied or
// leaked). The sensitive detail is reduced to a one-way hash; the agent ref is a
// non-sensitive entity reference kept in Metadata.
func (m *Module) persistFailure(ctx context.Context, sc store.Scope, sev sdkmodel.Severity, family, agentRef, title, detail string) (model.ID, error) {
	created, err := sc.Findings().Create(ctx, model.Finding{
		Kind: findingKindRedteam, Severity: sevToCore(sev), Status: model.FindingOpen,
		Source: family, SubjectKind: "agent", SubjectID: parseIDOrZero(agentRef),
		Title: clamp(title, maxNameLen), DetailHash: hashBytes(detail), OccurredAt: m.clock.Now(),
		Metadata: map[string]any{"subject_ref": clamp(agentRef, maxRefLen), "family": family},
	})
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

// emitFailure publishes a minimal-data FindingReport for a failed probe on the bus
// (best-effort; a publish failure is logged, not fatal).
func (m *Module) emitFailure(ctx context.Context, tenant model.TenantID, sev sdkmodel.Severity, agentRef, title, detail string) {
	if m.host == nil {
		return
	}
	report := sdkmodel.FindingReport{
		Kind: busRedteamFailure, Severity: sev, SubjectKind: "agent", SubjectRef: clamp(agentRef, maxRefLen),
		Title: clamp(title, maxNameLen), DetailHash: hashHex(detail), OccurredAt: m.clock.Now().Time(),
	}
	if err := m.host.Publish(ctx, event.FromObservation(tenant.String(), Name, report)); err != nil {
		m.debugf("redteam: publish finding failed", "err", err)
	}
}

// parseIDOrZero returns ref as a model.ID when it is a canonical UUID, else zero
// (the agent ref is often an external name, kept in Metadata["subject_ref"]).
func parseIDOrZero(ref string) model.ID {
	if id, err := model.ParseID(ref); err == nil {
		return id
	}
	return ""
}
