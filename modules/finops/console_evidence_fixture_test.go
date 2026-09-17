// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package finops

import (
	"context"
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"net/http/httptest"
	"os"
	"testing"
)

type consoleScopedData struct {
	tsScopedData
	data api.ModuleData
}

func (d consoleScopedData) View(ctx context.Context, fn func(store.Scope) error) error {
	return d.data.View(ctx, d.tenant, fn)
}

// Handler bodies, with real SQLite rows and the existing finite incomplete-reader
// fixtures. This tests DTO projection, not auth middleware (the HTTP test does).
// Optional explicit export writes only these exact response bytes for UI tests.
func TestFinopsConsoleHandlerFixtures(t *testing.T) {
	fixtures := map[string]map[string]json.RawMessage{}
	for _, name := range []string{"exact", "lower_bound", "unknown", "zero", "wide", "historical", "malformed", "future"} {
		t.Run(name, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			m.clock = &fakeClock{t: baseTime}
			reserve := int64(0)
			cost := int64(12000000)
			if name == "lower_bound" {
				reserve = 12000000
				cost = 0
				forcePager(m, func(int, model.Query) ([]model.Record, model.Page) { return nil, model.Page{HasMore: true} })
			}
			if name == "zero" {
				cost = 0
			}
			if name == "wide" {
				cost = 9007199254740993
			}
			id := createBudget(t, st, tenant, name, budgetSpec{Dimension: "global", Period: "monthly", Currency: "USD", LimitMicroUSD: 10000000, ReservedMicroUSD: reserve, Action: "alert", Thresholds: []float64{1}})
			m.ingest(t, tenant, mkCost("fixture", "fixture", "fixture", 1, 1, cost, baseTime))
			if name == "unknown" {
				forceCostPages(m, func(int, model.Query) ([]model.Record, model.Page) {
					return []model.Record{costSampleRow(tenant, baseTime, cost)}, model.Page{HasMore: true}
				})
			}
			if name == "historical" || name == "malformed" || name == "future" {
				row := alertRows(t, st, tenant)[0]
				switch name {
				case "historical":
					row[colAlertEvidence] = nil
					row[colAlertEvidenceHash] = nil
				case "malformed":
					row[colAlertEvidence] = "{"
				case "future":
					var env alertEvidenceEnvelope
					if err := json.Unmarshal([]byte(row.String(colAlertEvidence)), &env); err != nil {
						t.Fatal(err)
					}
					env.SchemaVersion = 2
					b, _ := json.Marshal(env)
					row[colAlertEvidence] = string(b)
				}
				if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
					repo, e := sc.Ext(budgetAlertKind)
					if e != nil {
						return e
					}
					_, e = repo.Update(context.Background(), row)
					return e
				}); err != nil {
					t.Fatal(err)
				}
			}
			mc := api.ModuleContext{Tenant: tenant, Data: consoleScopedData{tsScopedData: tsScopedData{st: st, tenant: tenant}, data: m.data}}
			req := httptest.NewRequest("GET", "/budgets/"+id.String()+"/status", nil)
			rc := chi.NewRouteContext()
			rc.URLParams.Add("id", id.String())
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
			status := httptest.NewRecorder()
			m.handleBudgetStatus(status, req, mc)
			alerts := httptest.NewRecorder()
			m.handleListAlerts(alerts, httptest.NewRequest("GET", "/alerts?limit=1", nil), mc)
			if status.Code != 200 || alerts.Code != 200 {
				t.Fatalf("status=%d %s alerts=%d %s", status.Code, status.Body.String(), alerts.Code, alerts.Body.String())
			}
			var got budgetStatusDTO
			if err := json.Unmarshal(status.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			want := "exact"
			if name == "lower_bound" || name == "unknown" {
				want = name
			}
			if got.Amount == nil || got.Amount.Class != want {
				t.Fatalf("amount=%+v want %s", got.Amount, want)
			}
			if name == "wide" && (got.Amount.EffectiveMicroUSD == nil || *got.Amount.EffectiveMicroUSD != "9007199254740993") {
				t.Fatal("wide decimal changed")
			}
			if name == "zero" && (got.Amount.EffectiveMicroUSD == nil || *got.Amount.EffectiveMicroUSD != "0") {
				t.Fatal("zero lost")
			}
			var list listResponse[alertDTO]
			if err := json.Unmarshal(alerts.Body.Bytes(), &list); err != nil {
				t.Fatal(err)
			}
			if name != "zero" && len(list.Items) != 1 {
				t.Fatalf("alerts=%d", len(list.Items))
			}
			if name == "historical" || name == "malformed" || name == "future" {
				if list.Items[0].Evidence.State != evidenceUnknownRead {
					t.Fatalf("broken evidence became valid")
				}
			}
			fixtures[name] = map[string]json.RawMessage{"status": append([]byte{}, status.Body.Bytes()...), "alerts": append([]byte{}, alerts.Body.Bytes()...)}
			t.Logf("fixture=%s class=%s alerts=%d", name, got.Amount.Class, len(list.Items))
		})
	}
	if t.Failed() {
		return
	}
	if path := os.Getenv("FINOPS_CONSOLE_FIXTURES"); path != "" {
		b, err := json.MarshalIndent(fixtures, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, append(b, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		t.Log("exported actual handler JSON")
	}
}
