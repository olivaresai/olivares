// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/store"
)

// finopsData is the composition-root-only extension FinOps needs for preventive
// user_group budgets: tenant spend reads still go through ModuleData, while group
// membership fan-out can read the auth partition through Store.AuthView.
type finopsData struct {
	api.ModuleData
	st store.Store
}

func (d finopsData) AuthView(ctx context.Context, fn func(store.AuthScope) error) error {
	return d.st.AuthView(ctx, fn)
}
