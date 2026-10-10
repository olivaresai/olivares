// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"log/slog"
	"strconv"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/sessions"
)

// The FinOps ledger kinds, spelled here because a test outside modules/finops cannot
// import the module's unexported constants. Reading them straight from the store is how
// these tests see the holds a gate left, without a report that summarizes them.
const (
	finopsReservationKind model.Kind = "finops.budget_reservation"
	finopsAdmissionKind   model.Kind = "finops.admission_idempotency"
)

// ledgerListLimit bounds one read of a test ledger; every fixture here stays far below it.
const ledgerListLimit = 200

// forEachFinOpsEngine runs body once on SQLite and once on PostgreSQL. Without a PostgreSQL
// server the second leg is skipped by name, and the skip says it is not a pass.
func forEachFinOpsEngine(t *testing.T, body func(*testing.T, store.Config)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		body(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	})
	t.Run("postgres", func(t *testing.T) {
		if !enginetest.PostgresAvailable(t) {
			t.Skipf("%s unset: this PostgreSQL leg is NOT RUN. This is not a pass.", enginetest.EnvSuperuserDSN)
		}
		body(t, store.Config{Engine: store.EnginePostgres, DSN: enginetest.IsolatedPostgres(t).App, MaxConns: 8})
	})
}

// openFinOpsEngine opens the FinOps module over a real in-memory SQLite store with one
// active business org: the same shape boot builds, so a gate under test talks to the real
// admission ledger.
func openFinOpsEngine(t *testing.T) (*finops.Module, store.Store, model.TenantID) {
	t.Helper()
	return openFinOpsEngineOn(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
}

// openFinOpsEngineOn is openFinOpsEngine on the store cfg names.
func openFinOpsEngineOn(t *testing.T, cfg store.Config) (*finops.Module, store.Store, model.TenantID) {
	t.Helper()
	ctx := context.Background()
	fin := finops.New()
	st, err := coreengine.Open(ctx, cfg, fin.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "admission-gates", Slug: "admission-gates", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	fin.UseData(api.NewModuleData(st))
	return fin, st, tenant
}

// listFinOpsRows lists every row of one FinOps ledger kind of the tenant, straight from the
// store.
func listFinOpsRows(t *testing.T, st store.Store, tenant model.TenantID, kind model.Kind) []model.Record {
	t.Helper()
	var (
		out  []model.Record
		more bool
	)
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		rows, page, err := repo.List(context.Background(), model.Query{Limit: ledgerListLimit})
		out, more = rows, page.HasMore
		return err
	}); err != nil {
		t.Fatalf("list %s: %v", kind, err)
	}
	if more {
		t.Fatalf("%s holds more than %d rows: the fixture outgrew one read", kind, ledgerListLimit)
	}
	return out
}

// TestSessionLaunchGate_LaunchesLeaveNoHoldToSettle is the end-to-end of the decision this
// gate makes about amounts. A launch never learns what the session spent, so it can
// neither commit nor release, and a hold nobody can settle is drift the engine produces
// itself, reported to the operator as if a caller had misbehaved. Every enforcing budget is
// still evaluated; nothing is held, so no ledger row exists, and each launch leaves its
// admission row published with no hold.
//
// It drives the real module on both engines, so the assertion is over the real ledger and
// not over a recorded call.
func TestSessionLaunchGate_LaunchesLeaveNoHoldToSettle(t *testing.T) {
	forEachFinOpsEngine(t, func(t *testing.T, cfg store.Config) {
		fin, st, tenant := openFinOpsEngineOn(t, cfg)
		createBudgetPolicy(t, st, tenant, "launch-cap", map[string]any{
			"dimension": "global", "period": "monthly",
			"limit_micro_usd": int64(5_000_000), "action": "block",
		})
		g := &sessionLaunchGate{
			fin:           fin,
			budgetPosture: availabilityFailClosed,
			log:           slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		}

		const launches = 4
		for i := 0; i < launches; i++ {
			dec, err := g.Authorize(context.Background(), tenant, sessions.LaunchIntent{
				PermissionMode: "default", RunRef: "run-" + strconv.Itoa(i), AgentRef: "agent-1",
			})
			if err != nil {
				t.Fatalf("launch %d: %v", i, err)
			}
			if !dec.Allowed {
				t.Fatalf("launch %d was denied under a cap with headroom: %+v", i, dec)
			}
		}

		if rows := listFinOpsRows(t, st, tenant, finopsReservationKind); len(rows) != 0 {
			t.Fatalf("after %d launches the ledger holds %d reservation row(s) nobody can settle: %v", launches, len(rows), rows)
		}
		adm := listFinOpsRows(t, st, tenant, finopsAdmissionKind)
		if len(adm) != launches {
			t.Fatalf("admission rows = %d, want one per launch (%d): the gate did not ask admission", len(adm), launches)
		}
		for _, r := range adm {
			if r.String("state") != "reserved" || r.String("handle") != "" {
				t.Fatalf("launch admission row %q = state %q handle %q, want reserved with no hold",
					r.String("idempotency_key"), r.String("state"), r.String("handle"))
			}
		}
	})
}
