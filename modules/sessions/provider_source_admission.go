// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

// AdmitSourceRegistration is a HOST composition port, called once per observation
// before bus publication. Opening fixed source/revision/environment; admission
// fixes the approved binding decision before queue/replay. It never consumes a
// binding supplied by a connector or inherited from a previous observation.
func (m *Module) AdmitSourceRegistration(ctx context.Context, tenant string, reg event.SourceRegistration) (event.SourceRegistration, error) {
	reg.BindingRef = ""
	if m.data == nil {
		return reg, errNoData
	}
	if tenant == "" || !reg.Valid() {
		return reg, badRequest("source admission requires tenant and exact registration")
	}
	attempt := func() error {
		reg.BindingRef = ""
		return m.data.Mutate(ctx, model.TenantID(tenant), func(sc store.Scope) error {
			repo, err := sc.Ext(providerBindingKind)
			if err != nil {
				return err
			}
			rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{
				eq(colPBSourceID, reg.SourceID), model.Filter{Column: colPBSourceRev, Op: model.OpEq, Value: reg.SourceRevision}, eq(colPBEnvRef, reg.EnvironmentRef), eq(colPBSelector, SelectorDedicated),
			}, Limit: 1})
			if err != nil || len(rows) == 0 {
				return err
			}
			rec := rows[0]
			if rec.String(colPBState) != BindingActive {
				return nil
			}
			// Version CAS orders admission against revocation. An event admitted before
			// revocation keeps this durable reference even if delivered afterwards.
			if _, err := repo.Update(ctx, rec); err != nil {
				return err
			}
			reg.BindingRef = rec.String(colPBRef)
			return nil
		})
	}
	err := attempt()
	if errors.Is(err, store.ErrConflict) {
		err = attempt()
	}
	if err != nil {
		reg.BindingRef = ""
	}
	return reg, err
}

// historicalBinding resolves only the exact decision the host admitted. State is
// deliberately historical: revocation affects new admission, never replay. A
// reference from another source/revision/environment cannot attribute this event.
func historicalBinding(ctx context.Context, sc store.Scope, reg *event.SourceRegistration) (ProviderSourceBinding, bool, error) {
	if !validBindingRef(reg.BindingRef) {
		return ProviderSourceBinding{}, false, nil
	}
	repo, err := sc.Ext(providerBindingKind)
	if err != nil {
		return ProviderSourceBinding{}, false, err
	}
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colPBRef, reg.BindingRef)}, Limit: 1})
	if err != nil || len(rows) == 0 {
		return ProviderSourceBinding{}, false, err
	}
	b := bindingFromRecord(rows[0])
	return b, b.SourceID.String() == reg.SourceID && b.SourceRevision == reg.SourceRevision && b.EnvironmentRef == reg.EnvironmentRef && b.SelectorKey == SelectorDedicated, nil
}
