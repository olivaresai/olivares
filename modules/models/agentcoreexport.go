// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/connectors/agentcore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// AgentCoreExportItems contributes model-access rows to the AgentCore
// Cedar export. It emits structured rows only; the connector translator decides
// whether each model target, surface or subject can be mapped to AgentCore.
func (m *Module) AgentCoreExportItems(ctx context.Context, tenant model.TenantID) ([]agentcore.ExportItem, error) {
	if m.data == nil {
		return nil, errors.New("models: data handle unavailable for AgentCore export")
	}
	var out []agentcore.ExportItem
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		recs, err := drainExt(ctx, sc, modelAccessKind)
		if err != nil {
			return err
		}
		out = make([]agentcore.ExportItem, 0, len(recs))
		for _, rec := range recs {
			out = append(out, agentcore.ExportItem{
				Kind:        "model_access",
				Tenant:      tenant.String(),
				SubjectKind: rec.String(colMASubjectKind),
				SubjectRef:  rec.String(colMASubjectRef),
				ScopeKind:   "workspace",
				Workspace:   rec.String(colMAWorkspace),
				Effect:      agentCoreModelAccessEffect(rec.String(colMAEffect)),
				Models:      []string{rec.String(colMATargetRef)},
				Surfaces:    parseStrings(rec.String(colMASurfaces)),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func agentCoreModelAccessEffect(effect string) string {
	if normalizeEffect(effect) == effectForbid {
		return "forbid"
	}
	return "permit"
}
