// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

import (
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// operationDTO is one immutable entry of the change-management ledger: what
// lifecycle action ran, the version transition, the plan it was bound to, WHO
// approved it (via the governance approval ref + the gate status consumed) and
// the outcome. It is the change-of-infrastructure evidence (compliance)
// reads, distinct from — and complementary to — the core hash-chain audit.
type operationDTO struct {
	DefinitionID string `json:"definition_id"`
	Op           string `json:"op"`
	FromVersion  int64  `json:"from_version"`
	ToVersion    int64  `json:"to_version"`
	PlanHash     string `json:"plan_hash,omitempty"`
	ApprovalRef  string `json:"approval_ref,omitempty"`
	GateStatus   string `json:"gate_status,omitempty"`
	Status       string `json:"status"`
	Actor        string `json:"actor,omitempty"`
	Result       string `json:"result,omitempty"`
	OccurredAt   string `json:"occurred_at,omitempty"`
}

func toOperationDTO(rec model.Record) operationDTO {
	return operationDTO{
		DefinitionID: rec.String(colDefinitionRef), Op: rec.String(colOp),
		FromVersion: rec.Int(colFromVersion), ToVersion: rec.Int(colToVersion),
		PlanHash: rec.String(colPlanHash), ApprovalRef: rec.String(colApprovalRef), GateStatus: rec.String(colGateStatus),
		Status: rec.String(colOpStatus), Actor: rec.String(colActor), Result: rec.String(colResult),
		OccurredAt: rec.String(colOccurredAt),
	}
}

// handleListOperations lists the append-only operation ledger, optionally
// filtered by definition_id, op or status. Read-tier (deploy:deployment:read).
func (m *Module) handleListOperations(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("definition_id")); v != "" {
		q.Filters = append(q.Filters, eq(colDefinitionRef, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("op")); v != "" {
		q.Filters = append(q.Filters, eq(colOp, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("status")); v != "" {
		q.Filters = append(q.Filters, eq(colOpStatus, v))
	}
	out := listResponse[operationDTO]{Items: []operationDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(operationKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toOperationDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
