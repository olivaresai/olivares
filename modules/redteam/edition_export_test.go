// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestStoredRedTeamEvidenceExportsAcrossEditions(t *testing.T) {
	ctx := context.Background()
	m := New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sc store.SystemScope) error {
		if _, err := sc.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sc.CreateOrg(ctx, model.Org{Name: "Stored evidence", Slug: "stored-evidence", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ids := map[model.Kind]string{}
	expected := map[model.Kind]model.Record{}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		now := model.SystemClock{}.Now().String()
		rows := []struct {
			kind model.Kind
			row  model.Record
		}{
			{targetKind, model.Record{colAgentRef: "stored-agent", colName: "Stored target", colAuthorized: true, colTargetStatus: "authorized", colTargetCreated: "user:admin"}},
			{runKind, model.Record{colTargetRef: ids[targetKind], colSuite: "all", colRunStatus: "degraded", colTotal: 1, colPassed: 0, colFailed: 0, colErrors: 0, colSkipped: 1, colScore: 0.0, colStartedAt: now, colLaunchedBy: "user:admin"}},
			{resultKind, model.Record{colRunRef: ids[runKind], colProbeID: "stored-probe", colFamily: "injection", colOutcome: "skipped", colSeverity: "info", colOccurredAt: now}},
		}
		for _, item := range rows {
			if item.kind == runKind {
				item.row[colTargetRef] = ids[targetKind]
			}
			if item.kind == resultKind {
				item.row[colRunRef] = ids[runKind]
			}
			repo, err := sc.Ext(item.kind)
			if err != nil {
				return err
			}
			row, err := repo.Create(ctx, item.row)
			if err != nil {
				return err
			}
			ids[item.kind] = row.String(model.ColID)
			expected[item.kind] = item.row
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Export(ctx, tenant, func(sc store.ExportScope) error {
		for kind, id := range ids {
			repo, err := sc.Ext(kind)
			if err != nil {
				return err
			}
			rows, _, err := repo.List(ctx, model.Query{})
			if err != nil {
				return err
			}
			if len(rows) != 1 || rows[0].String(model.ColID) != id {
				t.Errorf("stored %s missing from export: %+v", kind, rows)
			}
			if len(rows) == 1 {
				for key, value := range expected[kind] {
					gotJSON, err := json.Marshal(rows[0][key])
					if err != nil {
						return err
					}
					wantJSON, err := json.Marshal(value)
					if err != nil {
						return err
					}
					if string(gotJSON) != string(wantJSON) {
						t.Errorf("stored %s field %s changed in export: got %v, want %v", kind, key, rows[0][key], value)
					}
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
