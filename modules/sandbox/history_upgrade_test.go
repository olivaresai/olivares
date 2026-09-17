// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandbox

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type legacyHistoryRegistry struct{ store.ExtensionRegistry }

func (r legacyHistoryRegistry) Register(d model.EntityDescriptor) error {
	fields := make([]model.FieldSpec, 0, len(d.Fields))
	for _, f := range d.Fields {
		if f.Name != colLiveRef {
			fields = append(fields, f)
		}
	}
	d.Fields = fields
	return r.ExtensionRegistry.Register(d)
}

func TestScopedHistoryAdditiveUpgrade(t *testing.T) {
	backends := []store.Config{{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "sandbox.db")}}
	if enginetest.PostgresAvailable(t) {
		backends = append(backends, store.Config{Engine: store.EnginePostgres, DSN: enginetest.IsolatedPostgres(t).App})
	} else {
		t.Log("PostgreSQL not exercised without configured fixture")
	}
	for _, cfg := range backends {
		t.Run(string(cfg.Engine), func(t *testing.T) {
			ctx := context.Background()
			m := New()
			old, err := engine.Open(ctx, cfg, func(reg store.ExtensionRegistry) error { return m.RegisterSchema(legacyHistoryRegistry{reg}) })
			if err != nil {
				t.Fatal(err)
			}
			var tenant model.TenantID
			if err := old.System(ctx, func(sys store.SystemScope) error {
				if _, err := sys.EnsureSystemTenant(ctx); err != nil {
					return err
				}
				org, err := sys.CreateOrg(ctx, model.Org{Name: "history", Slug: "history", Status: model.StatusActive})
				tenant = org.TenantID
				return err
			}); err != nil {
				t.Fatal(err)
			}
			stamp := model.NewTimestamp(time.Now()).String()
			runRecord := func() model.Record {
				return model.Record{colKind: runKindReplay, colSubjectRef: "legacy", colRunner: "inproc-mock", colIsolated: true, colRunStatus: "degraded", colStepsTotal: int64(0), colStepsOK: int64(0), colStepsError: int64(0), colDestroyed: true, colStartedAt: stamp, colLaunchedBy: "fixture"}
			}
			var runID, comparisonID model.ID
			if err := old.Mutate(ctx, tenant, func(sc store.Scope) error {
				runs, err := sc.Ext(runKind)
				if err != nil {
					return err
				}
				rec, err := runs.Create(ctx, runRecord())
				if err != nil {
					return err
				}
				runID = model.ID(rec.String(model.ColID))
				comparisons, err := sc.Ext(comparisonKind)
				if err != nil {
					return err
				}
				rec, err = comparisons.Create(ctx, model.Record{colBaselineRun: runID.String(), colCandidateRun: runID.String(), colSubjectRef: "legacy", colVerdict: verdictInconclusive, colBaselineScore: float64(0), colCandScore: float64(0), colDelta: float64(0), colDecidedBy: "fixture", colOccurredAt: stamp})
				if err == nil {
					comparisonID = model.ID(rec.String(model.ColID))
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}
			current, err := engine.Open(ctx, cfg, m.RegisterSchema)
			if err != nil {
				t.Fatal(err)
			}
			defer current.Close()
			if err := current.View(ctx, tenant, func(sc store.Scope) error {
				for kind, id := range map[model.Kind]model.ID{runKind: runID, comparisonKind: comparisonID} {
					repo, err := sc.Ext(kind)
					if err != nil {
						return err
					}
					rec, err := repo.Get(ctx, id)
					if err != nil {
						return err
					}
					if !rec.IsNull(colLiveRef) || rec.String(colSubjectRef) != "legacy" {
						t.Fatal("upgrade relabeled historical subject")
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			live := model.NewID().String()
			if err := current.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(runKind)
				if err != nil {
					return err
				}
				rec := runRecord()
				rec[colLiveRef] = live
				rec[colSubjectRef] = ""
				made, err := repo.Create(ctx, rec)
				if err == nil && toRunDTO(made).LiveRef != live {
					t.Fatal("new run lost live_ref")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
