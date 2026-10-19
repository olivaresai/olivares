// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type preLaunchSpeedRegistry struct{ store.ExtensionRegistry }

func (r preLaunchSpeedRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == runKind {
		fields := make([]model.FieldSpec, 0, len(d.Fields))
		for _, f := range d.Fields {
			if f.Name != "fast_mode" && f.Name != "service_tier" && f.Name != "context_windows" {
				fields = append(fields, f)
			}
		}
		d.Fields = fields
	}
	return r.ExtensionRegistry.Register(d)
}

func launchSpeedUpgradeCase(t *testing.T, cfg store.Config) {
	t.Helper()
	ctx := context.Background()
	m := New()
	old, err := engine.Open(ctx, cfg, func(reg store.ExtensionRegistry) error {
		return m.RegisterSchema(preLaunchSpeedRegistry{reg})
	})
	if err != nil {
		t.Fatal(err)
	}
	var tenant model.TenantID
	if err := old.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Speed upgrade", Slug: "speed-upgrade", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var row model.Record
	if err := old.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		row, err = repo.Create(ctx, model.Record{colRunRef: "old-speed-run", colTransport: "stream-json",
			colPermissionMode: "default", colIsolation: "native", colState: stateStopped, colLastEventSeq: int64(0), colRunName: "Existing session"})
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
	read := func(st store.Store) model.Record {
		t.Helper()
		var out model.Record
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			out, err = repo.Get(ctx, model.ID(row.String(model.ColID)))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	got := read(current)
	if got.String(colRunName) != "Existing session" || got["fast_mode"] != nil || got["service_tier"] != nil || got["context_windows"] != nil {
		t.Fatalf("upgrade changed the existing run or invented speed choices: %v", got)
	}
	if err := current.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		got["fast_mode"], got["service_tier"] = false, "future-priority"
		got["context_windows"] = `{"claude-opus-5-5[1m]":1000000}`
		_, err = repo.Update(ctx, got)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := engine.Open(ctx, cfg, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close() //nolint:errcheck
	got = read(reopened)
	if got["fast_mode"] != false || got.String("service_tier") != "future-priority" || got.String("context_windows") != `{"claude-opus-5-5[1m]":1000000}` {
		t.Fatalf("new choices did not survive restart: fast_mode=%v service_tier=%v", got["fast_mode"], got["service_tier"])
	}
}

func TestLaunchSpeedUpgradeSQLite(t *testing.T) {
	launchSpeedUpgradeCase(t, store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "speed.db"), Debug: true})
}

func TestLaunchSpeedUpgradePostgres(t *testing.T) {
	if !enginetest.PostgresAvailable(t) {
		t.Skip("PostgreSQL runtime unavailable")
	}
	pg := enginetest.IsolatedPostgres(t)
	launchSpeedUpgradeCase(t, store.Config{Engine: store.EnginePostgres, DSN: pg.App, Debug: true})
}
