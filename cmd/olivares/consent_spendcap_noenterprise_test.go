// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"context"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Community has no spend-cap authoring path; stored-cap enforcement is tested separately.
func businessSpendCapFenceWriters() []fenceWriter { return nil }

// Seed an existing cap through the same directory authority barrier as other stored retirement fixtures.
func spendCapRetirementSeeder() retirementSeeder {
	return retirementSeeder{"core.policy", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		ctx := context.Background()
		var id model.ID
		mutate := func(fn func(store.Scope) error) error { return e.eng.store.Mutate(ctx, e.tT, fn) }
		if err := auth.FencedWrite(ctx, e.eng.authr, e.tT, []model.ID{s.id}, auth.FenceDirectory, mutate, func(sc store.Scope, _ bool) error {
			p, err := sc.Policies().Create(ctx, model.Policy{Name: "existing-account-cap", Kind: "spend_limit", Enabled: true, Spec: map[string]any{"scope_type": "user", "scope_key": "user:" + s.id.String(), "amount_micro_usd": int64(1000000), "unlimited": false, "period": "monthly"}})
			id = p.ID
			return err
		}); err != nil {
			e.t.Fatal(err)
		}
		return id
	}}
}
