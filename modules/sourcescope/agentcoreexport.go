// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/connectors/agentcore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// AgentCoreExportItems contributes connector→workspace assignments to the
// AgentCore Cedar export. It does not filter unmapped connectors; the
// connector translator reports unsupported rows with machine-readable reasons.
func (m *Module) AgentCoreExportItems(ctx context.Context, tenant model.TenantID) ([]agentcore.ExportItem, error) {
	data := m.moduleData()
	if data == nil {
		return nil, errors.New("sourcescope: data handle unavailable for AgentCore export")
	}
	var out []agentcore.ExportItem
	err := data.View(ctx, tenant, func(sc store.Scope) error {
		recs, err := allExt(ctx, sc, assignmentKind)
		if err != nil {
			return err
		}
		out = make([]agentcore.ExportItem, 0, len(recs))
		for _, rec := range recs {
			if !rec.Bool(colAssignEnabled) {
				continue
			}
			workspace := rec.String(colAssignWorkspace)
			out = append(out, agentcore.ExportItem{
				Kind:        "source_scope",
				Tenant:      tenant.String(),
				SubjectKind: "workspace",
				SubjectRef:  workspace,
				ScopeKind:   "workspace",
				Workspace:   workspace,
				Effect:      "permit",
				Sources:     []string{rec.String(colAssignConnector)},
				Access:      normalizeAssignmentMode(rec.String(colAssignMode)),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
