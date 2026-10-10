// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

func TestManagedCostUsesOnlyItsTrustedAttemptLink(t *testing.T) {
	eachIngestBackend(t, func(t *testing.T, cfg store.Config) {
		for _, mode := range []string{"managed", "untrusted label", "unattributed historical", "invalid", "missing row"} {
			t.Run(mode, func(t *testing.T) {
				m, st, tenant, _ := openFinCfg(t, cfg)
				var attempt, decoy model.Session
				if err := st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
					var err error
					attempt, err = sc.Sessions().Create(t.Context(), model.Session{ExternalID: "launch-one", State: model.SessionRunning})
					if err != nil {
						return err
					}
					decoy, err = sc.Sessions().Create(t.Context(), model.Session{ExternalID: "canonical-sid", State: model.SessionRunning})
					return err
				}); err != nil {
					t.Fatal(err)
				}
				cost := mkCost("anthropic", "model", "canonical-sid", 7, 3, 17, baseTime)
				id := attempt.ID.String()
				switch mode {
				case "invalid":
					id = "invalid"
				case "missing row":
					id = model.NewID().String()
				case "unattributed historical":
					id = ""
				}
				cost.Labels = map[string]string{"olivares.core_session_id": id}
				e := event.FromObservation(tenant.String(), "olivares.sessions", cost)
				e.SessionProjection = mode != "untrusted label"
				err := m.onEvent(t.Context(), e)
				if mode == "invalid" || mode == "missing row" {
					if err == nil || countCosts(t, st, tenant) != 0 {
						t.Fatalf("bad attempt recorded: err=%v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := m.onEvent(t.Context(), e); err != nil {
					t.Fatal(err)
				}
				if err := st.View(t.Context(), tenant, func(sc store.Scope) error {
					rows, _, err := sc.Costs().List(context.Background(), model.Query{Limit: 10})
					if err != nil {
						return err
					}
					want := attempt.ID
					if mode == "untrusted label" {
						want = decoy.ID
					}
					if mode == "unattributed historical" {
						want = ""
					}
					if len(rows) != 1 || rows[0].SessionID != want || rows[0].CostMicroUSD != 17 || rows[0].InputTokens != 7 {
						t.Fatalf("wrong attribution or duplicate: rows=%+v want=%s", rows, want)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			})
		}
	})
}
