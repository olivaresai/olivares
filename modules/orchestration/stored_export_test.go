// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package orchestration

import (
	"context"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"testing"
	"time"
)

// Registered orchestration relations remain readable through Store.Export.
// This package-local SQLite check does not qualify Community DR backup of a
// full Business installation, which also requires cross-edition bootstrap admission.
func TestStoredOrchestrationRemainsExportable(t *testing.T) {
	ctx := context.Background()
	m := New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err = st.System(ctx, func(sc store.SystemScope) error { _, e := sc.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	tenant := model.SystemTenantID
	var id model.ID
	err = st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(relationKind)
		if e != nil {
			return e
		}
		row, e := repo.Create(ctx, model.Record{colSupervisorRef: "stored-origin", colWorkerRef: "stored-worker", colLinkKind: "delegation", colToolRef: "", colMode: "read", colSignalSource: "observed", colConfidence: "attributed", colDelegationCnt: int64(1), colFirstSeenAt: time.Now().UTC(), colLastSeenAt: time.Now().UTC()})
		if e == nil {
			id = model.ID(row.String(model.ColID))
		}
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	err = st.Export(ctx, tenant, func(sc store.ExportScope) error {
		repo, e := sc.Ext(relationKind)
		if e != nil {
			return e
		}
		row, e := repo.Get(ctx, id)
		if e != nil {
			return e
		}
		if row.String(colSupervisorRef) != "stored-origin" || row.String(colWorkerRef) != "stored-worker" {
			t.Fatal("stored relation changed during export")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
