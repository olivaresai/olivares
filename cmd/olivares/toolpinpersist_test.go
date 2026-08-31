// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The persistence: rows keyed (tenant, tool), snapshots surviving a
// reload, deletes idempotent, and the system/foreign tenant rejected. Opening
// through boot() also proves the composite schema registrar actually registers
// the mcp.tool_pin entity in every edition.
func TestToolPinPersistenceRoundTrip(t *testing.T) {
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	st := eng.store
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		org, e := sys.CreateOrg(ctx, model.Org{Name: "Acme", Slug: "acme", Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	p := newToolPinPersistence(st, slog.New(slog.DiscardHandler))
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)

	snap := mcpc.PinSnapshot{
		Server: tenant.String(), Tool: "search_web",
		Fingerprint: "fp-1", PinnedAt: now, UpdatedAt: now, PinCount: 1,
	}
	if err := p.UpsertPin(ctx, snap); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Update in place (re-pin) with a drift recorded.
	snap.Fingerprint = "fp-2"
	snap.UpdatedAt = now.Add(time.Hour)
	snap.PinCount = 2
	snap.DriftFingerprint = "fp-evil"
	snap.DriftAt = now.Add(30 * time.Minute)
	if err := p.UpsertPin(ctx, snap); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	loaded, err := p.Load(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("want 1 snapshot, got %d", len(loaded))
	}
	got := loaded[0]
	if got.Server != tenant.String() || got.Tool != "search_web" ||
		got.Fingerprint != "fp-2" || got.PinCount != 2 ||
		got.DriftFingerprint != "fp-evil" ||
		!got.PinnedAt.Equal(now) ||
		!got.DriftAt.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	// UpdatedAt maps to the engine-stamped base column (the row is only written
	// when the pin changes) — assert presence, not the injected instant.
	if got.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt must carry the engine stamp: %+v", got)
	}
	// the CAS base version must SURVIVE the reload. It is the value the console
	// echoes as expected_version, and a boot that rebuilt every snapshot at 0 would
	// silently re-validate stale preconditions that clients still hold — a lost-update
	// window opened by a restart, which is worse than refusing the write.
	firstVersion := got.Version
	if firstVersion == 0 {
		t.Fatalf("reloaded snapshot carries no CAS version: %+v", got)
	}

	// Clearing the drift persists as NULL drift columns.
	snap.DriftFingerprint = ""
	snap.DriftAt = time.Time{}
	if err := p.UpsertPin(ctx, snap); err != nil {
		t.Fatalf("clear drift: %v", err)
	}
	loaded, _ = p.Load(ctx)
	if len(loaded) != 1 || loaded[0].DriftFingerprint != "" || !loaded[0].DriftAt.IsZero() {
		t.Fatalf("drift did not clear: %+v", loaded)
	}
	// ...and it must MOVE with the write, or it is a constant wearing a version's name:
	// every stale precondition would satisfy it forever.
	if loaded[0].Version <= firstVersion {
		t.Fatalf("CAS version did not advance across a write: %d then %d", firstVersion, loaded[0].Version)
	}

	if err := p.DeletePin(ctx, tenant.String(), "search_web"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := p.DeletePin(ctx, tenant.String(), "search_web"); err != nil {
		t.Fatalf("delete absent must be nil, got %v", err)
	}
	if loaded, _ = p.Load(ctx); len(loaded) != 0 {
		t.Fatalf("rows remain after delete: %+v", loaded)
	}

	// The system tenant and garbage server keys are rejected outright.
	if err := p.UpsertPin(ctx, mcpc.PinSnapshot{Server: model.SystemTenantID.String(), Tool: "x", Fingerprint: "f"}); err == nil {
		t.Fatal("system tenant must be rejected")
	}
	if err := p.UpsertPin(ctx, mcpc.PinSnapshot{Server: "not-a-tenant", Tool: "x", Fingerprint: "f"}); err == nil {
		t.Fatal("non-tenant server key must be rejected")
	}
}
