// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file adds a minimal-data, read-only accessor over the OPEN legal-hold plane
// (holds.go) for an IN-PROCESS consumer: the long-horizon archive orchestrator
// (the closed enterprise add-on) reads a tenant's active holds to decide which tenants'
// WORM archive segments must carry an object-lock legal hold. It is purely additive and
// read-only — the open binary behaves identically whether or not it is called — and it
// reuses the exact activeHoldRows query the sweep re-checks per batch, so "active" means
// the same thing everywhere.

// LegalHoldInfo is the minimal-data view of one ACTIVE legal hold: its id, scope and
// matter reference. It carries no matter content (docs/SECURITY-HARDENING.md).
type LegalHoldInfo struct {
	ID          string `json:"id"`
	MatterRef   string `json:"matter_ref,omitempty"`
	ScopeKind   string `json:"scope_kind"`             // tenant | data_class | subject
	DataClass   string `json:"data_class,omitempty"`   // set for scope_kind=data_class
	SubjectKind string `json:"subject_kind,omitempty"` // set for scope_kind=subject
	SubjectRef  string `json:"subject_ref,omitempty"`  // set for scope_kind=subject
}

// ActiveLegalHolds lists the tenant's ACTIVE legal holds (minimal data). It is an
// additive, read-only accessor for the composition root's long-horizon archive
// orchestrator; nothing in the open engine's behavior changes whether or not it is
// called.
func (m *Module) ActiveLegalHolds(ctx context.Context, tenant model.TenantID) ([]LegalHoldInfo, error) {
	if m.data == nil {
		return nil, errors.New("compliance: no data handle; cannot read legal holds")
	}
	var out []LegalHoldInfo
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		rows, err := activeHoldRows(ctx, sc)
		if err != nil {
			return err
		}
		for _, h := range rows {
			out = append(out, LegalHoldInfo{
				ID:          h.String(model.ColID),
				MatterRef:   h.String(colLHMatterRef),
				ScopeKind:   h.String(colLHScopeKind),
				DataClass:   h.String(colDataClass),
				SubjectKind: h.String(colSubjectKind),
				SubjectRef:  h.String(colSubjectRef),
			})
		}
		return nil
	})
	return out, err
}
