// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestCheckpointerDoesNotClaimSuccessOverAnUnenumerableEstate is the scheduler half
// of the H-02 contract, and it is on Postgres for the same reason its core
// counterpart is: on SQLite the enumeration is always authoritative, so a SQLite
// test here would be green over the exact configuration that fails in production.
//
// The chain being pinned is the one that produced the false certification:
// CheckpointAll returned nil having enumerated nothing, so once() stored
// lastSuccess and logged "audit checkpoint written for all tenants"
// (cmd/olivares/checkpoint.go:120-128). lastSuccess is not cosmetic — it is the
// source of the olivares_audit_checkpoint_age_seconds SLI, so a moved timestamp
// tells every dashboard and alert that the anchor is fresh while no tenant was
// anchored at all.
func TestCheckpointerDoesNotClaimSuccessOverAnUnenumerableEstate(t *testing.T) {
	pg := enginetest.IsolatedPostgres(t)
	ctx := context.Background()

	// App DSN only — no AdminDSN, so cross-tenant System reads cannot enumerate.
	eng, err := boot(ctx, bootConfig{
		DataDir: t.TempDir(), Engine: "postgres", DSN: pg.App,
		Logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("boot on app-only postgres: %v", err)
	}
	defer func() { _ = eng.Close() }()

	// A tenant carrying an event: real material that must not be silently skipped.
	var tenant model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		org, cerr := sys.CreateOrg(ctx, model.Org{Name: "cpsched", Slug: "cpsched", Status: model.StatusActive})
		tenant = org.TenantID
		return cerr
	}); err != nil {
		t.Fatalf("create org: %v", err)
	}
	if err := eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, aerr := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: "test.event", TargetKind: "core.test",
		})
		return aerr
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	cp := startCheckpointer(eng.signer, eng.store, time.Hour, eng.log, eng.metrics)
	defer func() { close(cp.stopCh); <-cp.doneCh }()

	if got := cp.lastSuccess.Load(); got != 0 {
		t.Fatalf("lastSuccess = %d before any run, want 0", got)
	}
	cp.once(ctx)
	if got := cp.lastSuccess.Load(); got != 0 {
		t.Fatalf("lastSuccess moved to %d after a run that could NOT enumerate tenants: the checkpoint-age SLI now reports a fresh anchor over an estate nothing was written for", got)
	}
}

// TestCheckpointerRecordsSuccessWhenItCanEnumerate is the counterweight: with the
// BYPASSRLS admin pool the same run must succeed and MOVE lastSuccess. Without this
// leg, "never move lastSuccess" would satisfy the test above and break the SLI.
func TestCheckpointerRecordsSuccessWhenItCanEnumerate(t *testing.T) {
	pg := enginetest.IsolatedPostgres(t)
	ctx := context.Background()

	eng, err := boot(ctx, bootConfig{
		DataDir: t.TempDir(), Engine: "postgres", DSN: pg.App, AdminDSN: pg.Admin,
		Logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("boot with admin pool: %v", err)
	}
	defer func() { _ = eng.Close() }()

	cp := startCheckpointer(eng.signer, eng.store, time.Hour, eng.log, eng.metrics)
	defer func() { close(cp.stopCh); <-cp.doneCh }()

	cp.once(ctx)
	if got := cp.lastSuccess.Load(); got == 0 {
		t.Fatal("lastSuccess did not move on a run that CAN enumerate the estate: the checkpoint-age SLI would never be emitted")
	}
}
